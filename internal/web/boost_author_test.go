package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type beforeRead struct {
	io.Reader
	before func()
}

func (r *beforeRead) Read(p []byte) (int, error) {
	if r.before != nil {
		before := r.before
		r.before = nil
		before()
	}
	return r.Reader.Read(p)
}

func TestBotBoostUsesWriterCurrentAuthor(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	rooms, err := app.DB.Rooms(t.Context(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(t.Context(), owner.ID, rooms[0].ID, messageInput("author", "message"))
	if err != nil {
		t.Fatal(err)
	}
	// Raw-body decoding follows authentication. Model a profile commit after
	// authentication captured the old display, but before the boost writer.
	body := &beforeRead{Reader: strings.NewReader("yes"), before: func() {
		if _, err := app.DB.Write.ExecContext(t.Context(), "UPDATE users SET name='Current',role=0,updated_at='2026-01-02 03:04:05' WHERE id=?", owner.ID); err != nil {
			t.Fatal(err)
		}
	}}
	r := httptest.NewRequest("POST", fmt.Sprintf("%s/rooms/%d/key/messages/%d/boosts", server.URL, message.RoomID, message.ID), body)
	r.Header.Set("Content-Type", "text/plain")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	var value struct {
		Booster struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Role   string `json:"role"`
			Avatar string `json:"avatar_url"`
		} `json:"booster"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil || w.Code != 201 || value.Booster.ID != owner.ID || value.Booster.Name != "Current" || value.Booster.Role != "member" || !strings.HasSuffix(value.Booster.Avatar, "/avatar?v=20260102030405") {
		t.Fatalf("boost JSON must describe the writer-current author: %d %s (%v)", w.Code, w.Body.String(), err)
	}
}
