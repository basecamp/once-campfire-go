package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCloseJoinsAdmittedHTTPHandlers(t *testing.T) {
	app, _, cookie, user := testApp(t)
	rooms, err := app.DB.Rooms(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/rooms/%d/messages", rooms[0].ID), reader)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "text/vnd.turbo-stream.html")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	requestDone := make(chan struct{})
	go func() { app.ServeHTTP(response, request); close(requestDone) }()
	// An unfinished body holds the real admitted handler inside bounded decoding.
	// Write returns only once that handler has consumed these bytes.
	if _, err := writer.Write([]byte("message[body]=drained")); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { app.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("shutdown returned while an admitted handler still owned resources")
	case <-time.After(20 * time.Millisecond):
	}
	writer.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join the completed handler")
	}
	<-requestDone
	if response.Code != http.StatusOK {
		t.Fatalf("admitted command did not finish: %d %s", response.Code, response.Body)
	}
	rejected := httptest.NewRecorder()
	app.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/up", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed application admitted another handler: %d", rejected.Code)
	}
}
