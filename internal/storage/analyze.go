package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func probe(ctx context.Context, path string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ffprobe", "-print_format", "json", "-show_streams", "-show_format", "-v", "error", path)
	raw, err := command.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return map[string]any{}, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && len(raw) == 0 {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	return result, nil
}
func object(value any) map[string]any { m, _ := value.(map[string]any); return m }
func streamOf(data map[string]any, kind string) map[string]any {
	streams, _ := data["streams"].([]any)
	for _, item := range streams {
		s := object(item)
		if s["codec_type"] == kind {
			return s
		}
	}
	return nil
}
func number(value any) (float64, error) {
	switch v := value.(type) {
	case json.Number:
		return v.Float64()
	case float64:
		return v, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(v), 64)
	default:
		return 0, fmt.Errorf("invalid numeric metadata %v", value)
	}
}
func floatNumber(n float64) json.Number {
	s := strconv.FormatFloat(n, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return json.Number(s)
}
func mediaMetadata(data map[string]any, video bool) (map[string]any, error) {
	out := map[string]any{}
	audio := streamOf(data, "audio")
	if !video {
		for _, key := range []string{"duration", "bit_rate", "sample_rate"} {
			if value := audio[key]; value != nil {
				n, err := number(value)
				if err != nil {
					return nil, err
				}
				if key == "duration" {
					out[key] = floatNumber(n)
				} else {
					out[key] = int64(n)
				}
			}
		}
		if tags := audio["tags"]; tags != nil {
			out["tags"] = tags
		}
		return out, nil
	}
	v := streamOf(data, "video")
	out["audio"] = len(audio) > 0
	out["video"] = len(v) > 0
	angle := object(v["tags"])["rotate"]
	if angle == nil {
		list, _ := v["side_data_list"].([]any)
		for _, entry := range list {
			m := object(entry)
			if m["side_data_type"] == "Display Matrix" {
				angle = m["rotation"]
				break
			}
		}
	}
	rotation := int64(0)
	if angle != nil {
		n, err := number(angle)
		if err != nil {
			return nil, err
		}
		rotation = int64(n)
		out["angle"] = rotation
	}
	var width, height *float64
	for key, destination := range map[string]**float64{"width": &width, "height": &height} {
		if value := v[key]; value != nil {
			n, err := number(value)
			if err != nil {
				return nil, err
			}
			*destination = &n
		}
	}
	if descriptor, ok := v["display_aspect_ratio"].(string); ok {
		a, b, found := strings.Cut(descriptor, ":")
		n, e1 := strconv.ParseInt(a, 10, 64)
		d, e2 := strconv.ParseInt(b, 10, 64)
		if !found || e1 != nil || e2 != nil {
			return nil, errors.New("invalid display aspect ratio")
		}
		if n != 0 {
			out["display_aspect_ratio"] = []int64{n, d}
			if width != nil {
				h := *width * float64(d) / float64(n)
				height = &h
			}
		}
	}
	if rotation == 90 || rotation == 270 || rotation == -90 || rotation == -270 {
		width, height = height, width
	}
	if width != nil {
		out["width"] = floatNumber(*width)
	}
	if height != nil {
		out["height"] = floatNumber(*height)
	}
	duration := v["duration"]
	if duration == nil {
		duration = object(data["format"])["duration"]
	}
	if duration != nil {
		n, err := number(duration)
		if err != nil {
			return nil, err
		}
		out["duration"] = floatNumber(n)
	}
	return out, nil
}
