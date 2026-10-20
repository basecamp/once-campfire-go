package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/application"
	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestOmittedMessageBodyUsesCurrentSearchContent(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", "oldneedle"))
	if err != nil {
		t.Fatal(err)
	}
	// Another command commits after the request loaded its message. The later
	// attachment-only command must derive FTS from that committed body, not captured.
	if _, err = app.DB.UpdateMessage(ctx, user.ID, captured.ID, messageInput("", "newneedle")); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("message[attachment]", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("report")); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PATCH", "/messages/1", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	if err = r.ParseMultipartForm(1024); err != nil {
		t.Fatal(err)
	}
	defer r.MultipartForm.RemoveAll()
	updated, err := app.updateMessageAttributes(r, user, captured.Message, "message[body]", "message[attachment]", nil)
	if err != nil || updated.Commit.Body != "newneedle" {
		t.Fatalf("current body: %+v %v", updated, err)
	}
	for query, count := range map[string]int{"newneedle": 1, "oldneedle": 0} {
		hits, err := app.DB.SearchReferences(ctx, user.ID, query)
		if err != nil || len(hits) != count {
			t.Fatalf("search %q: got %d, want %d: %v", query, len(hits), count, err)
		}
	}
}

func TestMessageProcessingFailureRetainsCommit(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := app.Storage.StageFile(ctx, "missing.png", "image/png", strings.NewReader("image"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := app.Storage.Path(staged.Blob.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	result, err := app.MessageCommands.Create(ctx, user.ID, rooms[0].ID, "", nil, staged)
	if err != nil || result.Commit.ID == 0 || !errors.Is(result.Processing, os.ErrNotExist) {
		t.Fatalf("processing erased commit: %+v %v", result, err)
	}
	if _, err = app.DB.Message(ctx, result.Commit.ID); err != nil {
		t.Fatal("missing committed message", err)
	}
	hits, err := app.DB.SearchReferences(ctx, user.ID, "missing")
	if err != nil || len(hits) != 1 || hits[0].ID != result.Commit.ID {
		t.Fatalf("missing committed search text: %+v %v", hits, err)
	}
}

func TestMessageAttachmentUpdatePreservesBodyAndIndexes(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", "preserved"))
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
	blob, err := app.DB.AttachedBlob(ctx, "Message", message.ID, "attachment")
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly empty body must fall back to the attachment filename in search.
	response, data = perform(t, server, "PATCH", path, "application/x-www-form-urlencoded", strings.NewReader(url.Values{"message[body]": {""}}.Encode()), cookie)
	if response.StatusCode != 302 {
		t.Fatalf("clear body: %s %s", response.Status, data)
	}
	hits, err := app.DB.SearchReferences(ctx, user.ID, "report")
	if err != nil || len(hits) != 1 {
		t.Fatalf("filename search: %+v %v", hits, err)
	}
	response, data = perform(t, server, "PATCH", path, "application/x-www-form-urlencoded", strings.NewReader(url.Values{"message[attachment]": {""}}.Encode()), cookie)
	if response.StatusCode != 302 {
		t.Fatalf("remove attachment: %s %s", response.Status, data)
	}
	if _, err = app.DB.AttachedBlob(ctx, "Message", message.ID, "attachment"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	hits, err = app.DB.SearchReferences(ctx, user.ID, "report")
	if err != nil || len(hits) != 0 {
		t.Fatalf("stale search: %+v %v", hits, err)
	}
	app.Close()
	if _, err = app.DB.Blob(ctx, blob.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("orphan blob: %v", err)
	}
}

func TestMessageUpdateRollbackAndMissingBodyRecord(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", "original"))
	if err != nil {
		t.Fatal(err)
	}
	body, nonexistent := "edited", int64(99999999)
	_, err = app.DB.UpdateMessage(ctx, user.ID, message.ID, database.MessageInput{Body: &body, Attachment: &nonexistent})
	if err == nil {
		t.Fatal("invalid attachment accepted")
	}
	unchanged, err := app.DB.Message(ctx, message.ID)
	if err != nil || unchanged.Body != "original" {
		t.Fatalf("partial commit: %+v %v", unchanged, err)
	}
	if _, err = app.DB.Write.ExecContext(ctx, "DELETE FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?", message.ID); err != nil {
		t.Fatal(err)
	}
	message, err = app.DB.UpdateMessage(ctx, user.ID, message.ID, database.MessageInput{Body: &body, Attachment: nil})
	if err != nil || message.Body != body {
		t.Fatalf("missing rich text: %+v %v", message, err)
	}
}

func TestBoostIDIsNotInterpretedAsRoomID(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", "message"))
	if err != nil {
		t.Fatal(err)
	}
	var boost database.BoostCommit
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
	if _, err = app.MessageCommands.Create(ctx, user.ID, 999999, "", nil, staged); err == nil {
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
	message, err := app.MessageCommands.Create(ctx, user.ID, rooms[0].ID, "", nil, staged)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.DB.Read.QueryRowContext(ctx, "SELECT count(*) FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?", message.Commit.ID).Scan(&count); err != nil || count != 0 {
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
	name := "Changed"
	if _, err = app.AccountCommands.UpdateUser(ctx, user.ID, user.ID, database.UserChanges{Name: &name}, application.Attachment{File: staged}); err == nil {
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
