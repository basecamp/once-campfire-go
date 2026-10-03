package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"text/template"

	"github.com/basecamp/once-campfire-go/assets"
)

//go:embed pwa/*
var pwaFiles embed.FS
var manifestTemplate = template.Must(template.New("manifest.json").Funcs(template.FuncMap{
	"json": func(s string) string {
		var b bytes.Buffer
		encoder := json.NewEncoder(&b)
		encoder.SetEscapeHTML(false)
		encoder.Encode(s)
		return string(bytes.TrimSuffix(b.Bytes(), []byte{'\n'}))
	},
	"image": func(origin, path string) string { return origin + assets.Path(path) },
}).ParseFS(pwaFiles, "pwa/manifest.json"))

func (s *Server) registerPWARoutes() {
	for _, path := range []string{"/webmanifest", "/webmanifest.json"} {
		s.mux.HandleFunc("GET "+path, s.browserCheck(s.manifest))
	}
	worker, _ := pwaFiles.ReadFile("pwa/service_worker.js")
	for _, path := range []string{"/service-worker", "/service-worker.js"} {
		s.mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			if s.blockBrowser(w, r) {
				return
			}
			if respondFormat(w, r, "js") == "" {
				return
			}
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Write(worker)
		})
	}
}
func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	if respondFormat(w, r, "json") == "" {
		return
	}
	a, _ := s.DB.Account(r.Context())
	name := a.Name
	if a.ID == 0 {
		name = "Campfire"
	}
	v := ""
	if a.ID != 0 {
		v = a.UpdatedAt.UTC().Format("20060102150405")
	}
	data := struct{ Name, Small, Logo, Origin string }{name, "/account/logo?size=small&v=" + v, "/account/logo?v=" + v, s.origin(r)}
	var b bytes.Buffer
	if err := manifestTemplate.Execute(&b, data); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	fmt.Fprint(w, b.String())
}
