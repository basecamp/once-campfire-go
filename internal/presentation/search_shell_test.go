package presentation

import (
	"bytes"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
	"testing"
	"time"
)

func TestSearchShellPreservesBytesIdentityAndOwnership(t *testing.T) {
	app, user := shellTestFragments(t)
	base := shellFixture{
		User:              user,
		Screen:            "search",
		Title:             "Search",
		BodyClass:         "sidebar searches",
		Query:             "coffee & <tea>",
		SearchResultCount: 2,
		RecentSearches:    []string{"coffee", "<tea>"},
		ReturnRoom:        12,
		MessagesHTML:      "<div>one &amp; two</div>",
	}
	check := func(t *testing.T, p shellFixture) []responsebody.Part {
		t.Helper()
		var expected, actual bytes.Buffer
		if err := app.renderer.ExecuteTemplate(&expected, "search", p); err != nil {
			t.Fatal(err)
		}
		parts, err := app.SearchParts(p.search(), responsebody.NewPart([]byte(p.MessagesHTML)))
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			if _, err := part.WriteTo(&actual); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(actual.Bytes(), expected.Bytes()) {
			t.Fatal("search shell differs from full template")
		}
		return parts
	}
	first := check(t, base)
	check(t, base)
	t.Run("non-rendered user state reuses shell", func(t *testing.T) {
		entries, size := len(app.cache.entries), app.cache.bytes
		p := base
		p.User.Email = "changed@example.test"
		p.User.Password = "changed password digest"
		p.User.BotToken = "changed bot token"
		p.User.Status = 2
		check(t, p)
		if len(app.cache.entries) != entries || app.cache.bytes != size {
			t.Fatal("non-rendered user state retained another copy of unchanged shell HTML")
		}
	})
	changes := map[string]func(*shellFixture){
		"query":            func(p *shellFixture) { p.Query = "other <query>" },
		"count and body":   func(p *shellFixture) { p.SearchResultCount = 1; p.MessagesHTML = "<p>new result</p>" },
		"recents":          func(p *shellFixture) { p.RecentSearches = []string{"new", "<old>"} },
		"return room":      func(p *shellFixture) { p.ReturnRoom++ },
		"user":             func(p *shellFixture) { p.User.Name = "Other & user"; p.User.ID++; p.User.Role = 0 },
		"account":          func(p *shellFixture) { p.Account.HasLogo = true; p.Account.UpdatedAt = time.Unix(1700000000, 0) },
		"flash":            func(p *shellFixture) { p.Notice = "Saved & seen" },
		"error":            func(p *shellFixture) { p.Error = "Failed <again>" },
		"styles":           func(p *shellFixture) { p.CustomStyles = "<style>body{color:red}</style>" },
		"frame":            func(p *shellFixture) { p.Frame = true },
		"reload and vapid": func(p *shellFixture) { p.Reload = true; p.VAPIDPublicKey = "new-public-key" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) { p := base; change(&p); check(t, p); check(t, base) })
	}
	var want bytes.Buffer
	for _, part := range first {
		part.WriteTo(&want)
	}
	// Eviction releases only cache references, not already selected response Parts.
	app.cache.limit = 4096
	for i := 0; i < 50; i++ {
		app.cache.putEntry(
			fragmentEntry{key: fmt.Sprint(i), part: responsebody.NewPart(make([]byte, 512))},
		)
	}
	var retained bytes.Buffer
	for _, part := range first {
		part.WriteTo(&retained)
	}
	if !bytes.Equal(retained.Bytes(), want.Bytes()) {
		t.Fatal("eviction changed captured shell bytes")
	}
	for _, limit := range []int{0, 1, 32 << 20} {
		app.cache = newFragmentCache(limit)
		check(t, base)
		check(t, base)
		if limit <= 1 && app.cache.bytes != 0 {
			t.Fatal("disabled/oversized shell was retained")
		}
		if limit > 1 {
			for _, e := range app.cache.entries {
				entry := e.Value.(fragmentEntry)
				if entry.shell == nil ||
					entry.bytes != len(
						entry.key,
					)+240+entry.shell.bytes+32+56*len(
						entry.shell.parts,
					) {
					t.Fatal("shell payload must be charged once")
				}
			}
		}
	}
}
