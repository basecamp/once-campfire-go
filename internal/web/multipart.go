package web

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const maxMultipartBody int64 = 10 << 30

type uploadedPathsKey struct{}

func multipartBoundary(r *http.Request) string {
	kind, parameters, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch kind {
	case "multipart/form-data", "multipart/related", "multipart/mixed":
		return parameters["boundary"]
	}
	return ""
}

// File parts stream to disk. Only text fields count against the 16 MiB memory
// limit; the reference permits up to 128 files and 4096 parts in a 10 GiB body.
func parseMultipart(r *http.Request, boundary string) (cleanup func(), err error) {
	paths := map[*multipart.FileHeader]string{}
	cleanup = func() {
		for _, path := range paths {
			os.Remove(path)
		}
	}
	reader := multipart.NewReader(r.Body, boundary)
	form := &multipart.Form{Value: url.Values{}, File: map[string][]*multipart.FileHeader{}}
	var textBytes int64
	files := 0
	for parts := 0; ; parts++ {
		part, e := reader.NextRawPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return cleanup, e
		}
		if parts >= 4096 {
			return cleanup, errors.New("too many multipart parts")
		}
		_, attrs, e := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if e != nil {
			return cleanup, e
		}
		name := attrs["name"]
		if filename, exists := attrs["filename"]; exists {
			if filename == "" {
				if _, e = io.Copy(io.Discard, part); e != nil {
					return cleanup, e
				}
				continue
			}
			files++
			if files > 128 {
				return cleanup, errors.New("too many multipart files")
			}
			file, e := os.CreateTemp("", "RackMultipart")
			if e != nil {
				return cleanup, e
			}
			// Rack strips browser-supplied directory components, including Windows paths.
			filename = strings.ReplaceAll(filename, `\`, "/")
			if i := strings.LastIndexByte(filename, '/'); i >= 0 {
				filename = filename[i+1:]
			}
			header := &multipart.FileHeader{Filename: filename, Header: part.Header}
			paths[header] = file.Name()
			header.Size, e = io.Copy(file, part)
			closeErr := file.Close()
			if e != nil {
				return cleanup, e
			}
			if closeErr != nil {
				return cleanup, closeErr
			}
			form.File[name] = append(form.File[name], header)
		} else {
			data, e := io.ReadAll(io.LimitReader(part, MaxBody-textBytes+1))
			if e != nil {
				return cleanup, e
			}
			textBytes += int64(len(data))
			if textBytes > MaxBody {
				return cleanup, &http.MaxBytesError{Limit: MaxBody}
			}
			form.Value[name] = append(form.Value[name], string(data))
		}
	}
	r.MultipartForm = form
	if r.PostForm == nil {
		r.PostForm = url.Values{}
	}
	if r.Form == nil {
		r.Form = url.Values{}
	}
	for key, values := range form.Value {
		r.PostForm[key] = values
		r.Form[key] = values
	}
	for key, values := range r.URL.Query() {
		r.Form[key] = values
	}
	*r = *r.WithContext(context.WithValue(r.Context(), uploadedPathsKey{}, paths))
	return cleanup, nil
}

func uploadedFile(r *http.Request, field string) (io.ReadCloser, *multipart.FileHeader, error) {
	if paths, ok := r.Context().Value(uploadedPathsKey{}).(map[*multipart.FileHeader]string); ok {
		files := r.MultipartForm.File[field]
		if len(files) == 0 {
			return nil, nil, http.ErrMissingFile
		}
		header := files[len(files)-1]
		file, err := os.Open(paths[header])
		return file, header, err
	}
	return r.FormFile(field)
}
