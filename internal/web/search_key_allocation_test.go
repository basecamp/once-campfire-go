package web

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestSearchCacheLongKeyAllocations(t *testing.T) {
	query := strings.Repeat("long query ", 1024)
	var digest [32]byte
	allocs := testing.AllocsPerRun(10, func() { digest = searchCacheKey(1, query, 2, 3, 4) })
	if allocs != 0 {
		t.Fatalf("long key allocates: %g", allocs)
	}
	want := sha256.Sum256([]byte(fmt.Sprintf("1,2,3,4,%d:%s", len(query), query)))
	if digest != want {
		t.Fatal("incremental digest differs from complete key")
	}
	if digest == searchCacheKey(1, query+"x", 2, 3, 4) {
		t.Fatal("query suffix ignored")
	}
}
