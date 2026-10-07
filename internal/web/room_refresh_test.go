package web

import (
	"context"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRoomRefreshCursorUsesQueriedRoomVersion(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	roomID := rooms[0].ID
	initial, err := app.DB.CreateMessage(ctx, user.ID, roomID, "initial", "initial", "initial")
	if err != nil {
		t.Fatal(err)
	}
	// Preserve fractional milliseconds to check Rails' epoch-millisecond truncation.
	stamp := time.Now().Add(-time.Minute).Truncate(time.Second).Add(123456 * time.Microsecond)
	value := stamp.UTC().Format("2006-01-02 15:04:05.000000")
	if _, err := app.DB.Write.ExecContext(ctx, "UPDATE messages SET created_at=?,updated_at=? WHERE id=?", value, value, initial.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Write.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id=?", value, roomID); err != nil {
		t.Fatal(err)
	}
	room, err := app.DB.Room(ctx, user.ID, roomID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := app.DB.MessagePageReferences(ctx, roomID, 0, "around")
	if err != nil {
		t.Fatal(err)
	}
	// A message commits after the initial queries, but before rendering. The
	// page must retain its queried room version, not advance to the render clock.
	late, err := app.DB.CreateMessage(ctx, user.ID, roomID, "arrived-during-render", "late message", "late message")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", fmt.Sprintf("/rooms/%d", roomID), nil)
	session := &browserSession{server: app, request: request}
	request = request.WithContext(context.WithValue(request.Context(), browserSessionKey{}, session))
	render := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		app.render(response, request, "room", 200, page{User: user, Room: room, messageRecords: messages})
		return response
	}
	first, second := render(), render()
	cursor := regexp.MustCompile(`data-refresh-room-loaded-at-value="([0-9]+)"`).FindStringSubmatch(first.Body.String())
	if len(cursor) != 2 || cursor[1] != strconv.FormatInt(stamp.UnixMilli(), 10) {
		t.Fatalf("cursor %v, want queried room timestamp %d", cursor, stamp.UnixMilli())
	}
	if first.Body.String() != second.Body.String() || first.Header().Get("ETag") != second.Header().Get("ETag") {
		t.Fatal("unchanged room representation is unstable")
	}
	if strings.Contains(first.Body.String(), late.ClientID) {
		t.Fatal("initial page unexpectedly contains the late message")
	}
	response, body := perform(t, server, "GET", fmt.Sprintf("/rooms/%d/refresh?since=%s", roomID, cursor[1]), "", nil, cookie)
	if response.StatusCode != 200 || !strings.Contains(string(body), `action="append"`) || !strings.Contains(string(body), late.ClientID) {
		t.Fatalf("refresh missed the late message: %d %s", response.StatusCode, body)
	}
}
