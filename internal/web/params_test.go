package web

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestJSONParametersKeepRawBodyAndQueryPrecedence(t *testing.T) {
	raw := `{"message":{"body":null,"attachment":null},"room_id":42,"ids":[1,2]}`
	r := httptest.NewRequest("PATCH", "/messages/1?room_id=17", strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if err := parseJSONParams(r); err != nil {
		t.Fatal(err)
	}
	if !nullParam(r, "message[body]") || !r.Form.Has("message[attachment]") || r.Form.Get("room_id") != "17" || strings.Join(r.Form["ids[]"], ",") != "1,2" {
		t.Fatal(r.Form)
	}
	body, _ := io.ReadAll(r.Body)
	if string(body) != raw {
		t.Fatalf("raw body: %q", body)
	}
	for _, raw := range []string{`{"broken":`, `{} {}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.ParseForm()
		if err := parseJSONParams(r); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestMultipartSpoolsLargeFilesAndRemovesTemporaryFiles(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("attachment", `C:\fakepath\report.txt`)
	io.CopyN(part, &zeroReader{}, MaxBody+1)
	writer.WriteField("title", "report")
	writer.Close()
	r := httptest.NewRequest("POST", "/?title=query", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	cleanup, err := parseMultipart(r, multipartBoundary(r))
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	file, header, err := uploadedFile(r, "attachment")
	if err != nil {
		t.Fatal(err)
	}
	size, err := io.Copy(io.Discard, file)
	file.Close()
	if err != nil || size != MaxBody+1 || header.Filename != "report.txt" || r.Form.Get("title") != "query" {
		t.Fatal(size, header, r.Form, err)
	}
	paths := r.Context().Value(uploadedPathsKey{}).(map[*multipart.FileHeader]string)
	cleanup()
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary file survived: %v", err)
		}
	}
}

type zeroReader struct{}

func (*zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestMultipartTextLimitCleansEarlierFiles(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("attachment", "first.txt")
	part.Write([]byte("content"))
	part, _ = writer.CreateFormField("body")
	io.CopyN(part, &zeroReader{}, MaxBody+1)
	writer.Close()
	r := httptest.NewRequest("POST", "/", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	cleanup, err := parseMultipart(r, multipartBoundary(r))
	cleanup()
	var limit *http.MaxBytesError
	if !errors.As(err, &limit) {
		t.Fatal(err)
	}
}
