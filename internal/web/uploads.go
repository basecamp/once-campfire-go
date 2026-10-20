package web

import (
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

func (s *Server) directUpload(w http.ResponseWriter, r *http.Request, _ database.User) {
	params, _ := r.Context().Value(structuredParamsKey{}).(map[string]any)
	attributes, _ := params["blob"].(map[string]any)
	if len(attributes) == 0 {
		http.Error(w, "Invalid blob", 400)
		return
	}
	scalar := func(key string) (string, bool) {
		switch value := attributes[key].(type) {
		case string:
			return value, true
		case json.Number:
			return value.String(), true
		default:
			return "", false
		}
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
	b := database.Blob{
		Filename: filename,
		Checksum: checksum,
		ByteSize: size,
		Metadata: json.RawMessage("{}"),
	}
	if hasType {
		b.ContentType = &contentType
	}
	if metadata, ok := attributes["metadata"].(map[string]any); ok {
		b.Metadata, err = json.Marshal(metadata)
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	b.ID = 0
	b.Key = ""
	b.ServiceName = "local"
	b, err = s.DB.CreateBlob(r.Context(), b)
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
		database.Blob
		SignedID     string `json:"signed_id"`
		DirectUpload struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"direct_upload"`
	}{Blob: b, SignedID: storage.SignedID(s.Storage.Verifier, b)}
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
	if err := s.Storage.Verifier.Verify(r.PathValue("encoded_token"), "blob_token", s.DB.Now(), &token); err != nil {
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
	if !strings.EqualFold(ct, expected) || r.ContentLength != token.ContentLength ||
		token.ContentLength < 0 {
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
	if err := s.Storage.Verifier.Verify(r.PathValue("encoded_key"), "blob_key", s.DB.Now(), &key); err != nil {
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
	s.serveStored(w, r, path, ct, key.Disposition, diskFile)
}

func (s *Server) blobDownload(w http.ResponseWriter, r *http.Request) {
	b, err := s.Storage.FindSigned(r.Context(), r.PathValue("signed_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	disposition := r.URL.Query().Get("disposition")
	// Blob byte ranges are sent inline unless the MIME type forces a download.
	if r.PathValue("controller") == "active_storage/blobs/proxy" && strings.TrimSpace(r.Header.Get("Range")) != "" {
		disposition = "inline"
	}
	if disposition != "attachment" {
		disposition = "inline"
	}
	if !storage.Inline(b.Type()) {
		disposition = "attachment"
	}
	if r.PathValue("controller") == "active_storage/blobs/proxy" {
		path, err := s.Storage.Path(b.Key)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=3155695200, public, immutable")
		s.serveStored(
			w,
			r,
			path,
			storage.ServingType(b.Type()),
			storage.Disposition(disposition, storage.Filename(b.Filename)),
			blobFile,
		)
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

func (s *Server) serveStored(
	w http.ResponseWriter,
	r *http.Request,
	path, ct, disposition string,
	mode storageFileMode,
) {
	file, err := os.Open(path)
	if err != nil {
		w.Header().Set("Cache-Control", "no-cache")
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			s.fail(w, err)
		}
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		w.Header().Set("Cache-Control", "no-cache")
		s.fail(w, err)
		return
	}
	if !stat.Mode().IsRegular() {
		w.Header().Set("Cache-Control", "no-cache")
		http.NotFound(w, r)
		return
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Disposition", disposition)
	if mode == contentFile {
		streamFileResponse(w)
		http.ServeContent(w, r, stat.Name(), time.Time{}, file)
		return
	}
	serveStorageFile(w, r, file, stat, ct, mode)
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
	return s.Storage.StageFile(
		r.Context(),
		header.Filename,
		ct,
		io.MultiReader(strings.NewReader(string(head)), file),
	)
}

func (s *Server) storageAuth(
	next func(http.ResponseWriter, *http.Request, database.User),
) http.HandlerFunc {
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
	b, err := s.Storage.FindSigned(r.Context(), r.PathValue("signed_blob_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.Storage.DecodeVariation(r.PathValue("variation_key"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, err = s.Storage.Representation(r.Context(), b, v)
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.PathValue("controller") == "active_storage/representations/proxy" {
		path, err := s.Storage.Path(b.Key)
		if err != nil {
			s.fail(w, err)
			return
		}
		disposition := r.URL.Query().Get("disposition")
		if disposition != "attachment" {
			disposition = "inline"
		}
		if !storage.Inline(b.Type()) {
			disposition = "attachment"
		}
		w.Header().Set("Cache-Control", "max-age=3155695200, public, immutable")
		s.serveStored(
			w,
			r,
			path,
			storage.ServingType(b.Type()),
			storage.Disposition(disposition, storage.Filename(b.Filename)),
			representationFile,
		)
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
