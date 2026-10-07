package richtext

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

// Run with CAMPFIRE_RICHTEXT_ORACLE set to output from bench/richtext-oracle.
func TestRustOracle(t *testing.T) {
	path := os.Getenv("CAMPFIRE_RICHTEXT_ORACLE")
	if path == "" {
		path = "testdata/rust.json.gz"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(path, ".gz") {
		reader, e := gzip.NewReader(bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		raw, e = io.ReadAll(reader)
		reader.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	type outcome struct {
		OK    json.RawMessage `json:"ok"`
		Error string          `json:"error"`
	}
	var corpus struct {
		Users []struct {
			ID          int64
			Name, Title string
			SGID        string `json:"attachable_sgid"`
			Path        string `json:"user_path"`
			Avatar      string `json:"avatar_path"`
		}
		Signed []struct {
			SGID, Model string
			ID          int64
			Exists      bool
		}
		Cases []struct {
			Name, Body, Host                            string
			Presentation, Editable, Filtered, Mentioned outcome
			Plain                                       outcome `json:"plain_text"`
			BodyHTML                                    outcome `json:"body_html"`
		}
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	users := map[int64]*Mention{}
	for _, u := range corpus.Users {
		users[u.ID] = &Mention{u.ID, u.Name, u.Title, u.SGID, u.Path, u.Avatar}
	}
	resolve := func(sgid string, verified bool) (*Mention, error) {
		if verified {
			for _, s := range corpus.Signed {
				if s.SGID == sgid && s.Model == "User" && s.Exists {
					return users[s.ID], nil
				}
			}
			return nil, nil
		}
		gid, err := rails.UnverifiedUserGID(sgid)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(strings.Split(gid, "?")[0], "/")
		if len(parts) < 2 || parts[len(parts)-2] != "User" {
			return nil, nil
		}
		id, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		return users[id], nil
	}
	type mismatch struct {
		Name, Field string
		Got         any
		Want        any
		Error       string
	}
	var failures []mismatch
	totals := map[string]int{}
	matched := map[string]int{}
	for _, c := range corpus.Cases {
		result, err := Process(c.Body, Context{Host: c.Host, Resolve: resolve})
		display, displayErr := Display(c.Body, Context{Host: c.Host, Resolve: resolve})
		if display.Presentation != result.Presentation || display.Plain != result.Plain {
			t.Fatalf("%s: focused display differs: %v", c.Name, displayErr)
		}
		ids, _ := MentionIDs(c.Body, Context{Host: c.Host, Resolve: resolve})
		if !reflect.DeepEqual(ids, result.Mentioned) {
			t.Fatalf("%s: focused mentions differ", c.Name)
		}
		// ENGINE-45b: the create path derives display, plain text and
		// mentions from ProcessMessage's single parse; its outputs must be
		// exactly the focused calls' outputs, field for field.
		message, messageErr := ProcessMessage(c.Body, Context{Host: c.Host, Resolve: resolve})
		if message.Presentation != display.Presentation || message.Plain != display.Plain {
			t.Fatalf("%s: ProcessMessage display differs: %v", c.Name, messageErr)
		}
		if !reflect.DeepEqual(message.Mentioned, ids) {
			t.Fatalf("%s: ProcessMessage mentions differ", c.Name)
		}
		edited, _ := Editable(c.Body, Context{Host: c.Host, Resolve: resolve})
		if edited != result.Editable {
			t.Fatalf("%s: focused editor differs", c.Name)
		}

		plain, plainErr := PlainText(c.Body, Context{Host: c.Host, Resolve: resolve})
		if plain != result.Plain || (plainErr != nil) != (result.Errors["plain"] != nil) {
			t.Fatalf("%s: focused plain text differs: %q, %v", c.Name, plain, plainErr)
		}
		checks := []struct {
			name string
			want outcome
			got  any
		}{{"presentation", c.Presentation, result.Presentation}, {"plain", c.Plain, result.Plain}, {"filtered", c.Filtered, result.Filtered}, {"body_html", c.BodyHTML, result.BodyHTML}, {"editable", c.Editable, result.Editable}, {"mentioned", c.Mentioned, result.Mentioned}}
		for _, check := range checks {
			totals[check.name]++
			fieldErr := err
			if e := result.Errors[check.name]; e != nil {
				fieldErr = e
			}
			var want any
			json.Unmarshal(check.want.OK, &want)
			got, _ := json.Marshal(check.got)
			var actual any
			json.Unmarshal(got, &actual)
			if check.want.Error != "" && fieldErr != nil || check.want.Error == "" && reflect.DeepEqual(actual, want) || (want == nil && check.got == "") {
				matched[check.name]++
				continue
			}
			errorText := ""
			if fieldErr != nil {
				errorText = fieldErr.Error()
			}
			failures = append(failures, mismatch{c.Name, check.name, actual, want, errorText})
		}
	}
	report, _ := json.MarshalIndent(failures, "", "  ")
	if len(failures) > 0 {
		if err = os.WriteFile(filepath.Join(t.TempDir(), "richtext-differences.json"), report, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for field, total := range totals {
		t.Logf("%s: %d/%d", field, matched[field], total)
	}
	if len(failures) > 0 {
		for _, m := range failures[:min(8, len(failures))] {
			t.Logf("%s / %s: got %v want %v (%s)", m.Name, m.Field, m.Got, m.Want, m.Error)
		}
		t.Fatal(fmt.Sprintf("%d rich-text differences; see richtext-differences.json", len(failures)))
	}
}
