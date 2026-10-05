package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHotCacheRatio hammers the room, sidebar, and search pages after a warmup
// and checks that the in-memory caches stay hot. A disk cache is only worth
// adding when this ratio is poor.
func TestHotCacheRatio(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil || len(rooms) != 1 {
		t.Fatal(rooms, err)
	}
	room := rooms[0].ID
	for i := 0; i < 40; i++ {
		body := fmt.Sprintf("<p>hello campfire %d</p>", i)
		plain := fmt.Sprintf("hello campfire %d", i)
		if _, err = app.DB.CreateMessage(ctx, user.ID, room, fmt.Sprintf("seed-%d", i), body, plain); err != nil {
			t.Fatal(err)
		}
	}
	roomPath := fmt.Sprintf("/rooms/%d", room)
	sidebarPath := "/users/me/sidebar"
	searchPath := "/searches?q=hello"
	client := server.Client()

	get := func(path, encoding string) (int, []byte, http.Header, error) {
		request, err := http.NewRequest("GET", server.URL+path, nil)
		if err != nil {
			return 0, nil, nil, err
		}
		request.Header.Set("Accept", "*/*")
		if encoding != "" {
			request.Header.Set("Accept-Encoding", encoding)
		}
		request.AddCookie(cookie)
		response, err := client.Do(request)
		if err != nil {
			return 0, nil, nil, err
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			return response.StatusCode, nil, response.Header, err
		}
		return response.StatusCode, payload, response.Header, nil
	}

	for i := 0; i < 4; i++ {
		for _, path := range []string{roomPath, sidebarPath, searchPath} {
			code, _, _, err := get(path, "")
			if err != nil || code != 200 {
				t.Fatalf("warmup %s: %d %v", path, code, err)
			}
		}
	}
	app.ResetCacheStats()

	requests := 40
	workers := 4
	if os.Getenv("CAMPFIRE_HOT_BENCH") == "1" {
		requests = 250
		workers = 8
	}
	hammer := func(path, encoding string) float64 {
		var failures atomic.Int32
		start := time.Now()
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range requests {
					code, payload, _, err := get(path, encoding)
					if err != nil || code != 200 || len(payload) == 0 {
						failures.Add(1)
						return
					}
				}
			}()
		}
		wg.Wait()
		if failures.Load() != 0 {
			t.Fatalf("%s %s had %d failed responses", path, encoding, failures.Load())
		}
		return float64(workers*requests) / time.Since(start).Seconds()
	}

	roomRPS := hammer(roomPath, "")
	sidebarRPS := hammer(sidebarPath, "")
	searchRPS := hammer(searchPath, "")
	gzipRPS := hammer(roomPath, "gzip")

	statsResponse, err := client.Get(server.URL + "/debug/cache-stats")
	if err != nil {
		t.Fatal(err)
	}
	defer statsResponse.Body.Close()
	var stats map[string]struct {
		Hits   uint64  `json:"hits"`
		Misses uint64  `json:"misses"`
		Ratio  float64 `json:"ratio"`
	}
	if err = json.NewDecoder(statsResponse.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	t.Logf("rps room=%.0f sidebar=%.0f search=%.0f room_gzip=%.0f", roomRPS, sidebarRPS, searchRPS, gzipRPS)
	for _, name := range []string{"room_shell", "message_list", "message_window", "sidebar", "search"} {
		item := stats[name]
		t.Logf("cache %s hits=%d misses=%d ratio=%.3f", name, item.Hits, item.Misses, item.Ratio)
		if item.Hits == 0 || item.Misses != 0 {
			t.Errorf("%s cache was not hot after warmup: hits=%d misses=%d", name, item.Hits, item.Misses)
		}
	}

	code, identity, _, err := get(roomPath, "")
	if err != nil {
		t.Fatal(err)
	}
	_, compressed, header, err := get(roomPath, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if code != 200 || header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("room gzip headers: %d %v", code, header)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	// The refresh timestamp changes on every response. Everything else is cached.
	stamp := regexp.MustCompile(`data-refresh-room-loaded-at-value="\d+"`)
	placeholder := []byte(`data-refresh-room-loaded-at-value="0"`)
	if !bytes.Equal(stamp.ReplaceAll(decoded, placeholder), stamp.ReplaceAll(identity, placeholder)) {
		t.Fatalf("gzip room diverged from identity (%d vs %d bytes)", len(decoded), len(identity))
	}

	marker := "brand-new-marker"
	response, posted := perform(t, server, "POST", roomPath+"/messages", "application/x-www-form-urlencoded", strings.NewReader(url.Values{"message[body]": {"<p>" + marker + "</p>"}}.Encode()), cookie)
	response.Body.Close()
	if response.StatusCode >= 400 {
		t.Fatalf("post failed: %d %s", response.StatusCode, posted)
	}
	_, after, _, err := get(roomPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte(marker)) {
		t.Fatal("room page did not include the message posted after the cache was warm")
	}
}
