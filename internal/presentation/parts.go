package presentation

import (
	"github.com/basecamp/once-campfire-go/internal/responsebody"
	"html/template"
)

func FragmentList(fragments []template.HTML) responsebody.Part {
	size := 0
	for _, fragment := range fragments {
		size += len(fragment)
	}
	body := make([]byte, size)
	offset := 0
	for _, fragment := range fragments {
		offset += copy(body[offset:], fragment)
	}
	// The Part owns unpooled bytes through eviction and outstanding responses.
	return responsebody.NewPart(body)
}
