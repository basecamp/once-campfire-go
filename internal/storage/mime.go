package storage

import (
	"bytes"
	"path"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

type magicMatch struct {
	offset, range_end int
	value             []byte
	children          []magicMatch
}
type magicEntry struct {
	contentType string
	matches     []magicMatch
}

func mimeMatches(data []byte, matches []magicMatch) bool {
	for _, m := range matches {
		if m.value == nil {
			continue
		}
		length := len(m.value)
		if m.range_end >= 0 {
			length += m.range_end - m.offset
		}
		if length > 0 && m.offset >= len(data) {
			continue
		}
		window := []byte{}
		if length > 0 {
			window = data[m.offset:min(len(data), m.offset+length)]
		}
		hit := bytes.Equal(window, m.value)
		if m.range_end >= 0 {
			hit = bytes.Contains(window, m.value)
		}
		if hit && (len(m.children) == 0 || mimeMatches(data, m.children)) {
			return true
		}
	}
	return false
}
func mimeChild(child, parent string) bool {
	if child == parent {
		return true
	}
	for _, p := range mimeParents[child] {
		if mimeChild(p, parent) {
			return true
		}
	}
	return false
}
func ExtensionType(extension string) string {
	return mimeExtensions[strings.TrimPrefix(strings.ToLower(extension), ".")]
}
func Identify(data []byte, name, declared string) string {
	candidates := []string{}
	for _, entry := range mimeMagic {
		if mimeMatches(data, entry.matches) {
			candidates = append(candidates, strings.ToLower(entry.contentType))
			break
		}
	}
	declared = strings.ToLower(declared)
	if i := strings.IndexAny(declared, ";, \t\n\r\v\f"); i >= 0 {
		declared = declared[:i]
	}
	if declared != "application/octet-stream" && strings.Contains(declared, "/") {
		candidates = append(candidates, declared)
	}
	extension := path.Ext(name)
	if strings.TrimPrefix(path.Base(name), extension) == "" {
		extension = ""
	}
	if t := ExtensionType(extension); t != "" {
		candidates = append(candidates, t)
	}
	candidates = append(candidates, "application/octet-stream")
	pick := candidates[0]
	for _, candidate := range candidates[1:] {
		if mimeChild(candidate, pick) {
			pick = candidate
		}
	}
	return pick
}
func magicPrefixLength() int {
	var reach func([]magicMatch) int
	reach = func(matches []magicMatch) int {
		n := 0
		for _, m := range matches {
			end := m.offset
			if m.range_end >= 0 {
				end = m.range_end
			}
			n = max(n, end+len(m.value), reach(m.children))
		}
		return n
	}
	n := 0
	for _, entry := range mimeMagic {
		n = max(n, reach(entry.matches))
	}
	return n
}

var MagicPrefixLength = magicPrefixLength()

func DefaultFormat(b database.Blob) string {
	if b.Type() != "image/png" && b.Type() != "image/jpeg" && b.Type() != "image/gif" && b.Type() != "image/webp" {
		return "png"
	}
	extension := strings.TrimPrefix(path.Ext(b.Filename), ".")
	if extension != "" && ExtensionType(extension) == b.Type() {
		return extension
	}
	if extensions := mimeTypeExtensions[b.Type()]; len(extensions) > 0 {
		return extensions[0]
	}
	return "png"
}
