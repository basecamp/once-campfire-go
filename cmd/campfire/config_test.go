package main

import "testing"

func TestResponseCacheBudget(t *testing.T) {
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "")
	if n, err := responseCacheBudget(); err != nil || n != 64<<20 {
		t.Fatal(n, err)
	}
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	if n, err := responseCacheBudget(); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "2048")
	if _, err := responseCacheBudget(); err == nil {
		t.Fatal("unbounded configuration accepted")
	}
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "1")
	if n, err := responseCacheBudget(); err != nil || n != 1<<20 {
		t.Fatal(n, err)
	}
}
