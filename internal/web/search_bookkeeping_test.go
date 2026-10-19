package web

import "testing"

func TestSearchCacheEvictsUserBookkeeping(t *testing.T) {
	c := newSearchResultCache(4096)
	for user := int64(1); user <= 1000; user++ {
		c.put(user, "q", 1, 1, 1, searchResult{})
	}
	if len(c.byUser) > len(c.entries) {
		t.Fatalf("%d users retained for %d entries", len(c.byUser), len(c.entries))
	}
}
