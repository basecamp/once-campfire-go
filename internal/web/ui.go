package web

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/basecamp/once-campfire-go/assets"
)

func translationButton(key string) template.HTML {
	var b strings.Builder
	fmt.Fprintf(&b, `<details class="position-relative" data-controller="popup" data-action="keydown.esc-&gt;popup#close toggle-&gt;popup#toggle click@document-&gt;popup#closeOnClickOutside" data-popup-orientation-top-class="popup-orientation-top"><summary class="btn" tabindex="-1"><img width="20" height="20" aria-hidden="true" class="color-icon" src="%s"><span class="for-screen-reader">Translate</span></summary><div class="language-list-menu shadow" data-popup-target="menu"><dl class="language-list">`, assets.Path("globe.svg"))
	for _, entry := range translations[key] {
		fmt.Fprintf(&b, `<dt>%s</dt><dd class="margin-none">%s</dd>`, template.HTMLEscapeString(entry[0]), template.HTMLEscapeString(entry[1]))
	}
	b.WriteString("</dl></div></details>")
	return template.HTML(b.String())
}
