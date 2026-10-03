// Package assets embeds the pinned Campfire frontend and public files.
package assets

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:generated
var files embed.FS

var Manifest = func() map[string]struct {
	Digested string `json:"digested_path"`
} {
	data, err := files.ReadFile("generated/manifest.json")
	if err != nil {
		panic(err)
	}
	var manifest map[string]struct {
		Digested string `json:"digested_path"`
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		panic(err)
	}
	return manifest
}()
var Importmap = template.HTML(read("generated/importmap.html"))
var Stylesheets = template.HTML(read("generated/stylesheets.html"))

func read(name string) string {
	data, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(data)
}
func Path(logical string) string {
	if asset, ok := Manifest[logical]; ok {
		return "/assets/" + asset.Digested
	}
	return logical
}
func Public() fs.FS {
	f, err := fs.Sub(files, "generated/public")
	if err != nil {
		panic(err)
	}
	return f
}
func Serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if !fs.ValidPath(name) {
		return false
	}
	body, err := files.ReadFile("generated/public/" + name)
	if err != nil {
		return false
	}
	typ := mime.TypeByExtension(path.Ext(name))
	switch path.Ext(name) {
	case ".js":
		typ = "text/javascript"
	case ".css":
		typ = "text/css"
	case ".html":
		typ = "text/html"
	}
	if typ != "" {
		w.Header().Set("Content-Type", typ)
	}
	w.Header().Set("Cache-Control", "public, max-age=2592000")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	return true
}
