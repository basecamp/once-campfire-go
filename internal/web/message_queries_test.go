package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMessageQueryDisablesStaleFragmentObservation(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE users SET name='query-author-before' WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", "<p>queryneedle</p>")); err != nil {
		t.Fatal(err)
	}
	query := func(ctx context.Context) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := app.normalizeRequest(httptest.NewRequest("GET", "/searches?q=queryneedle", nil).WithContext(ctx))
		if info := requestMetadata(ctx); info != nil {
			requestMetadata(r.Context()).databaseVersion = info.databaseVersion
		}
		writer, r := app.withBrowserSession(w, r)
		app.search(writer, r, user)
		return w
	}
	warm := query(ctx)
	if warm.Code != 200 || !strings.Contains(warm.Body.String(), `class="message__author" title="query-author-before"`) {
		t.Fatal("warm query failed", warm.Code)
	}
	// This is the generation observed by HTTP ingress. Change display data before
	// the scoped query fixes its snapshot, leaving the message timestamp unchanged.
	version, err := app.DB.ResponseVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queryCtx := context.WithValue(ctx, requestInfoKey{}, &requestInfo{databaseVersion: version})
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE users SET name='query-author-after' WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	response := query(queryCtx)
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, `class="message__author" title="query-author-after"`) || strings.Contains(body, `class="message__author" title="query-author-before"`) {
		t.Fatal("reused stale display observation", response.Code)
	}
	scope := app.MessageQueries.Observe(ctx, app.presentationFacts(queryCtx))
	read, err := app.DB.BeginMessageRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := read.PageReferences(ctx, user.ID, rooms[0].ID, 0, "around")
	read.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := app.Fragments.MessageList(scope, refs); ok {
		t.Fatal("changed observation admitted message list")
	}
	for _, ref := range refs {
		if _, ok := app.Fragments.Message(scope, ref); ok {
			t.Fatal("changed observation admitted message fragment")
		}
	}
}
