package presentation

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

type SearchInput struct {
	LayoutInput
	Query             string
	SearchResultCount int
	RecentSearches    []string
	ReturnRoom        int64
}

func (f *Fragments) SearchParts(p SearchInput, messages responsebody.Part) ([]responsebody.Part, error) {
	// As with room shells, this exact input drives identity and execution.
	// Matching references, authorization, recent searches and return room have
	// already been read afresh; none of those database observations are cached.
	input := struct {
		layoutShellPage
		Query             string
		SearchResultCount int
		RecentSearches    []string
		ReturnRoom        int64
	}{shellPage(p.LayoutInput), p.Query, p.SearchResultCount, p.RecentSearches, p.ReturnRoom}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("search-shell/%x", sha256.Sum256(raw))
	entry, ok := f.cache.entry(key)
	if !ok {
		marker := "\x00campfire-" + rand.Text() + "\x00"
		input.MessagesHTML = template.HTML(marker)
		b := new(bytes.Buffer)
		if err := f.renderer.ExecuteTemplate(b, "search", input); err != nil {
			return nil, err
		}
		rendered := b.String()
		before, after, found := strings.Cut(rendered, marker)
		if !found || strings.Count(rendered, marker) != 1 {
			return nil, fmt.Errorf("search template must contain one message insertion point")
		}
		entry = f.cache.putEntry(fragmentEntry{key: key, shell: &templateShell{
			parts: []responsebody.Part{responsebody.NewPart([]byte(before)), responsebody.NewPart([]byte(after))},
			bytes: len(before) + len(after),
		}})
	}
	return []responsebody.Part{entry.shell.parts[0], messages, entry.shell.parts[1]}, nil
}
