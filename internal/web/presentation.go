package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;").Replace(s)
}
func rubyFloat(n float64) string {
	s := strconv.FormatFloat(n, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}
func attachmentDimensions(b storage.Blob) (width, height, half, ratio string) {
	var metadata map[string]any
	d := json.NewDecoder(bytes.NewReader(b.Metadata))
	d.UseNumber()
	if d.Decode(&metadata) != nil {
		return
	}
	w, ok := metadata["width"].(json.Number)
	if !ok {
		return
	}
	h, ok := metadata["height"].(json.Number)
	if !ok {
		return
	}
	wf, e1 := w.Float64()
	hf, e2 := h.Float64()
	if e1 != nil || e2 != nil || wf <= 0 || hf <= 0 {
		return
	}
	width, height = string(w), string(h)
	floating := strings.ContainsAny(width, ".eE")
	if wf > 1200 || hf > 800 {
		scale := math.Min(1200/wf, 800/hf)
		wf *= scale
		hf *= scale
		width, height = rubyFloat(wf), rubyFloat(hf)
		floating = true
	}
	if floating {
		half = rubyFloat(wf / 2)
	} else {
		half = strconv.FormatInt(int64(wf)/2, 10)
	}
	ratio = rubyFloat(wf / hf)
	return
}
func attachmentHTML(b storage.Blob, blobURL, downloadURL, previewURL string) string {
	blob, download, preview := escape(blobURL), escape(downloadURL), escape(previewURL)
	if storage.Previewable(b.Type()) || storage.Variable(b.Type()) {
		width, height, half, ratio := attachmentDimensions(b)
		content := ""
		if strings.HasPrefix(b.Type(), "video") {
			content = `<video src="` + blob + `" poster="` + preview + `" controls="controls" preload="none" width="100%" height="100%" class="message__attachment"></video>`
		} else {
			size := ""
			if width != "" {
				size = ` width="` + width + `" height="` + height + `"`
			}
			content = `<a class="flex" data-lightbox-target="image" data-action="lightbox#open" data-lightbox-url-value="` + download + `" href="` + blob + `"><img` + size + ` class="message__attachment" loading="lazy" src="` + preview + `" /></a>`
		}
		if width != "" {
			return `<div class="max-inline-size center flex overflow-clip" style="width: ` + half + `px; aspect-ratio: ` + ratio + `;">` + content + `</div>`
		}
		return `<div class="max-inline-size center overflow-clip">` + content + `</div>`
	}
	name := escape(storage.Filename(b.Filename))
	return `<div class="flex-inline align-center gap-half"><img class="colorize--black" aria-hidden="true" src="` + escape(assets.Path("common-file-text.svg")) + `" width="22" height="22" /><span>` + name + `</span><a class="btn message__action-btn hide-in-ios-pwa" style="--width: auto;" href="` + download + `"><img aria-hidden="true" src="` + escape(assets.Path("download.svg")) + `" width="20" height="20" /><span class="for-screen-reader">Download ` + name + `</span></a><button class="btn message__action-btn" style="--width: auto;" data-controller="web-share" data-action="web-share#share" data-web-share-files-value="` + download + `"><img aria-hidden="true" src="` + escape(assets.Path("share.svg")) + `" width="20" height="20" /><span class="for-screen-reader">Share ` + name + `</span></button></div>`
}

type sound struct {
	Text, Image   string
	Width, Height int
}

func soundHTML(plain string) string {
	name, found := strings.CutPrefix(plain, "/play ")
	if !found {
		return ""
	}
	s, ok := sounds[name]
	if !ok {
		return ""
	}
	content := escape(s.Text)
	if s.Image != "" {
		content = fmt.Sprintf(`<img width="%d" height="%d" class="align--middle" src="%s" />`, s.Width, s.Height, escape(assets.Path("sounds/"+s.Image)))
	}
	return `<div class="sound" data-controller="sound" data-action="messages:play-&gt;sound#play" data-sound-url-value="` + escape(assets.Path(name+".mp3")) + `"><button class="btn btn--plain" data-action="sound#play">🔊</button>` + content + `</div>`
}
