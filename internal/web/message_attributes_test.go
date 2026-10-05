package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestMessageAttachmentUpdatePreservesBodyAndIndexes(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "", "preserved", "preserved")
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d/messages/%d", message.RoomID, message.ID)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, _ := form.CreateFormFile("message[attachment]", "report.txt")
	file.Write([]byte("a report"))
	form.Close()
	response, data := perform(t, server, "PATCH", path, form.FormDataContentType(), &body, cookie)
	if response.StatusCode != 302 {
		t.Fatalf("update: %s %s", response.Status, data)
	}
	updated, err := app.DB.Message(ctx, message.ID)
	if err != nil || updated.Body != "preserved" {
		t.Fatalf("body changed: %+v %v", updated, err)
	}
	blob, err := app.Storage.Attached(ctx, "Message", message.ID, "attachment")
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly empty body must fall back to the attachment filename in search.
	response, data = perform(t, server, "PATCH", path, "application/x-www-form-urlencoded", strings.NewReader(url.Values{"message[body]": {""}}.Encode()), cookie)
	if response.StatusCode != 302 {
		t.Fatalf("clear body: %s %s", response.Status, data)
	}
	hits, err := app.DB.Search(ctx, user.ID, "report")
	if err != nil || len(hits) != 1 {
		t.Fatalf("filename search: %+v %v", hits, err)
	}
	response, data = perform(t, server, "PATCH", path, "application/x-www-form-urlencoded", strings.NewReader(url.Values{"message[attachment]": {""}}.Encode()), cookie)
	if response.StatusCode != 302 {
		t.Fatalf("remove attachment: %s %s", response.Status, data)
	}
	if _, err = app.Storage.Attached(ctx, "Message", message.ID, "attachment"); !errors.Is(err, database.ErrNoRows) {
		t.Fatal(err)
	}
	hits, err = app.DB.Search(ctx, user.ID, "report")
	if err != nil || len(hits) != 0 {
		t.Fatalf("stale search: %+v %v", hits, err)
	}
	app.Close()
	if _, err = app.Storage.Blob(ctx, blob.ID); !errors.Is(err, database.ErrNoRows) {
		t.Fatalf("orphan blob: %v", err)
	}
}

func TestMessageUpdateRollbackAndMissingBodyRecord(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "", "original", "original")
	if err != nil {
		t.Fatal(err)
	}
	body, nonexistent := "edited", int64(99999999)
	_, err = app.DB.UpdateMessageAttributes(ctx, user.ID, message.ID, &body, body, &nonexistent)
	if err == nil {
		t.Fatal("invalid attachment accepted")
	}
	message, err = app.DB.Message(ctx, message.ID)
	if err != nil || message.Body != "original" {
		t.Fatalf("partial commit: %+v %v", message, err)
	}
	if _, err = app.DB.Write.ExecContext(ctx, "DELETE FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?", message.ID); err != nil {
		t.Fatal(err)
	}
	message, err = app.DB.UpdateMessageAttributes(ctx, user.ID, message.ID, &body, body, nil)
	if err != nil || message.Body != body {
		t.Fatalf("missing rich text: %+v %v", message, err)
	}
}

func TestBoostIDIsNotInterpretedAsRoomID(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "", "message", "message")
	if err != nil {
		t.Fatal(err)
	}
	var boost database.Boost
	for range 3 {
		boost, err = app.DB.CreateBoost(ctx, user.ID, message.ID, "yes")
		if err != nil {
			t.Fatal(err)
		}
	}
	response, data := perform(t, server, "DELETE", fmt.Sprintf("/messages/%d/boosts/%d", message.ID, boost.ID), "", nil, cookie)
	if response.StatusCode != 204 {
		t.Fatalf("delete: %s %s", response.Status, data)
	}
	// Root messages accept the same room_id parameter as the reference routes.
	response, data = perform(t, server, "GET", fmt.Sprintf("/messages?room_id=%d", message.RoomID), "", nil, cookie)
	if response.StatusCode != 200 || !bytes.Contains(data, []byte("message")) {
		t.Fatalf("root messages: %s %s", response.Status, data)
	}
}

func TestEmptyMessageUpdateRequiresParameter(t *testing.T) {
	r := httptest.NewRequest("PATCH", "/messages/1", nil)
	w := httptest.NewRecorder()
	if requireMessage(w, r) || w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestStagedUploadRollbackAndAbsentBody(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	staged, err := app.Storage.StageFile(ctx, "rollback.txt", "text/plain", strings.NewReader("content"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := app.Storage.Path(staged.Blob.Key)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = app.DB.Read.QueryRowContext(ctx, "SELECT count(*) FROM active_storage_blobs WHERE key=?", staged.Blob.Key).Scan(&count); err != nil || count != 0 {
		t.Fatal("premature blob row", count, err)
	}
	if _, err = app.DB.CreateMessageWithUpload(ctx, user.ID, 999999, "", nil, "rollback.txt", staged, false); err == nil {
		t.Fatal("invalid room accepted")
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rolled-back file survives", err)
	}
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	staged, err = app.Storage.StageFile(ctx, "attachment.txt", "text/plain", strings.NewReader("content"))
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessageWithUpload(ctx, user.ID, rooms[0].ID, "", nil, "attachment.txt", staged, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.DB.Read.QueryRowContext(ctx, "SELECT count(*) FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?", message.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("absent body became rich text", count, err)
	}
	path, _ = app.Storage.Path(staged.Blob.Key)
	if _, err = os.Stat(path); err != nil {
		t.Fatal("committed file missing", err)
	}
}

func TestProfileUploadRollsBackRecordAndFile(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	staged, err := app.Storage.StageFile(ctx, "avatar.txt", "text/plain", strings.NewReader("avatar"))
	if err != nil {
		t.Fatal(err)
	}
	path, _ := app.Storage.Path(staged.Blob.Key)
	// Fail after the user and blob writes, so the entire transaction must roll back.
	if _, err = app.DB.Write.ExecContext(ctx, `CREATE TRIGGER reject_avatar BEFORE INSERT ON active_storage_attachments BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = app.DB.UpdateUser(ctx, user.ID, map[string]string{"name": "Changed"}, nil, staged); err == nil {
		t.Fatal("attachment failure ignored")
	}
	unchanged, err := app.DB.User(ctx, user.ID)
	if err != nil || unchanged.Name != user.Name {
		t.Fatal("profile changed despite rollback", unchanged, err)
	}
	var n int
	if err = app.DB.Read.QueryRowContext(ctx, "SELECT count(*) FROM active_storage_blobs WHERE key=?", staged.Blob.Key).Scan(&n); err != nil || n != 0 {
		t.Fatal("blob survived rollback", n, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staged file survived rollback", err)
	}
}
