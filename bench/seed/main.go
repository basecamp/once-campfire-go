// Command seed builds the benchmark seed: the reference's test fixtures plus the parts of the
// reference's parity "default" seed (reference/parity/seeds/default.rb) that bench/application
// measures, written as plain rows into the shared Rails schema, so that both applications read
// identical data. It stands in for `parity/bin/seed build default`, which needs the Rails image.
//
// What it keeps from default.rb: fixture rows with their CRC32 ids and spread timestamps, the
// extra people (with Jason's and Deploy Bot's avatars), rooms and memberships, the watercooler's
// 120 busy messages and the bot's, the designers' and direct rooms' text posts, involvements,
// unread flags, recent searches and the labels bench/application reads. What it leaves out: the
// designers' attachments, sounds, mentions and embeds, the unrenderable message and transfers,
// which no measured route shows.
//
//	go run ./bench/seed --out ../once-campfire-rust/parity/.seed/default \
//	  --fixtures ../once-campfire-rust/reference/test/fixtures
package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"hash/crc32"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// The seed clock (Parity::Seed::NOW).
var now = time.Date(2026, 3, 2, 16, 0, 0, 0, time.UTC)

// "secret123456" at bcrypt cost 12 (Parity::Seed::PASSWORD_DIGEST).
const passwordDigest = "$2a$12$Gz1V3e1PgXe5HFJ5lLlH8eP5BlvdNLdGGdQyacJSn7rvquw4Lt0ue"

func main() {
	out := flag.String("out", "", "seed directory to write ({db,storage}/, labels.json)")
	fixtures := flag.String("fixtures", "", "reference/test/fixtures")
	envFile := flag.String("env", "", "parity/.env.reference (SECRET_KEY_BASE for the avatar tokens)")
	flag.Parse()
	if *out == "" || *fixtures == "" || *envFile == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := build(*out, *fixtures, *envFile); err != nil {
		log.Fatal(err)
	}
}

type seed struct {
	tx       *database.Tx
	fixtures string
	labels   map[string]any
	storage  string
	clientID int
	blobKeys int
}

// identify is ActiveRecord::FixtureSet.identify: a label's CRC32 below 2**30 - 1.
func identify(label string) int64 {
	return int64(crc32.ChecksumIEEE([]byte(label)) % (1<<30 - 1))
}

func stamp(t time.Time) string { return database.Stamp(t) }

func build(out, fixtures, envFile string) error {
	secret, err := envValue(envFile, "SECRET_KEY_BASE")
	if err != nil {
		return err
	}
	secrets, err := rails.NewSecrets(secret)
	if err != nil {
		return err
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s exists; remove it first", out)
	}
	for _, dir := range []string{"db", "storage"} {
		if err := os.MkdirAll(filepath.Join(out, dir), 0o755); err != nil {
			return err
		}
	}
	db, err := database.Open(filepath.Join(out, "db", "production.sqlite3"), 1)
	if err != nil {
		return err
	}
	s := &seed{fixtures: fixtures, labels: map[string]any{"clock.now": now.Format(time.RFC3339)}, storage: filepath.Join(out, "storage")}
	err = db.Transaction(context.Background(), func(tx *database.Tx) error {
		s.tx = tx
		return s.run()
	})
	if err != nil {
		db.Close()
		return err
	}
	for _, name := range []string{"david", "jason"} {
		s.labels["avatar_tokens."+name] = secrets.SignedID("User", s.id("users", name), "avatar", time.Time{})
	}
	if _, err = db.Write.Exec("UPDATE ar_internal_metadata SET created_at=?, updated_at=?", stamp(now), stamp(now)); err != nil {
		return err
	}
	if _, err = db.Write.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	keys := make([]string, 0, len(s.labels))
	for key := range s.labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{\n")
	for i, key := range keys {
		value, _ := json.Marshal(s.labels[key])
		k, _ := json.Marshal(key)
		fmt.Fprintf(&b, "  %s: %s", k, value)
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return os.WriteFile(filepath.Join(out, "labels.json"), []byte(b.String()), 0o644)
}

func envValue(path, key string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s: no %s", path, key)
}

func (s *seed) exec(query string, args ...any) int64 {
	result, err := s.tx.Exec(query, args...)
	if err != nil {
		panic(fmt.Errorf("%s: %w", query, err))
	}
	id, _ := result.LastInsertId()
	return id
}

func (s *seed) label(table, name string, value any) {
	key := table + "." + name
	if _, taken := s.labels[key]; taken {
		panic("label " + key + " already taken")
	}
	s.labels[key] = value
}

func (s *seed) id(table, name string) int64 {
	value, ok := s.labels[table+"."+name]
	if !ok {
		panic("no label " + table + "." + name)
	}
	return value.(int64)
}

// fixture is one labelled entry of a fixture file, in file order.
type fixture struct {
	label  string
	fields map[string]string
}

var agoPattern = regexp.MustCompile(`^<%= (\d+)\.(minutes?|hours?|days?)\.ago %>$`)

// readFixtures parses the flat YAML the reference's fixtures use: `label:` lines and indented
// `key: value` lines, evaluating the `N.unit.ago` ERB at the seed clock.
func (s *seed) readFixtures(name string) []fixture {
	file, err := os.Open(filepath.Join(s.fixtures, name+".yml"))
	if err != nil {
		panic(err)
	}
	defer file.Close()
	var list []fixture
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "<%") || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			list = append(list, fixture{label: strings.TrimSuffix(line, ":"), fields: map[string]string{}})
			continue
		}
		key, value, _ := strings.Cut(strings.TrimSpace(line), ": ")
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		if m := agoPattern.FindStringSubmatch(value); m != nil {
			n, _ := strconv.Atoi(m[1])
			unit := map[string]time.Duration{"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour}[strings.TrimSuffix(m[2], "s")]
			value = stamp(now.Add(-time.Duration(n) * unit))
		}
		list[len(list)-1].fields[key] = strings.TrimPrefix(value, ":")
	}
	return list
}

// uuidV5 is Digest::UUID.uuid_v5(Digest::UUID::URL_NAMESPACE, name).
func uuidV5(name string) string {
	namespace := []byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	sum := sha1.Sum(append(namespace, name...))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	h := fmt.Sprintf("%x", sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (s *seed) nextClientMessageID() string {
	s.clientID++
	return uuidV5(fmt.Sprintf("https://parity.campfire.test/messages/%d", s.clientID))
}

var tags = regexp.MustCompile(`<[^>]*>`)

// post is room.messages.create!: the message, its body (touching the message), the room touch
// and the search index.
func (s *seed) post(room, creator int64, at time.Time, body, as string) int64 {
	id := s.message(room, creator, s.nextClientMessageID(), at, body)
	if as != "" {
		s.label("messages", as, id)
	}
	return id
}

func (s *seed) message(room, creator int64, clientID string, at time.Time, body string, fixedID ...int64) int64 {
	var id int64
	if len(fixedID) > 0 {
		id = fixedID[0]
		s.exec("INSERT INTO messages(id,client_message_id,created_at,creator_id,room_id,updated_at) VALUES (?,?,?,?,?,?)", id, clientID, stamp(at), creator, room, stamp(at))
	} else {
		id = s.exec("INSERT INTO messages(client_message_id,created_at,creator_id,room_id,updated_at) VALUES (?,?,?,?,?)", clientID, stamp(at), creator, room, stamp(at))
	}
	s.exec("INSERT INTO action_text_rich_texts(body,created_at,name,record_id,record_type,updated_at) VALUES (?,?,'body',?,'Message',?)", body, stamp(at), id, stamp(at))
	s.exec("INSERT INTO message_search_index(rowid,body) VALUES (?,?)", id, strings.TrimSpace(tags.ReplaceAllString(body, "")))
	s.exec("UPDATE rooms SET updated_at=? WHERE id=? AND updated_at<?", stamp(at), room, stamp(at))
	return id
}

func (s *seed) boost(message, booster int64, content string, at time.Time) int64 {
	return s.exec("INSERT INTO boosts(booster_id,content,created_at,message_id,updated_at) VALUES (?,?,?,?,?)", booster, content, stamp(at), message, stamp(at))
}

func (s *seed) user(name, email, bio string, role int, botToken string, at time.Time) int64 {
	var address, digest, token, about any
	if email != "" {
		address, digest = email, passwordDigest
	}
	if botToken != "" {
		token = botToken
	}
	if bio != "" {
		about = bio
	}
	return s.exec("INSERT INTO users(bio,bot_token,created_at,email_address,name,password_digest,role,status,updated_at) VALUES (?,?,?,?,?,?,?,0,?)", about, token, stamp(at), address, name, digest, role, stamp(at))
}

func (s *seed) room(name, kind string, creator int64, at time.Time, members []int64) int64 {
	var label any
	if name != "" {
		label = name
	}
	room := s.exec("INSERT INTO rooms(created_at,creator_id,name,type,updated_at) VALUES (?,?,?,?,?)", stamp(at), creator, label, kind, stamp(at))
	s.grant(room, members, at)
	return room
}

// grant is room.memberships.grant_to: each member at the room type's default involvement.
func (s *seed) grant(room int64, members []int64, at time.Time) {
	var kind string
	if err := s.tx.QueryRow("SELECT type FROM rooms WHERE id=?", room).Scan(&kind); err != nil {
		panic(err)
	}
	involvement := "mentions"
	if kind == "Rooms::Direct" {
		involvement = "everything"
	}
	for _, user := range members {
		s.exec("INSERT INTO memberships(created_at,involvement,room_id,updated_at,user_id) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING", stamp(at), involvement, room, stamp(at), user)
	}
}

// blobKey is the seed's deterministic ActiveStorage::Blob key: SHA-256("parity-blob-N") in base 36.
func (s *seed) blobKey() string {
	s.blobKeys++
	sum := sha256.Sum256([]byte(fmt.Sprintf("parity-blob-%d", s.blobKeys)))
	return new(big.Int).SetBytes(sum[:]).Text(36)[:28]
}

func (s *seed) attachAvatar(user int64, at time.Time) {
	source := filepath.Join(s.fixtures, "files", "moon.jpg")
	data, err := os.ReadFile(source)
	if err != nil {
		panic(err)
	}
	key := s.blobKey()
	path := filepath.Join(s.storage, key[:2], key[2:4], key)
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err = os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}
	sum := md5.Sum(data)
	metadata := `{"identified":true,"analyzed":true}`
	blob := s.exec("INSERT INTO active_storage_blobs(byte_size,checksum,content_type,created_at,filename,key,metadata,service_name) VALUES (?,?,?,?,?,?,?,?)",
		len(data), base64.StdEncoding.EncodeToString(sum[:]), "image/jpeg", stamp(at), "moon.jpg", key, metadata, "local")
	s.exec("INSERT INTO active_storage_attachments(blob_id,created_at,name,record_id,record_type) VALUES (?,?,'avatar',?,'User')", blob, stamp(at), user)
	s.exec("UPDATE users SET updated_at=? WHERE id=?", stamp(at), user)
}

func (s *seed) run() error {
	s.loadFixtures()
	s.scenario()
	return nil
}

func (s *seed) loadFixtures() {
	users := map[string]int{"member": 0, "administrator": 1, "bot": 2}
	for i, f := range s.readFixtures("accounts") {
		at := now.Add(-60*24*time.Hour + time.Duration(i)*time.Minute)
		s.exec("INSERT INTO accounts(id,created_at,join_code,name,updated_at) VALUES (?,?,?,?,?)", identify(f.label), stamp(at), f.fields["join_code"], f.fields["name"], stamp(at))
		s.label("accounts", f.label, identify(f.label))
	}
	for i, f := range s.readFixtures("users") {
		at := now.Add(-59*24*time.Hour + time.Duration(i)*time.Hour)
		id := identify(f.label)
		var email, digest, bio, token any
		if v := f.fields["email_address"]; v != "" {
			email, digest = v, passwordDigest
		}
		if v := f.fields["bio"]; v != "" {
			bio = v
		}
		if f.label == "bender" {
			token = "BenderBot123" // the seed pins the fixture's random token
		}
		s.exec("INSERT INTO users(id,bio,bot_token,created_at,email_address,name,password_digest,role,status,updated_at) VALUES (?,?,?,?,?,?,?,?,0,?)",
			id, bio, token, stamp(at), email, f.fields["name"], digest, users[f.fields["role"]], stamp(at))
		s.label("users", f.label, id)
	}
	for i, f := range s.readFixtures("rooms") {
		at := now.Add(-58*24*time.Hour + time.Duration(i)*time.Hour)
		var name any
		if v := f.fields["name"]; v != "" {
			name = v
		}
		s.exec("INSERT INTO rooms(id,created_at,creator_id,name,type,updated_at) VALUES (?,?,?,?,?,?)", identify(f.label), stamp(at), identify(f.fields["creator"]), name, f.fields["type"], stamp(at))
		s.label("rooms", f.label, identify(f.label))
	}
	for i, f := range s.readFixtures("memberships") {
		at := now.Add(-57*24*time.Hour + time.Duration(i)*time.Minute)
		involvement := f.fields["involvement"]
		if involvement == "" {
			involvement = "mentions"
		}
		s.exec("INSERT INTO memberships(id,created_at,involvement,room_id,updated_at,user_id) VALUES (?,?,?,?,?,?)", identify(f.label), stamp(at), involvement, identify(f.fields["room"]), stamp(at), identify(f.fields["user"]))
	}
	for i, f := range s.readFixtures("webhooks") {
		at := now.Add(-56*24*time.Hour + time.Duration(i)*time.Minute)
		s.exec("INSERT INTO webhooks(id,created_at,updated_at,url,user_id) VALUES (?,?,?,?,?)", identify(f.label), stamp(at), stamp(at), f.fields["url"], identify(f.fields["user"]))
	}
	for i, f := range s.readFixtures("push/subscriptions") {
		at := now.Add(-55*24*time.Hour + time.Duration(i)*time.Minute)
		s.exec("INSERT INTO push_subscriptions(id,auth_key,created_at,endpoint,p256dh_key,updated_at,user_agent,user_id) VALUES (?,?,?,?,?,?,?,?)",
			identify(f.label), f.fields["auth_key"], stamp(at), f.fields["endpoint"], f.fields["p256dh_key"], stamp(at), f.fields["user_agent"], identify(f.fields["user"]))
	}
	for i, f := range s.readFixtures("sessions") {
		at := now.Add(-54*24*time.Hour + time.Duration(i)*time.Minute)
		s.exec("INSERT INTO sessions(id,created_at,last_active_at,token,updated_at,user_agent,user_id) VALUES (?,?,?,?,?,?,?)",
			identify(f.label), stamp(at), f.fields["last_active_at"], f.fields["token"], stamp(at), f.fields["user_agent"], identify(f.fields["user"]))
	}
	for i, f := range s.readFixtures("searches") {
		at := now.Add(-3*24*time.Hour + time.Duration(i)*time.Minute)
		s.exec("INSERT INTO searches(id,created_at,query,updated_at,user_id) VALUES (?,?,?,?,?)", identify(f.label), stamp(at), f.fields["query"], stamp(at), identify(f.fields["user"]))
	}
	bodies := map[string]string{}
	for _, f := range s.readFixtures("action_text/rich_texts") {
		bodies[strings.TrimSuffix(f.fields["record"], " (Message)")] = f.fields["body"]
	}
	created := map[string]time.Time{}
	for _, f := range s.readFixtures("messages") {
		at, err := time.Parse("2006-01-02 15:04:05.999999", f.fields["created_at"])
		if err != nil {
			panic(err)
		}
		created[f.label] = at
		s.message(identify(f.fields["room"]), identify(f.fields["creator"]), f.fields["client_message_id"], at, bodies[f.label], identify(f.label))
		s.label("messages", f.label, identify(f.label))
	}
	for i, f := range s.readFixtures("boosts") {
		at := created[f.fields["message"]].Add(time.Minute + time.Duration(i)*time.Second)
		id := s.exec("INSERT INTO boosts(id,booster_id,content,created_at,message_id,updated_at) VALUES (?,?,?,?,?,?)",
			identify(f.label), identify(f.fields["booster"]), f.fields["content"], stamp(at), identify(f.fields["message"]), stamp(at))
		s.label("boosts", f.label, id)
	}
}

func (s *seed) scenario() {
	day := 24 * time.Hour
	david, jason, jz, kevin, bender := s.id("users", "david"), s.id("users", "jason"), s.id("users", "jz"), s.id("users", "kevin"), s.id("users", "bender")

	rita := s.user("Rita Lopez", "rita@37signals.com", "Support", 0, "", now.Add(-40*day))
	s.label("users", "rita", rita)
	mallory := s.user("Mallory Banned", "mallory@example.com", "", 0, "", now.Add(-39*day))
	s.label("users", "mallory", mallory)
	deployBot := s.user("Deploy Bot", "", "", 2, "DeployBot456", now.Add(-38*day))
	s.label("users", "deploy_bot", deployBot)
	oldBot := s.user("Old Bot", "", "", 2, "OldBot789abc", now.Add(-37*day))
	s.label("users", "old_bot", oldBot)
	s.exec("INSERT INTO webhooks(created_at,updated_at,url,user_id) VALUES (?,?,?,?)", stamp(now.Add(-37*day)), stamp(now.Add(-37*day)), "https://example.com/old-bot", oldBot)
	// User#grant_membership_to_open_rooms
	for _, user := range []int64{rita, mallory, deployBot, oldBot} {
		var at string
		s.tx.QueryRow("SELECT created_at FROM users WHERE id=?", user).Scan(&at)
		s.exec("INSERT INTO memberships(created_at,involvement,room_id,updated_at,user_id) SELECT ?,'everything',id,?,? FROM rooms WHERE type='Rooms::Open'", at, at, user)
	}

	s.attachAvatar(jason, now.Add(-36*day))
	s.attachAvatar(deployBot, now.Add(-36*day+time.Minute))

	active := func() []int64 {
		rows, err := s.tx.QueryContext(context.Background(), "SELECT id FROM users WHERE status=0 ORDER BY id")
		if err != nil {
			panic(err)
		}
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		return ids
	}
	s.label("rooms", "quiet", s.room("Quiet Corner", "Rooms::Closed", kevin, now.Add(-35*day), []int64{kevin, david}))
	s.label("rooms", "archive", s.room("Archive", "Rooms::Open", david, now.Add(-34*day), active()))
	s.label("rooms", "broken", s.room("Broken", "Rooms::Closed", david, now.Add(-33*day), []int64{david}))
	group := s.room("", "Rooms::Direct", david, now.Add(-32*day), []int64{david, jason, jz, kevin})
	s.label("rooms", "group_direct", group)

	designers, watercooler := s.id("rooms", "designers"), s.id("rooms", "watercooler")
	s.grant(designers, []int64{rita, deployBot}, now.Add(-31*day))
	s.grant(designers, []int64{mallory}, now.Add(-31*day+time.Minute))

	// Designers: the text posts of default.rb's presentation tour.
	morning := now.Truncate(day).Add(-day + 9*time.Hour)
	s.post(designers, jason, morning, "<p>Morning! Anyone around?</p>", "plain")
	s.post(designers, jason, morning.Add(2*time.Minute), "<p>Coffee first, then the launch plan.</p>", "threaded")

	// Watercooler: the busy room.
	lines := []string{"Did anyone see the game last night?", "Coffee machine is fixed!", "I'm heading out for lunch.",
		"New plants in the kitchen.", "Who took my stapler?", "Friday demo is at 3pm.",
		"The wifi is flaky again.", "Congrats on the launch!", "Anyone up for a walk?", "Back in 5."}
	busyStart := now.Truncate(day).Add(-2*day + 8*time.Hour)
	for i := 1; i <= 120; i++ {
		author := []int64{david, jason, david, jason, bender}[i%5]
		s.post(watercooler, author, busyStart.Add(time.Duration(i*7)*time.Minute), fmt.Sprintf("<p>%03d. %s</p>", i, lines[i%len(lines)]), fmt.Sprintf("busy_%03d", i))
	}
	botMessage := s.post(watercooler, bender, now.Add(-20*time.Minute), "<p>Build 1043 passed.</p>", "bot_in_watercooler")

	davidAndJason, davidAndKevin, benderAndKevin := s.id("rooms", "david_and_jason"), s.id("rooms", "david_and_kevin"), s.id("rooms", "bender_and_kevin")
	s.post(davidAndJason, jason, now.Add(-5*time.Hour), "<p>Got a minute?</p>", "direct_first")
	s.post(davidAndJason, david, now.Add(-5*time.Hour+2*time.Minute), "<p>Sure, what's up?</p>", "")
	s.post(davidAndJason, jason, now.Add(-5*time.Hour+3*time.Minute), "<p>The pricing page. Let's talk after lunch.</p>", "")
	s.post(benderAndKevin, bender, now.Add(-4*time.Hour), "<p>Your nightly report is ready.</p>", "")
	groupFirst := s.post(group, jz, now.Add(-3*time.Hour), "<p>Group ping: who's in for Thursday?</p>", "group_direct_first")
	directUnread := s.post(davidAndKevin, kevin, now.Add(-2*time.Hour), "<p>Can you approve my PR?</p>", "direct_unread")

	// People states.
	s.exec("UPDATE users SET status=1,email_address=?,updated_at=? WHERE id=?", "rita-deactivated-00000000-0000-4000-8000-000000000000@37signals.com", stamp(now.Add(-30*day)), rita)
	s.exec("DELETE FROM memberships WHERE user_id=? AND room_id IN (SELECT id FROM rooms WHERE type!='Rooms::Direct')", rita)
	s.exec("UPDATE users SET status=1,email_address=NULL,updated_at=? WHERE id=?", stamp(now.Add(-29*day)), oldBot)
	s.exec("DELETE FROM memberships WHERE user_id=? AND room_id IN (SELECT id FROM rooms WHERE type!='Rooms::Direct')", oldBot)
	s.exec("UPDATE users SET status=2,updated_at=? WHERE id=?", stamp(now.Add(-28*day)), mallory)
	s.label("bans", "mallory", s.exec("INSERT INTO bans(created_at,ip_address,updated_at,user_id) VALUES (?,?,?,?)", stamp(now.Add(-28*day)), "203.0.113.9", stamp(now.Add(-28*day)), mallory))
	loner := s.user("Lonely Lou", "lou@37signals.com", "", 0, "", now.Add(-27*day))
	s.label("users", "loner", loner)

	// David's involvements cover every bell state.
	for room, involvement := range map[string]string{"designers": "mentions", "pets": "everything", "watercooler": "everything", "hq": "nothing", "archive": "invisible", "david_and_kevin": "nothing"} {
		s.exec("UPDATE memberships SET involvement=? WHERE room_id=? AND user_id=?", involvement, s.id("rooms", room), david)
	}
	s.exec("INSERT INTO searches(created_at,query,updated_at,user_id) VALUES (?,?,?,?)", stamp(now.Add(-2*day)), "Borgias", stamp(now.Add(-2*day)), david)
	s.exec("INSERT INTO searches(created_at,query,updated_at,user_id) VALUES (?,?,?,?)", stamp(now.Add(-day)), "cuckoo", stamp(now.Add(-day)), david)

	// Room#updated_at is its last message's creation; nobody is connected; these are unread.
	s.exec("UPDATE rooms SET updated_at=(SELECT max(created_at) FROM messages WHERE room_id=rooms.id) WHERE EXISTS (SELECT 1 FROM messages WHERE room_id=rooms.id AND created_at>rooms.updated_at)")
	s.exec("UPDATE memberships SET unread_at=NULL,connected_at=NULL,connections=0")
	unread := func(room, user, message int64) {
		s.exec("UPDATE memberships SET unread_at=(SELECT created_at FROM messages WHERE id=?) WHERE room_id=? AND user_id=?", message, room, user)
	}
	unread(watercooler, david, botMessage)
	unread(davidAndKevin, david, directUnread)
	unread(group, david, groupFirst)
	unread(designers, kevin, s.id("messages", "third"))

	s.label("bot_keys", "bender", fmt.Sprintf("%d-BenderBot123", bender))
	s.label("bot_keys", "deploy_bot", fmt.Sprintf("%d-DeployBot456", deployBot))
	s.label("join_codes", "signal", "CRMu-l8Ge-KB9B")
	s.label("passwords", "all", "secret123456")
	s.label("ips", "banned", "203.0.113.9")
	for key, value := range s.labels {
		name, ok := strings.CutPrefix(key, "users.")
		if !ok {
			continue
		}
		var email database.NullString
		s.tx.QueryRow("SELECT email_address FROM users WHERE id=?", value).Scan(&email)
		if email.Valid {
			s.labels["emails."+name] = email.String
		}
	}
}
