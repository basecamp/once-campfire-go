package richtext

// ENGINE-56 corpus benchmarks: steady-state rich-text processing over the
// golden corpus categorized by message-body type. The oracle testdata doubles
// as the benchmark input so the measured work matches the byte-identical
// corpus. Keep raw output in tmp/ (repo convention: ignored artifacts).

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

type benchCase struct {
	name, body, host string
}

// benchCorpus is the golden corpus plus a resolution table, so attachment and
// mention bodies take the same resolve path the oracle test drives.
type benchCorpus struct {
	cases []benchCase
	users map[int64]*Mention
}

func loadBenchCorpus(tb testing.TB) *benchCorpus {
	tb.Helper()
	raw, err := os.ReadFile("testdata/rust.json.gz")
	if err != nil {
		tb.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		tb.Fatal(err)
	}
	defer reader.Close()
	unzipped, err := io.ReadAll(reader)
	if err != nil {
		tb.Fatal(err)
	}
	var corpus struct {
		Users []struct {
			ID          int64
			Name, Title string
			SGID        string `json:"attachable_sgid"`
			Path        string `json:"user_path"`
			Avatar      string `json:"avatar_path"`
		}
		Cases []struct {
			Name, Body, Host string
		}
	}
	if err := json.Unmarshal(unzipped, &corpus); err != nil {
		tb.Fatal(err)
	}
	bc := &benchCorpus{users: make(map[int64]*Mention, len(corpus.Users))}
	for _, u := range corpus.Users {
		bc.users[u.ID] = &Mention{u.ID, u.Name, u.Title, u.SGID, u.Path, u.Avatar}
	}
	bc.cases = make([]benchCase, 0, len(corpus.Cases))
	for _, c := range corpus.Cases {
		bc.cases = append(bc.cases, benchCase{c.Name, c.Body, c.Host})
	}
	return bc
}

// ctx builds a Context whose resolve returns corpus users from a
// prewarmed cache — the steady-state shape of the web layer's per-request
// mention cache (ENGINE-45b) after the first mention of each user, so the
// benchmark attributes cost to the parse path, not to the DB/verifier round
// trip that a cache miss would add once.
func (bc *benchCorpus) ctx(host string) Context {
	cache := make(map[int64]*Mention, len(bc.users))
	for id, m := range bc.users {
		cache[id] = m
	}
	return Context{Host: host, Resolve: func(sgid string, verified bool) (*Mention, error) {
		if verified {
			return nil, nil // benchmark bodies carry unverified sgids
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
		return cache[id], nil
	}}
}

func (bc *benchCorpus) category(category string) []benchCase {
	var out []benchCase
	for _, c := range bc.cases {
		if classifyBody(c.body) == category {
			out = append(out, c)
		}
	}
	return out
}

// classifyBody mirrors the benchmark report's message-body taxonomy.
func classifyBody(body string) string {
	switch {
	case strings.Contains(body, "action-text-attachment"):
		return "attachment"
	case containsEmoji(body):
		return "emoji"
	case containsAnyByte(body, '<', '&'):
		return "rich"
	default:
		return "plain"
	}
}

func containsAnyByte(s string, bytes ...byte) bool {
	for i := 0; i < len(s); i++ {
		for _, b := range bytes {
			if s[i] == b {
				return true
			}
		}
	}
	return false
}

func containsEmoji(s string) bool {
	for _, r := range s {
		if r >= 0x1F300 && r <= 0x1FAFF || r >= 0x2600 && r <= 0x27BF || r == 0xFE0F {
			return true
		}
	}
	return false
}

func benchProcess(b *testing.B, cases []benchCase, run func(*testing.B, benchCase)) {
	b.Helper()
	if len(cases) == 0 {
		b.Skip("no cases in category")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		run(b, cases[n%len(cases)])
	}
}

// benchIgnoreErr is the benchmark's run wrapper: the corpus contains bodies
// whose pipeline returns expected errors (the oracle pins those outcomes too),
// so benchmarks must not fail on them.
func benchIgnoreErr(err error) {
	_ = err
}

func BenchmarkProcessMessagePlain(b *testing.B) {
	bc := loadBenchCorpus(b)
	cases := bc.category("plain")
	benchProcess(b, cases, func(b *testing.B, c benchCase) {
		benchIgnoreErr(func() error { _, err := ProcessMessage(c.body, bc.ctx(c.host)); return err }())
	})
}

func BenchmarkProcessMessageRich(b *testing.B) {
	bc := loadBenchCorpus(b)
	cases := bc.category("rich")
	benchProcess(b, cases, func(b *testing.B, c benchCase) {
		benchIgnoreErr(func() error { _, err := ProcessMessage(c.body, bc.ctx(c.host)); return err }())
	})
}

func BenchmarkProcessMessageEmoji(b *testing.B) {
	bc := loadBenchCorpus(b)
	cases := bc.category("emoji")
	benchProcess(b, cases, func(b *testing.B, c benchCase) {
		benchIgnoreErr(func() error { _, err := ProcessMessage(c.body, bc.ctx(c.host)); return err }())
	})
}

func BenchmarkProcessMessageAttachment(b *testing.B) {
	bc := loadBenchCorpus(b)
	cases := bc.category("attachment")
	benchProcess(b, cases, func(b *testing.B, c benchCase) {
		benchIgnoreErr(func() error { _, err := ProcessMessage(c.body, bc.ctx(c.host)); return err }())
	})
}

func BenchmarkProcessMessageAll(b *testing.B) {
	bc := loadBenchCorpus(b)
	benchProcess(b, bc.cases, func(b *testing.B, c benchCase) {
		benchIgnoreErr(func() error { _, err := ProcessMessage(c.body, bc.ctx(c.host)); return err }())
	})
}

func BenchmarkProcessAll(b *testing.B) {
	bc := loadBenchCorpus(b)
	benchProcess(b, bc.cases, func(b *testing.B, c benchCase) {
		benchIgnoreErr(func() error { _, err := Process(c.body, bc.ctx(c.host)); return err }())
	})
}

func BenchmarkPlainTextAll(b *testing.B) {
	bc := loadBenchCorpus(b)
	benchProcess(b, bc.cases, func(b *testing.B, c benchCase) {
		benchIgnoreErr(func() error { _, err := PlainText(c.body, bc.ctx(c.host)); return err }())
	})
}

func BenchmarkEditableAll(b *testing.B) {
	bc := loadBenchCorpus(b)
	benchProcess(b, bc.cases, func(b *testing.B, c benchCase) {
		benchIgnoreErr(func() error { _, err := Editable(c.body, bc.ctx(c.host)); return err }())
	})
}
