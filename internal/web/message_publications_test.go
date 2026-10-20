package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestBoostHTTPPublishesAndRemovesCommittedMarkup(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	message, err := app.DB.CreateMessage(ctx, owner.ID, room.ID, messageInput("publication", "message"))
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
	identifier := fmt.Sprintf(`{"channel":"RoomMessagesChannel","signed_stream_name":%q}`, app.Secrets.SignStream(rails.RoomStream(room.Type, room.ID)))
	if err = wsjson.Write(ctx, conn, map[string]string{"command": "subscribe", "identifier": identifier}); err != nil {
		t.Fatal(err)
	}
	if err = wsjson.Read(ctx, conn, &frame); err != nil || frame["type"] != "confirm_subscription" {
		t.Fatal("subscription", frame, err)
	}
	readOutput := func(t *testing.T) string {
		t.Helper()
		frame = nil
		if err := wsjson.Read(ctx, conn, &frame); err != nil || frame["identifier"] != identifier {
			t.Fatal("publication", frame, err)
		}
		output, ok := frame["message"].(string)
		if !ok {
			t.Fatal("non-markup publication", frame)
		}
		return output
	}
	for _, endpoint := range []struct {
		name, path, contentType, body string
		status                        int
	}{
		{"browser", fmt.Sprintf("/messages/%d/boosts", message.ID), "application/x-www-form-urlencoded", "boost%5Bcontent%5D=%3Cyes%3E", 302},
		// Bot endpoints accept a session cookie too, but retain their raw-body API.
		{"bot", fmt.Sprintf("/rooms/%d/key/messages/%d/boosts", room.ID, message.ID), "text/plain", "<yes>", 201},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			response, data := perform(t, server, "POST", endpoint.path, endpoint.contentType, strings.NewReader(endpoint.body), cookie)
			if response.StatusCode != endpoint.status {
				t.Fatalf("create: %s %s", response.Status, data)
			}
			boosts, err := app.DB.Boosts(ctx, message.ID)
			if err != nil || len(boosts) != 1 || boosts[0].Content != "<yes>" {
				t.Fatal("persisted boost", boosts, err)
			}
			id := boosts[0].ID
			if endpoint.name == "browser" {
				if response.Header.Get("Location") != fmt.Sprintf("/messages/%d/boosts", message.ID) {
					t.Fatal("redirect", response.Header)
				}
			} else {
				var value struct {
					ID      int64  `json:"id"`
					Content string `json:"content"`
					Message struct {
						ID  int64  `json:"id"`
						URL string `json:"url"`
					} `json:"message"`
				}
				if err := json.Unmarshal(data, &value); err != nil || value.ID != id || value.Content != "<yes>" || value.Message.ID != message.ID || value.Message.URL != fmt.Sprintf("%s/rooms/%d/messages/%d", server.URL, room.ID, message.ID) {
					t.Fatalf("bot receipt: %s %v", data, err)
				}
			}
			output := readOutput(t)
			if !strings.Contains(output, `action="append" target="boosts_message_publication"`) || !strings.Contains(output, fmt.Sprintf(`id="boost_%d"`, id)) || !strings.Contains(output, "&lt;yes&gt;") || strings.Contains(output, "<yes>") {
				t.Fatal("append markup", output)
			}
			response, data = perform(t, server, "DELETE", fmt.Sprintf("%s/%d", endpoint.path, id), "", nil, cookie)
			if response.StatusCode != 204 {
				t.Fatalf("delete: %s %s", response.Status, data)
			}
			if output := readOutput(t); output != rails.TurboStream("remove", fmt.Sprintf("boost_%d", id), "") {
				t.Fatal("remove markup", output)
			}
		})
	}
	result, err := app.MessageCommands.Delete(ctx, owner.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup failure and request cancellation must not suppress removal. The
	// caller still receives the postcommit failure rather than a fake success.
	processing := errors.New("cleanup failed")
	result.Processing = processing
	request, requestCancel := context.WithCancel(ctx)
	requestCancel()
	output, err := app.MessagePublications.Removed(request, result)
	want := rails.TurboStream("remove", "message_publication", "")
	if output != want || !errors.Is(err, processing) || readOutput(t) != want {
		t.Fatalf("committed removal: %q %v", output, err)
	}
}
