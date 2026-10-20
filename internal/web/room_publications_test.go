package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestInvolvementPublishesAfterRequestCancellation(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	room, err := app.DB.CreateRoom(ctx, owner.ID, "Rooms::Open", &sql.NullString{String: "Visibility", Valid: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/cable", &websocket.DialOptions{
		Subprotocols: []string{"actioncable-v1-json"}, HTTPHeader: http.Header{"Cookie": {cookie.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var frame map[string]any
	if err = wsjson.Read(ctx, conn, &frame); err != nil || frame["type"] != "welcome" {
		t.Fatal("welcome", frame, err)
	}
	identifier := fmt.Sprintf(`{"channel":"Turbo::StreamsChannel","signed_stream_name":%q}`, app.Secrets.SignStream(rails.UserRoomsStream(owner.ID)))
	if err = wsjson.Write(ctx, conn, map[string]string{"command": "subscribe", "identifier": identifier}); err != nil {
		t.Fatal(err)
	}
	if err = wsjson.Read(ctx, conn, &frame); err != nil || frame["type"] != "confirm_subscription" {
		t.Fatal("subscription", frame, err)
	}
	commit, err := app.RoomCommands.Involvement(ctx, owner.ID, room.ID, "invisible")
	if err != nil {
		t.Fatal(err)
	}
	request, requestCancel := context.WithCancel(ctx)
	requestCancel()
	if err = app.RoomPublications.Involvement(request, owner.ID, commit); err != nil {
		t.Fatal(err)
	}
	want := rails.TurboStream("remove", room.DOM("list"), "")
	if err = wsjson.Read(ctx, conn, &frame); err != nil || frame["identifier"] != identifier || frame["message"] != want {
		t.Fatalf("committed visibility effect lost to request cancellation: %v %v", frame, err)
	}
}
