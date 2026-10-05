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
		s.mux.HandleFunc("GET "+path, s.browserCheck(s.pwaManifest))
	}
	for _, path := range []string{"/service-worker", "/service-worker.js"} {
		s.mux.HandleFunc("GET "+path, s.browserCheck(s.pwaServiceWorker))
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
