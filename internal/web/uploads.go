package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func (s *Server) registerStorageRoutes() {
	s.mux.HandleFunc("GET /rails/active_storage/representations/redirect/{token}/{variation}/{filename...}", s.representation)
	s.mux.HandleFunc("GET /rails/active_storage/representations/proxy/{token}/{variation}/{filename...}", s.representation)
	s.mux.HandleFunc("GET /rails/active_storage/representations/{token}/{variation}/{filename...}", s.representation)
	s.mux.HandleFunc("POST /rails/active_storage/direct_uploads", s.storageAuth(s.directUpload))
	s.mux.HandleFunc("PUT /rails/active_storage/disk/{token}", s.storageAuth(s.diskUpload))
	s.mux.HandleFunc("GET /rails/active_storage/disk/{token}/{filename...}", s.diskDownload)
	s.mux.HandleFunc("GET /rails/active_storage/blobs/redirect/{token}/{filename...}", s.blobDownload)
	s.mux.HandleFunc("GET /rails/active_storage/blobs/proxy/{token}/{filename...}", s.blobDownload)
	s.mux.HandleFunc("GET /rails/active_storage/blobs/{token}/{filename...}", s.blobDownload)
}
func (s *Server) directUpload(w http.ResponseWriter, r *http.Request, _ database.User) {
	var data struct {
		Blob map[string]json.RawMessage `json:"blob"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil || data.Blob == nil {
		http.Error(w, "Invalid blob", 400)
		return
	}
	scalar := func(key string) (string, bool) {
		raw, ok := data.Blob[key]
		if !ok {
			return "", false
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text, true
		}
		var number json.Number
		if json.Unmarshal(raw, &number) == nil && len(raw) > 0 && string(raw) != "null" {
			return number.String(), true
		}
		return "", false
	}
	filename, _ := scalar("filename")
	checksum, _ := scalar("checksum")
	sizeText, hasSize := scalar("byte_size")
	if filename == "" || checksum == "" || !hasSize {
		http.Error(w, "Invalid blob", 422)
		return
	}
	// Active Record's integer cast accepts a numeric prefix (but not nonnumbers).
	sizeText = strings.TrimLeft(sizeText, " \t\r\n\v\f")
	end := 0
	if strings.HasPrefix(sizeText, "+") || strings.HasPrefix(sizeText, "-") {
		end = 1
	}
	digits := end
	for end < len(sizeText) && sizeText[end] >= '0' && sizeText[end] <= '9' {
		end++
	}
	if end == digits {
		http.Error(w, "Invalid byte size", 422)
		return
	}
	size, err := strconv.ParseInt(sizeText[:end], 10, 64)
	if err != nil || size < 0 || size > MaxBody {
		http.Error(w, "Request too large", 413)
		return
	}
	contentType, hasType := scalar("content_type")
	b := storage.Blob{Filename: filename, Checksum: checksum, ByteSize: size, Metadata: json.RawMessage("{}")}
	if hasType {
		b.ContentType = &contentType
	}
	if raw := data.Blob["metadata"]; len(raw) > 0 && raw[0] == '{' {
		b.Metadata = raw
	}
	b.ID = 0
	b.Key = ""
	b.ServiceName = "local"
	b, err = s.Storage.Create(r.Context(), b)
	if err != nil {
		s.fail(w, err)
		return
	}
	path, err := s.Storage.UploadURL(b)
	if err != nil {
		s.fail(w, err)
		return
	}
	response := struct {
		storage.Blob
		SignedID     string `json:"signed_id"`
		DirectUpload struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"direct_upload"`
	}{Blob: b, SignedID: s.Storage.SignedID(b)}
	if created, err := time.Parse("2006-01-02 15:04:05.999999", b.CreatedAt); err == nil {
		response.Blob.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	response.DirectUpload.URL = s.origin(r) + path
	response.DirectUpload.Headers = map[string]string{"Content-Type": b.Type()}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(response)
}
func (s *Server) diskUpload(w http.ResponseWriter, r *http.Request, _ database.User) {
	var token storage.DiskToken
	if err := s.Storage.Verifier.Verify(r.PathValue("token"), "blob_token", s.DB.Now(), &token); err != nil {
		http.NotFound(w, r)
		return
	}
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		ct = ""
	}
	expected := ""
	if token.ContentType != nil {
		expected = *token.ContentType
	}
	if !strings.EqualFold(ct, expected) || r.ContentLength != token.ContentLength || token.ContentLength < 0 {
		w.WriteHeader(422)
		return
	}
	if err = s.Storage.Upload(r.Context(), token, r.Body); err != nil {
		if errors.Is(err, storage.ErrIntegrity) {
			w.WriteHeader(422)
		} else {
			s.fail(w, err)
		}
		return
	}
	w.WriteHeader(204)
}
func (s *Server) diskDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "max-age=3600, public")
	var key storage.DiskKey
	if err := s.Storage.Verifier.Verify(r.PathValue("token"), "blob_key", s.DB.Now(), &key); err != nil {
		http.NotFound(w, r)
		return
	}
	path, err := s.Storage.Path(key.Key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := ""
	if key.ContentType != nil {
		ct = *key.ContentType
	}
	s.serveStored(w, r, path, ct, key.Disposition, false)
}
func (s *Server) blobDownload(w http.ResponseWriter, r *http.Request) {
	b, err := s.Storage.FindSigned(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	disposition := r.URL.Query().Get("disposition")
	if disposition != "attachment" {
		disposition = "inline"
	}
	if !storage.Inline(b.Type()) {
		disposition = "attachment"
	}
	if strings.Contains(r.URL.Path, "/proxy/") {
		path, err := s.Storage.Path(b.Key)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=3155695200, public, immutable")
		s.serveStored(w, r, path, storage.ServingType(b.Type()), storage.Disposition(disposition, storage.Filename(b.Filename)), true)
		return
	}
	path, err := s.Storage.DiskURL(b, disposition)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "max-age=300, private")
	http.Redirect(w, r, s.origin(r)+path, 302)
}
func (s *Server) serveStored(w http.ResponseWriter, r *http.Request, path, ct, disposition string, proxy bool) {
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		s.fail(w, err)
		return
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Disposition", disposition)
	if strings.HasPrefix(r.URL.Path, "/rails/active_storage/") {
		serveStorageFile(w, r, file, stat, ct, proxy)
		return
	}
	modified := stat.ModTime()
	if proxy {
		modified = time.Time{}
	}
	http.ServeContent(w, r, stat.Name(), modified, file)
}
func (s *Server) uploadAttachment(r *http.Request, field string) (storage.Blob, error) {
	staged, err := s.stageAttachment(r, field)
	if err != nil {
		return storage.Blob{}, err
	}
	return staged.Save(r.Context())
}
func (s *Server) stageAttachment(r *http.Request, field string) (*storage.Staged, error) {
	file, header, err := uploadedFile(r, field)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	head := make([]byte, storage.MagicPrefixLength)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	head = head[:n]
	ct := storage.Identify(head, header.Filename, header.Header.Get("Content-Type"))
	return s.Storage.StageFile(r.Context(), header.Filename, ct, io.MultiReader(strings.NewReader(string(head)), file))
}
func (s *Server) attachUploaded(r *http.Request, field, kind string, id int64, name string) error {
	if r.MultipartForm == nil || len(r.MultipartForm.File[field]) == 0 {
		return nil
	}
	b, err := s.uploadAttachment(r, field)
	if err != nil {
		return err
	}
	return s.Storage.Attach(r.Context(), b, kind, id, name)
}

func (s *Server) storageAuth(next func(http.ResponseWriter, *http.Request, database.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_token")
		if err != nil {
			w.WriteHeader(401)
			return
		}
		var token string
		if err = s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(cookie.Value), s.DB.Now(), &token); err != nil {
			w.WriteHeader(401)
			return
		}
		u, err := s.DB.SessionUser(r.Context(), token)
		if err != nil {
			w.WriteHeader(401)
			return
		}
		next(w, r, u)
	}
}

func (s *Server) representation(w http.ResponseWriter, r *http.Request) {
	b, err := s.Storage.FindSigned(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.Storage.DecodeVariation(r.PathValue("variation"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, err = s.Storage.Representation(r.Context(), b, v)
	if err != nil {
		s.fail(w, err)
		return
	}
	if strings.Contains(r.URL.Path, "/proxy/") {
		path, err := s.Storage.Path(b.Key)
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "max-age=3155695200, public, immutable")
		s.serveStored(w, r, path, b.Type(), storage.Disposition("inline", storage.Filename(b.Filename)), true)
		return
	}
	path, err := s.Storage.DiskURL(b, r.URL.Query().Get("disposition"))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "max-age=300, private")
	http.Redirect(w, r, s.origin(r)+path, 302)
}

func (s *Server) optionalUpload(r *http.Request, field string) (*storage.Staged, error) {
	if r.MultipartForm == nil || len(r.MultipartForm.File[field]) == 0 {
		return nil, nil
	}
	return s.stageAttachment(r, field)
}
func (s *Server) analyzeUpload(upload *storage.Staged) {
	if upload != nil {
		s.Jobs.Enqueue("analyze", func(ctx context.Context) error {
			_, err := s.Storage.Analyze(ctx, upload.Blob)
			return err
		})
	}
}
