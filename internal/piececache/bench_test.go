package piececache

import (
	"math/rand"
	"testing"
)

// The sinks keep the compiler from eliminating the measured work.
var (
	benchSinkEntry *Entry
	benchSinkBytes []byte
)

func benchRaw(size int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(rng.Intn(26) + 'a')
	}
	return buf
}

// BenchmarkGet measures warm hits over a rotating key set, so every iteration
// also churns the LRU order: map lookup, MoveToFront, immutable view.
// Contract: 0 allocs/op.
func BenchmarkGet(b *testing.B) {
	cache := New(64 << 20)
	keys := []string{
		"room/1/shell/v7",
		"room/1/messages/v42",
		"room/1/tail/v7",
		"room/2/shell/v3",
		"room/2/messages/v9",
	}
	for i, key := range keys {
		raw := benchRaw(16<<10, int64(i+1))
		if _, ok := cache.Put(key, raw, mustFragment(b, raw)); !ok {
			b.Fatalf("Put(%s) rejected", key)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSinkEntry = cache.Get(keys[i%len(keys)])
	}
	if benchSinkEntry == nil {
		b.Fatal("Get missed")
	}
}

// BenchmarkAssemble measures a 3-piece page (~80 KB of raw content) assembled
// into a caller-owned response buffer. Contract: 0 allocs/op.
func BenchmarkAssemble(b *testing.B) {
	cache := New(64 << 20)
	keys := []string{"room/1/shell/v7", "room/1/messages/v42", "room/1/tail/v7"}
	pieces := make([]*Entry, 3)
	for i, size := range []int{30 << 10, 28 << 10, 22 << 10} {
		raw := benchRaw(size, int64(i+1))
		entry, ok := cache.Put(keys[i], raw, mustFragment(b, raw))
		if !ok {
			b.Fatalf("Put(%s) rejected", keys[i])
		}
		pieces[i] = entry
	}

	for _, encoding := range []Encoding{Gzip, Identity} {
		b.Run(encoding.String(), func(b *testing.B) {
			buf := make([]byte, 0, 256<<10)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf = buf[:0]
				out, _, err := Assemble(buf, encoding, pieces...)
				if err != nil {
					b.Fatal(err)
				}
				buf = out[:0]
			}
			benchSinkBytes = buf
		})
	}
}
