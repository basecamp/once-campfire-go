package web

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// Key every value the sidebar template reads. Authorization and membership data
// are still read afresh before looking up the rendered fragment.
func sidebarCacheKey(p page) string {
	raw, _ := json.Marshal(p)
	return fmt.Sprintf("sidebar/%x", sha256.Sum256(raw))
}
