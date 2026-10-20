package web

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/qrcode"
)

var qrSlots = make(chan struct{}, 4)

func (s *Server) qrCode(w http.ResponseWriter, r *http.Request) {
	value := strings.NewReplacer("-", "+", "_", "/").Replace(r.PathValue("id"))
	var data []byte
	var err error
	if strings.Contains(value, "=") {
		data, err = base64.StdEncoding.Strict().DecodeString(value)
	} else {
		data, err = base64.RawStdEncoding.Strict().DecodeString(value)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	select {
	case qrSlots <- struct{}{}:
		defer func() { <-qrSlots }()
	case <-r.Context().Done():
		return
	}
	body, ok := qrcode.SVG(data)
	if !ok {
		w.WriteHeader(422)
		return
	}
	w.Header().Set("Cache-Control", "max-age=31556952, public")
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Write([]byte(body))
}
