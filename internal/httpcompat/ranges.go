package httpcompat

import (
	"math"
	"strings"
)

// ByteRanges follows Rack 3.2's permissive parser. Nil means a full response;
// an allocated empty slice means unsatisfiable. Bounds are inclusive.
func ByteRanges(header string, size int64) [][2]int64 {
	if size == 0 {
		return nil
	}
	spec := ""
	for offset := 0; offset < len(header); {
		index := strings.Index(header[offset:], "bytes=")
		if index < 0 {
			break
		}
		offset += index + 6
		end := strings.IndexByte(header[offset:], ';')
		if end < 0 {
			end = len(header) - offset
		}
		if end > 0 {
			spec = header[offset : offset+end]
			break
		}
	}
	if spec == "" || strings.Count(spec, ",") >= 100 {
		return nil
	}
	split := strings.Split(spec, ",")
	for i := 1; i < len(split); i++ {
		split[i] = strings.TrimLeft(split[i], " \t")
	}
	for len(split) > 0 && split[len(split)-1] == "" {
		split = split[:len(split)-1]
	}
	ranges := make([][2]int64, 0, len(split))
	total := int64(0)
	for _, value := range split {
		if !strings.Contains(value, "-") {
			return nil
		}
		parts := strings.Split(value, "-")
		for len(parts) > 0 && parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
		var start, end int64
		if len(parts) == 0 || parts[0] == "" {
			if len(parts) < 2 {
				return nil
			}
			start = max(0, size-decimalPrefix(parts[1]))
			end = size - 1
		} else {
			start = decimalPrefix(parts[0])
			end = size - 1
			if len(parts) > 1 {
				last := decimalPrefix(parts[1])
				if last < start {
					return nil
				}
				end = min(last, size-1)
			}
		}
		if start <= end {
			length := end - start + 1
			if total > size-length {
				return [][2]int64{}
			}
			total += length
			ranges = append(ranges, [2]int64{start, end})
		}
	}
	return ranges
}
func decimalPrefix(text string) int64 {
	text = strings.TrimLeft(text, " \t\n\r\v\f")
	text = strings.TrimPrefix(text, "+")
	if strings.HasPrefix(text, "0d") || strings.HasPrefix(text, "0D") {
		text = text[2:]
	}
	var n int64
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '_' && i > 0 && i+1 < len(text) && text[i-1] >= '0' && text[i-1] <= '9' && text[i+1] >= '0' && text[i+1] <= '9' {
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		digit := int64(c - '0')
		if n > (math.MaxInt64-digit)/10 {
			return math.MaxInt64
		}
		n = n*10 + digit
	}
	return n
}
