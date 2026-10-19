package cable

import (
	"math/rand"
	"testing"
	"unsafe"
)

func TestFrameCacheChargesBothRepresentations(t *testing.T) {
	fc := newFrameCache()
	random := rand.New(rand.NewSource(42))
	for i := 0; i < 64; i++ {
		data := make([]byte, 16384)
		random.Read(data)
		fc.lookupOrCreate("scope", "id", data)
	}
	var retained int64
	for e := fc.first; e != nil; e = e.gNext {
		// Resolve deflation, then include both capacities, objects and key bytes.
		retained += int64(e.frame.RetainedSize()+len(e.key.scope)+len(e.key.id)) + int64(unsafe.Sizeof(*e))
	}
	if fc.bytes < retained {
		t.Fatalf("charged %d; representations retain at least %d", fc.bytes, retained)
	}
}
