package fastdb

import "testing"

// benchRefs keeps the measured slice reachable after the loop.
var benchRefs []MessageRef

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
