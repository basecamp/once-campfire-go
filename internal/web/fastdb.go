package web

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/fastdb"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// parseFastDB maps a CAMPFIRE_FASTDB value to its setting, accepting the same
// shapes as parseRecordedPieces. An unrecognised value reports valid=false so
// the caller can warn while keeping the default on rather than silently
// changing behaviour.
func parseFastDB(raw string) (enabled, valid bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on", "true", "1":
		return true, true
	case "off", "false", "0":
		return false, true
	default:
		return true, false
	}
}

// roomOf converts a fastdb room record to the database type the page
// assembly uses. The strings are the same bytes fastdb decoded; the copy is a
// struct assignment, not a re-decode.
func roomOf(r fastdb.Room) database.Room {
	return database.Room{ID: r.ID, CreatorID: r.CreatorID, Name: r.Name, Type: r.Type, UpdatedAt: r.UpdatedAt}
}

func userOf(u fastdb.User) database.User {
	return database.User{ID: u.ID, Name: u.Name, Email: u.Email, Password: u.Password, Bio: u.Bio, BotToken: u.BotToken, UpdatedAt: u.UpdatedAt, Role: u.Role, Status: u.Status}
}

func usersOf(users []fastdb.User) []database.User {
	out := make([]database.User, 0, len(users))
	for _, u := range users {
		out = append(out, userOf(u))
	}
	return out
}

func messageOf(m fastdb.Message) database.Message {
	return database.Message{ID: m.ID, RoomID: m.RoomID, CreatorID: m.CreatorID, ClientID: m.ClientID, Body: m.Body, Creator: m.Creator, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

// fastConn borrows one fastdb connection for the request and returns the
// release function. c is nil when the fast read path is off or the pool
// cannot serve the borrow (closing down, request cancelled): callers fall
// back to the database/sql readers for that request, which is the same
// behaviour CAMPFIRE_FASTDB=off selects.
func (s *Server) fastConn(r *http.Request) (c *fastdb.Conn, release func()) {
	return s.fastConnCtx(r.Context())
}

// fastConnCtx is fastConn for call sites that only hold a context (messageViews
// is called from request handlers and from background webhook delivery).
func (s *Server) fastConnCtx(ctx context.Context) (c *fastdb.Conn, release func()) {
	if s.fastdb == nil {
		return nil, func() {}
	}
	c, err := s.fastdb.Borrow(ctx)
	if err != nil {
		return nil, func() {}
	}
	return c, func() { s.fastdb.Return(c) }
}

// blobOf maps a fastdb blob record to the storage type the page assembly
// uses. The bytes and NULL handling are identical to storage.Store's own
// decode (asserted by the fastdb differential tests); this is a struct
// assignment, not a re-decode.
func blobOf(b fastdb.Blob) storage.Blob {
	return storage.Blob{ID: b.ID, Key: b.Key, Filename: b.Filename, ContentType: b.ContentType, Metadata: b.Metadata, ServiceName: b.ServiceName, ByteSize: b.ByteSize, Checksum: b.Checksum, CreatedAt: b.CreatedAt}
}

// roomRow returns the room for user,id: through fastdb when c is set (a
// missing or inaccessible room is ErrNoRows, the same sentinel the
// database/sql reader returns), else through s.DB.
func (s *Server) roomRow(c *fastdb.Conn, ctx context.Context, user, id int64) (database.Room, error) {
	if c == nil {
		return s.DB.Room(ctx, user, id)
	}
	var r fastdb.Room
	if err := c.Room(&r, user, id); err != nil {
		return database.Room{}, err
	}
	return roomOf(r), nil
}

// messageRefs returns the room's message window in the same shape
// database.DB.MessagePageReferences yields (refs for before/plus-anchor-zero,
// full records for after/around, chronologically ordered), through fastdb
// when c is set, and the precomputed messages-page validator derived from it.
//
// The result is cached keyed by (room, room version, anchor, direction):
// version is the room's updated_at, read from the room row both callers
// already fetched for membership, so a hit costs one in-memory lookup and no
// page scan — the version comparison is the key itself. A fill scans once,
// stores only the references (id, room, update stamp), and returns that scan
// for the current request; full message rows hydrate later on fragment cache
// misses exactly where they always have. The returned references slice is the
// cached window's on a hit and must not be mutated.
func (s *Server) messageRefs(c *fastdb.Conn, ctx context.Context, room, anchor int64, direction string, version time.Time) ([]database.Message, messageValidator, error) {
	key := messageRefsKey{room: room, version: version.UnixMicro(), anchor: anchor, direction: direction}
	if entry, ok := s.refsCache.lookup(key); ok {
		s.messageRefsHits.Add(1)
		return entry.refs, messageValidator{etag: entry.etag, etagFrame: entry.etagFrame, modified: entry.modified}, nil
	}
	s.messageRefsMisses.Add(1)
	var messages []database.Message
	var err error
	if c == nil {
		messages, err = s.DB.MessagePageReferences(ctx, room, anchor, direction)
	} else {
		var refs []fastdb.Message
		if refs, err = c.MessagePageReferences(nil, room, anchor, direction); err == nil {
			messages = make([]database.Message, 0, len(refs))
			for _, m := range refs {
				messages = append(messages, messageOf(m))
			}
		}
	}
	if err != nil {
		return nil, messageValidator{}, err
	}
	validator := messageValidatorOf(messages)
	s.refsCache.store(key, messages, validator)
	return messages, validator, nil
}

// sessionUser returns the active user for a session token, through fastdb
// when c is set. ErrNoRows means the token is unknown or the user inactive,
// exactly like database.DB.SessionUser.
func (s *Server) sessionUser(c *fastdb.Conn, ctx context.Context, token string) (database.User, error) {
	if c == nil {
		return s.DB.SessionUser(ctx, token)
	}
	var u fastdb.User
	if err := c.SessionUser(&u, token); err != nil {
		return database.User{}, err
	}
	return userOf(u), nil
}

// sessionState returns the active user for a session token together with the
// session's last_active_at, from one joined fastdb read when the auth fast
// path is enabled and the pool can serve the borrow. Otherwise it falls back
// to the legacy two-step shape — database.DB.SessionUser plus a zero
// lastActive, which the auth middleware treats as "always refresh", so the
// legacy RefreshSession-on-every-request behaviour is preserved exactly.
// ErrNoRows means the token is unknown or the user inactive, exactly like
// database.DB.SessionUser.
func (s *Server) sessionState(c *fastdb.Conn, ctx context.Context, token string) (database.User, time.Time, error) {
	if s.authFast && c != nil {
		var u fastdb.User
		lastActive, err := c.SessionActive(&u, token)
		return userOf(u), lastActive, err
	}
	u, err := s.DB.SessionUser(ctx, token)
	return u, time.Time{}, err
}

// invitation mirrors the raw room-page probe in Server.room: true when the
// room is the account's first room and holds no more than 40 messages.
func (s *Server) invitation(c *fastdb.Conn, ctx context.Context, room int64) (bool, error) {
	if c != nil {
		return c.Invitation(room)
	}
	var invitation bool
	err := s.DB.Read.QueryRowContext(ctx, "SELECT ?=(SELECT id FROM rooms ORDER BY created_at LIMIT 1) AND NOT EXISTS(SELECT 1 FROM messages WHERE room_id=? LIMIT 1 OFFSET 40)", room, room).Scan(&invitation)
	return invitation, err
}

// directPlaceholders returns the sidebar's start-a-ping candidates, through
// fastdb when c is set.
func (s *Server) directPlaceholders(c *fastdb.Conn, ctx context.Context, user int64) ([]database.User, error) {
	if c == nil {
		return s.DB.DirectPlaceholders(ctx, user)
	}
	users, err := c.DirectPlaceholders(nil, user)
	if err != nil {
		return nil, err
	}
	return usersOf(users), nil
}

// searchResults runs the FTS query in the same shape database.DB.Search
// yields (membership-scoped, chronological, up to 100), through fastdb when c
// is set. The word fold and quoted-token MATCH expression are held byte-equal
// to the database reader by the differential tests.
func (s *Server) searchResults(c *fastdb.Conn, ctx context.Context, user int64, query string) ([]database.Message, error) {
	if c == nil {
		return s.DB.Search(ctx, user, query)
	}
	refs, err := c.Search(nil, user, query)
	if err != nil {
		return nil, err
	}
	out := make([]database.Message, 0, len(refs))
	for _, m := range refs {
		out = append(out, messageOf(m))
	}
	return out, nil
}

// roomData runs the room page's database reads on one borrowed fastdb
// connection (or the database/sql readers when the fast path is off) and
// returns the page inputs. The connection is returned before any rendering,
// so the pool's critical section covers only the reads; the render of a
// large room page must not occupy a pool slot.
//
// A room lookup failure is the only error the room handler redirects on;
// MessagePageReferences, displayRoom and the invitation probe all report
// through s.fail. The retry around an anchor that no longer exists drops the
// ErrNoRows from the first attempt exactly like the previous inline flow.
func (s *Server) roomData(r *http.Request, u database.User) (room database.Room, messages []database.Message, view sidebarRoom, invitation bool, err error) {
	c, release := s.fastConn(r)
	defer release()
	room, err = s.roomRowCached(c, r.Context(), u.ID, roomID(r))
	if err != nil {
		return
	}
	anchor, _ := strconv.ParseInt(strings.TrimPrefix(r.PathValue("anchor"), "@"), 10, 64)
	messages, _, err = s.messageRefs(c, r.Context(), room.ID, anchor, "around", room.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		messages, _, err = s.messageRefs(c, r.Context(), room.ID, 0, "around", room.UpdatedAt)
	}
	if err != nil {
		return
	}
	view, err = s.displayRoom(c, r.Context(), room, u)
	if err != nil {
		return
	}
	invitation, err = s.invitationCached(c, r.Context(), room.ID)
	return
}

// messageData runs the messages page's database reads (the room lookup for
// membership, then the message window) on one borrowed fastdb connection (or
// database/sql when the fast path is off) and returns the page inputs with
// the connection already returned, together with the precomputed
// conditional-GET validator for the window.
func (s *Server) messageData(r *http.Request, u database.User) (messages []database.Message, validator messageValidator, err error) {
	c, release := s.fastConn(r)
	defer release()
	var room database.Room
	if room, err = s.roomRowCached(c, r.Context(), u.ID, roomID(r)); err != nil {
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	direction := "before"
	if before == 0 {
		before, _ = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		direction = "after"
	}
	messages, validator, err = s.messageRefs(c, r.Context(), room.ID, before, direction, room.UpdatedAt)
	return
}

// sidebarData runs the sidebar's database reads (SidebarRooms plus per-direct
// RoomMembers, then DirectPlaceholders) on one borrowed fastdb connection (or
// database/sql when the fast path is off) and returns the page inputs with
// the connection already returned.
func (s *Server) sidebarData(r *http.Request, u database.User) ([]sidebarRoom, []database.User, error) {
	c, release := s.fastConn(r)
	defer release()
	items, err := s.sidebarRooms(c, r.Context(), u)
	if err != nil {
		return nil, nil, err
	}
	placeholders, err := s.directPlaceholders(c, r.Context(), u.ID)
	if err != nil {
		return nil, nil, err
	}
	return items, placeholders, nil
}

// sidebarRoomsJoined runs the sidebar room read as one fastdb JOIN (rooms and
// direct-room members in a single statement) and assembles the sidebar view,
// replacing the per-room RoomMembers round trips of the legacy reader. The
// row stream arrives grouped: rooms in SidebarRooms order, each room
// contiguous, one row per other direct-room member and one member-less row
// for every other room.
func (s *Server) sidebarRoomsJoined(c *fastdb.Conn, user database.User) ([]sidebarRoom, error) {
	rows, err := c.SidebarMembers(nil, user.ID)
	if err != nil {
		return nil, err
	}
	result := make([]sidebarRoom, 0, len(rows))
	for i := 0; i < len(rows); {
		row := rows[i]
		room := sidebarRoom{Room: roomOf(row.Room.Room), Involvement: row.Room.Involvement, Unread: row.Room.Unread}
		end := i + 1
		for end < len(rows) && rows[end].Room.ID == row.Room.ID {
			end++
		}
		if room.Type == "Rooms::Direct" {
			members := make([]database.User, 0, end-i)
			for j := i; j < end; j++ {
				if rows[j].Member.ID != 0 {
					members = append(members, userOf(rows[j].Member))
				}
			}
			room.applyMembers(members, user)
		}
		result = append(result, room)
		i = end
	}
	return result, nil
}

// openFastPool opens the fast read pool for the server, honouring
// CAMPFIRE_FASTDB (default on; off restores the database/sql readers). The
// pool holds one Conn per application CPU; CAMPFIRE_FASTDB_POOL_SIZE
// overrides the size (tests pin single-Conn serialization with it). A pool
// that cannot open (unreadable database, non-WAL file) downgrades to the
// legacy readers with a warning instead of failing startup.
func openFastPool(dbPath string) *fastdb.Pool {
	enabled := true
	if raw, ok := os.LookupEnv("CAMPFIRE_FASTDB"); ok {
		var valid bool
		enabled, valid = parseFastDB(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_FASTDB; keeping fast reads on", "value", raw)
		}
	}
	if !enabled {
		slog.Info("fast reads", "enabled", false)
		return nil
	}
	size := max(1, runtime.GOMAXPROCS(0))
	if raw, ok := os.LookupEnv("CAMPFIRE_FASTDB_POOL_SIZE"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			slog.Warn("invalid CAMPFIRE_FASTDB_POOL_SIZE; using GOMAXPROCS", "value", raw)
		} else {
			size = n
		}
	}
	pool, err := fastdb.OpenPool(dbPath, size)
	if err != nil {
		slog.Warn("fast read pool unavailable; falling back to database/sql reads", "path", dbPath, "error", err)
		return nil
	}
	slog.Info("fast reads", "enabled", true, "path", dbPath, "pool_size", size)
	return pool
}
