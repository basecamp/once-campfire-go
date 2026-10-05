package web

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"hash/crc32"
	"math/bits"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/zstd"
)

func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		if raw, ok = os.LookupEnv("THRUSTER_" + key); !ok {
			return fallback
		}
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

// responseEncoding applies the same vetoes as the public front compressor
// before negotiating Accept-Encoding for a cached private response.
func responseEncoding(w http.ResponseWriter, r *http.Request) string {
	if r == nil || r.Method == "HEAD" {
		return ""
	}
	if !envBool("GZIP_COMPRESSION_ENABLED", true) {
		return ""
	}
	if envBool("GZIP_COMPRESSION_DISABLE_ON_AUTH", false) {
		for _, name := range []string{"Cookie", "Authorization", "X-CSRF-Token"} {
			if r.Header.Get(name) != "" {
				return ""
			}
		}
	}
	if w != nil && w.Header().Get("No-Gzip-Compression") != "" {
		return ""
	}
	return negotiatedEncoding(r)
}

// negotiatedEncoding matches the public front server: zstd when it is at least
// as acceptable as gzip, otherwise gzip, otherwise identity.
func negotiatedEncoding(r *http.Request) string {
	if r == nil || r.Method == "HEAD" {
		return ""
	}
	quality := func(name string) float64 {
		for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
			pieces := strings.Split(part, ";")
			if !strings.EqualFold(strings.TrimSpace(pieces[0]), name) {
				continue
			}
			q := 1.0
			for _, p := range pieces[1:] {
				if value, ok := strings.CutPrefix(strings.TrimSpace(p), "q="); ok {
					q, _ = strconv.ParseFloat(value, 64)
					q = min(1, max(0, q))
				}
			}
			return q
		}
		return 0
	}
	gz, zs := quality("gzip"), quality("zstd")
	if zs > 0 && zs >= gz {
		return "zstd"
	}
	if gz > 0 {
		return "gzip"
	}
	return ""
}

func zstdBytes(p []byte) []byte {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil
	}
	if _, err = w.Write(p); err != nil {
		return nil
	}
	if err = w.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// paddingComment reproduces the front server's deterministic gzip comment so a
// cached encoding decompresses to the same bytes and keeps a stable size.
func paddingComment(body []byte) string {
	const n = 32
	checksum := crc32.Checksum(body[:min(len(body), 64<<10)], crc32.MakeTable(crc32.Castagnoli))
	length := 1 + int((bits.RotateLeft32(checksum, 19)^0xab0755de)%uint32(n))
	padding := make([]byte, length)
	for i := range padding {
		padding[i] = "Padding-"[i%8]
	}
	return string(padding)
}

func gzipMember(p []byte) []byte {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil
	}
	w.Comment = paddingComment(p)
	if _, err = w.Write(p); err != nil {
		return nil
	}
	if err = w.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

func zstdMember(p []byte) []byte {
	body := zstdBytes(p)
	if body == nil {
		return nil
	}
	jitter := []byte(paddingComment(p))
	trailer := make([]byte, 8+len(jitter))
	binary.LittleEndian.PutUint32(trailer, 0x184D2A50)
	binary.LittleEndian.PutUint32(trailer[4:], uint32(len(jitter)))
	copy(trailer[8:], jitter)
	return append(body, trailer...)
}

func encodedBytes(raw, gz, zs []byte, enc string) []byte {
	switch enc {
	case "gzip":
		if len(gz) > 0 {
			return gz
		}
		return gzipMember(raw)
	case "zstd":
		if len(zs) > 0 {
			return zs
		}
		return zstdMember(raw)
	default:
		return raw
	}
}
