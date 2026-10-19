package fastdb

import (
	"strings"
	"testing"
)

// benchRefs keeps the measured slice reachable after the loop.
var benchRefs []MessageRef

// benchSearch keeps the measured search result reachable after the loop.
var benchSearch []Message

// BenchmarkSearch measures the ENGINE-30 FTS scan: the membership-scoped
// MATCH query, positional decode into caller-owned space. Allocations per op
// are the query normalization and MATCH text (request state, not per-record).
func BenchmarkSearch(b *testing.B) {
	path := fixtureDB(b)
	c, err := OpenReadOnly(path, 256)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()

	users, err := c.searchBenchmarkUsers()
	if err != nil {
		b.Fatal(err)
	}
	word := c.searchBenchmarkWord(b)
	if word == "" {
		b.Skip("fixture has no indexed message words")
	}
	dst := make([]Message, 0, 100)
	matched := false
	var user int64
	for _, candidate := range users {
		if out, err := c.Search(dst[:0], candidate, word); err != nil {
			b.Fatal(err)
		} else if len(out) > 0 {
			dst = out[:0]
			user = candidate
			matched = true
			break
		}
	}
	if !matched {
		b.Skipf("fixture has no %q hits for any membership user", word)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := c.Search(dst[:0], user, word)
		if err != nil {
			b.Fatal(err)
		}
		dst = out[:0]
	}
	b.StopTimer()
	benchSearch = dst
}

// searchBenchmarkUsers returns every membership user in id order.
func (c *Conn) searchBenchmarkUsers() ([]int64, error) {
	st, err := c.db.Prepare("SELECT DISTINCT user_id FROM memberships ORDER BY user_id")
	if err != nil {
		return nil, err
	}
	defer st.Finalize()
	var users []int64
	for {
		if row, err := st.Step(); err != nil {
			return nil, err
		} else if !row {
			break
		}
		users = append(users, st.ColumnInt64(0))
	}
	return users, nil
}

// searchBenchmarkWord returns one word from the fixture's own FTS index
// bodies, or "" when the index is empty.
func (c *Conn) searchBenchmarkWord(b *testing.B) string {
	b.Helper()
	st, err := c.db.Prepare("SELECT body FROM message_search_index LIMIT 8")
	if err != nil {
		b.Fatal(err)
	}
	defer st.Finalize()
	for {
		if row, err := st.Step(); err != nil {
			b.Fatal(err)
		} else if !row {
			break
		}
		body := string(st.ColumnBytes(0))
		for _, word := range strings.Fields(body) {
			word = strings.Trim(word, "\"'.,!?()[]{}")
			if word != "" {
				return word
			}
		}
	}
	return ""
}

// BenchmarkMessageRefs measures the reduced 40-row reference scan with a
// caller-owned destination. Contract: 0 allocs/op in both directions.
func BenchmarkMessageRefs(b *testing.B) {
	path := fixtureDB(b)
	c, err := OpenReadOnly(path, 256)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()

	room, middle := refBenchmarkAnchors(b, c)
	dst := make([]MessageRef, 0, 40)
	if out, err := c.MessageRefs(dst, room, 0); err != nil {
		b.Fatal(err)
	} else if len(out) != 40 {
		b.Fatalf("fixture contract: room %d returned %d refs, want 40", room, len(out))
	}

	b.Run("latest", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, err := c.MessageRefs(dst[:0], room, 0)
			if err != nil {
				b.Fatal(err)
			}
			if len(out) != 40 {
				b.Fatalf("rows = %d, want 40", len(out))
			}
			dst = out[:0]
		}
	})
	b.Run("before", func(b *testing.B) {
		dst = dst[:0]
		if out, err := c.MessageRefs(dst, room, middle); err != nil {
			b.Fatal(err)
		} else if len(out) != 40 {
			b.Fatalf("fixture contract: anchor %d returned %d refs, want 40", middle, len(out))
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, err := c.MessageRefs(dst[:0], room, middle)
			if err != nil {
				b.Fatal(err)
			}
			if len(out) != 40 {
				b.Fatalf("rows = %d, want 40", len(out))
			}
			dst = out[:0]
		}
	})
	b.StopTimer()
	benchRefs = dst
}

// refBenchmarkAnchors returns the room with the most messages and the id of
// its 41st-oldest message, so both the unfiltered and the before-anchor scans
// yield exactly 40 rows.
func refBenchmarkAnchors(b *testing.B, c *Conn) (room, anchor int64) {
	b.Helper()
	st, err := c.db.Prepare("SELECT room_id,count(*) FROM messages GROUP BY room_id ORDER BY count(*) DESC, room_id LIMIT 1")
	if err != nil {
		b.Fatal(err)
	}
	if row, err := st.Step(); err != nil || !row {
		b.Fatalf("room lookup: row=%v err=%v", row, err)
	}
	room = st.ColumnInt64(0)
	count := st.ColumnInt64(1)
	st.Reset()
	st.Finalize()
	if count < 40 {
		b.Fatalf("fixture contract: best room has %d messages, want >= 40", count)
	}

	st, err = c.db.Prepare("SELECT id FROM messages WHERE room_id=? ORDER BY created_at,id LIMIT 1 OFFSET 40")
	if err != nil {
		b.Fatal(err)
	}
	defer st.Finalize()
	if err := st.BindInt64(1, room); err != nil {
		b.Fatal(err)
	}
	if row, err := st.Step(); err != nil || !row {
		b.Fatalf("anchor lookup: row=%v err=%v", row, err)
	}
	return room, st.ColumnInt64(0)
}
