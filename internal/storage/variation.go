package storage

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

type Symbol string
type Entry struct {
	Key   string
	Value any
}
type Variation []Entry

func Resize(width, height int64, format string) Variation {
	v := Variation{{"resize_to_limit", []any{width, height}}}
	if format != "" {
		v = append(v, Entry{"format", Symbol(format)})
	}
	return v
}
func (v Variation) Get(key string) any {
	for _, e := range v {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}
func (v Variation) DefaultFormat(format string) Variation {
	result := Variation{{"format", Symbol(format)}}
	for _, e := range v {
		if e.Key == "format" {
			result[0] = e
		} else {
			result = append(result, e)
		}
	}
	return result
}
func (v Variation) Format() (string, error) {
	value := v.Get("format")
	format := "png"
	switch x := value.(type) {
	case string:
		format = x
	case Symbol:
		format = string(x)
	case nil:
	default:
		return "", errors.New("invalid format")
	}
	if !slices.Contains([]string{"png", "webp", "jpeg", "jpg", "gif", "tiff", "avif", "heic", "heif"}, format) {
		return "", fmt.Errorf("unsupported format %q", format)
	}
	return format, nil
}
func (v Variation) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, e := range v {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(e.Key)
		value, err := json.Marshal(e.Value)
		if err != nil {
			return nil, err
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}
func (s *Store) VariationKey(v Variation) (string, error) {
	raw, err := v.MarshalJSON()
	if err != nil {
		return "", err
	}
	return s.Verifier.GenerateRaw(raw, "variation", time.Time{})
}
func (s *Store) DecodeVariation(key string) (Variation, error) {
	raw, err := s.Verifier.VerifyRaw(key, "variation", s.DB.Now())
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readVariationValue(decoder)
	if err != nil {
		return nil, err
	}
	v, ok := value.(Variation)
	if !ok {
		return nil, errors.New("transformations must be a hash")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing transformation data")
	}
	return v, nil
}
func readVariationValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch x := token.(type) {
	case json.Delim:
		if x == '{' {
			var entries Variation
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				value, err := readVariationValue(decoder)
				if err != nil {
					return nil, err
				}
				entries = append(entries, Entry{key.(string), value})
			}
			_, err := decoder.Token()
			return entries, err
		}
		if x == '[' {
			values := []any{}
			for decoder.More() {
				value, err := readVariationValue(decoder)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			_, err := decoder.Token()
			return values, err
		}
		return nil, errors.New("invalid delimiter")
	case json.Number:
		return x.Int64()
	default:
		return token, nil
	}
}
func (v Variation) MarshalRuby() []byte {
	w := marshalWriter{out: []byte{4, 8}}
	w.value(v)
	return w.out
}
func (v Variation) Digest() string {
	sum := sha1.Sum(v.MarshalRuby())
	return base64.StdEncoding.EncodeToString(sum[:])
}

type marshalWriter struct {
	out     []byte
	symbols []string
}

func (w *marshalWriter) long(n int64) {
	if n == 0 {
		w.out = append(w.out, 0)
	} else if n > 0 && n < 123 {
		w.out = append(w.out, byte(n+5))
	} else if n < 0 && n > -124 {
		w.out = append(w.out, byte(n-5))
	} else {
		var data []byte
		x := n
		for i := 0; i < 8; i++ {
			data = append(data, byte(x))
			x >>= 8
			if x == 0 {
				w.out = append(w.out, byte(len(data)))
				break
			}
			if x == -1 {
				w.out = append(w.out, byte(-len(data)))
				break
			}
		}
		w.out = append(w.out, data...)
	}
}
func (w *marshalWriter) str(s string) { w.long(int64(len(s))); w.out = append(w.out, s...) }
func (w *marshalWriter) symbol(s string) {
	if i := slices.Index(w.symbols, s); i >= 0 {
		w.out = append(w.out, ';')
		w.long(int64(i))
	} else {
		w.symbols = append(w.symbols, s)
		w.out = append(w.out, ':')
		w.str(s)
	}
}
func (w *marshalWriter) value(value any) {
	switch x := value.(type) {
	case nil:
		w.out = append(w.out, '0')
	case bool:
		if x {
			w.out = append(w.out, 'T')
		} else {
			w.out = append(w.out, 'F')
		}
	case int64:
		if x >= -(1<<30) && x < 1<<30 {
			w.out = append(w.out, 'i')
			w.long(x)
		} else {
			sign := byte('+')
			magnitude := uint64(x)
			if x < 0 {
				sign = '-'
				magnitude = uint64(-(x + 1)) + 1
			}
			w.out = append(w.out, 'l', sign)
			var digits []byte
			for magnitude > 0 {
				digits = append(digits, byte(magnitude))
				magnitude >>= 8
			}
			if len(digits)%2 != 0 {
				digits = append(digits, 0)
			}
			w.long(int64(len(digits) / 2))
			w.out = append(w.out, digits...)
		}
	case Symbol:
		w.symbol(string(x))
	case string:
		w.out = append(w.out, 'I', '"')
		w.str(x)
		w.long(1)
		w.symbol("E")
		w.out = append(w.out, 'T')
	case []any:
		w.out = append(w.out, '[')
		w.long(int64(len(x)))
		for _, v := range x {
			w.value(v)
		}
	case Variation:
		w.out = append(w.out, '{')
		w.long(int64(len(x)))
		for _, e := range x {
			w.symbol(e.Key)
			w.value(e.Value)
		}
	default:
		panic(fmt.Sprintf("unsupported Marshal value %T", value))
	}
}
