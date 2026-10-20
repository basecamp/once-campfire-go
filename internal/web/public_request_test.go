package web

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/front"
)

// Unit tests cover individual forwarding aliases and application IP parsing.
// This checks their composition through the real public listener: a private peer
// must not turn a surviving Client-IP list into a new login-throttle identity.
func TestPublicListenerRejectsClientIPThrottleBypass(t *testing.T) {
	app, _, _, _ := testApp(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	config := front.FromLookup(func(string) (string, bool) { return "", false })
	config.HTTPPort, config.TargetPort = port, port
	config.ForwardHeaders, config.LogRequests = false, false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- front.Serve(ctx, config, app) }()
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("public listener did not stop")
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := client.Get(base + "/up")
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal(response.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for attempt := 1; attempt <= 12; attempt++ {
		request, err := http.NewRequest("POST", base+"/session", strings.NewReader("email_address=owner%40test&password=wrong"))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", base)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		spoof := "127.0.0.1, 198.51.100.20"
		if attempt == 12 {
			spoof = "127.0.0.1, 198.51.100.21"
		}
		request.Header.Set("Client-IP", spoof)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		want := 401
		if attempt > 10 {
			want = 429
		}
		if response.StatusCode != want {
			t.Fatalf("public attempt%d status%d, want%d; changed Client-IP must not reset throttling", attempt, response.StatusCode, want)
		}
	}
}
