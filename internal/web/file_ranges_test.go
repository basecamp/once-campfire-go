package web

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageFileRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		proxy  bool
		header string
		status int
		body   string
	}{
		{false, "bytes=a-b", 206, "0"}, {false, "bytes=1-0", 200, "0123456789"}, {false, "bytes=-0", 416, "Byte range unsatisfiable\n"},
		{true, "bytes=a-b", 206, "0"}, {true, "bytes=1-0", 416, ""}, {true, "bytes=-0", 416, ""}, {true, "bytes=1-2", 206, "12"},
	} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		stat, _ := file.Stat()
		r := httptest.NewRequest("GET", "/rails/active_storage/disk/token/file", nil)
		r.Header.Set("Range", test.header)
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "image/png")
		serveStorageFile(w, r, file, stat, "image/png", test.proxy)
		file.Close()
		if w.Code != test.status || w.Body.String() != test.body {
			t.Fatalf("proxy=%v range=%s: %d %q", test.proxy, test.header, w.Code, w.Body.String())
		}
	}
	file, _ := os.Open(path)
	defer file.Close()
	stat, _ := file.Stat()
	r := httptest.NewRequest("GET", "/rails/active_storage/disk/token/file", nil)
	r.Header.Set("Range", "bytes=0-1,3-4")
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "image/png")
	serveStorageFile(w, r, file, stat, "image/png", false)
	if w.Code != 206 || w.Header().Get("Content-Type") != "image/png" || !strings.Contains(w.Body.String(), "--AaB03x\r\ncontent-type: text/plain\r\ncontent-range: bytes 0-1/10\r\n\r\n01") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}
