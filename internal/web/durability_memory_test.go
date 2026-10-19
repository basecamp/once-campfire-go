package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"unsafe"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestReleaseResponseDropsBodies(t *testing.T) {
	b := &responseBuffer{body: bytes.NewBuffer(make([]byte, 2<<20)), encoded: make([]byte, 128)}
	for i := range b.partsBuf {
		b.partsBuf[i] = make([]byte, 64)
	}
	b.parts = b.partsBuf[:]
	b.clearForPool()
	if b.body != nil || b.parts != nil || b.encoded != nil || b.assembly != nil {
		t.Error("released response retains body storage")
	}
	for i, p := range b.partsBuf {
		if p != nil {
			t.Errorf("released part %d retains storage", i)
		}
	}
}

func TestReferenceCacheChargesRetainedRows(t *testing.T) {
	c := newMessageRefsCache(64 << 10)
	for i := int64(0); i < 100; i++ {
		c.store(messageRefsKey{room: i}, make([]database.Message, 40), messageValidator{etag: "tag"})
	}
	retained := 0
	for _, e := range c.entries {
		entry := e.Value.(messageRefsEntry)
		cost := 240 + cap(entry.refs)*int(unsafe.Sizeof(database.Message{})) + len(entry.etag)
		if entry.bytes < cost {
			t.Fatalf("charged %d; retained at least %d", entry.bytes, cost)
		}
		retained += cost
	}
	if retained > c.limit {
		t.Fatalf("retained %d exceeds budget %d", retained, c.limit)
	}
}

func TestSidebarGatesBounded(t *testing.T) {
	s := &Server{fragments: newFragmentCache(4096)}
	name := strings.Repeat("large sidebar", 512)
	for i := int64(1); i <= 10000; i++ {
		p := page{User: database.User{ID: i, Name: name}, gateVersion: 1, gateGeneration: 1}
		key := sidebarCacheKey(p)
		s.fragments.put(key, template.HTML("frame"))
		s.recordSidebarGate(p, key)
	}
	count, retained := 0, 0
	s.sidebarGates.Range(func(_, value any) bool { count++; retained += len(value.(servedGate).frameKey); return true })
	if count > 4096 {
		t.Fatalf("retained %d gates", count)
	}
	if retained > 4096*72 {
		t.Fatalf("retained %d key bytes", retained)
	}
	if _, ok := s.sidebarGate(1); ok {
		t.Fatal("colliding user received another user's gate")
	}
}
