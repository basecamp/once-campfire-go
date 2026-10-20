package presentation

import (
	"bytes"
	"html/template"
	"io"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

// Renderer owns templates and their compiled layouts. It consumes complete
// owned inputs, not requests, contexts, database handles or data loaders.
type Renderer struct {
	templates       *template.Template
	messageLayouts  messageLayouts
	secrets         *rails.Secrets
	storageVerifier rails.Verifier
}

func NewRenderer(secrets *rails.Secrets) (*Renderer, error) {
	templates, layouts, err := parseTemplates(secrets)
	if err != nil {
		return nil, err
	}
	return &Renderer{templates: templates, messageLayouts: layouts, secrets: secrets, storageVerifier: secrets.AppVerifier("ActiveStorage")}, nil
}

func (r *Renderer) ExecuteTemplate(w io.Writer, name string, data any) error {
	return r.templates.ExecuteTemplate(w, name, data)
}

func (r *Renderer) Markup(name string, data any) (string, error) {
	var b bytes.Buffer
	err := r.ExecuteTemplate(&b, name, data)
	return b.String(), err
}
