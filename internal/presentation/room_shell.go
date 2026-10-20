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

// Authorization and page data are read afresh. Only static surrounding bytes
// are retained; cursor and message parts are inserted independently per request.
func (f *Fragments) RoomParts(p LayoutInput, loadedAt string, messages responsebody.Part) ([]responsebody.Part, error) {
	input := shellPage(p)
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("room-shell/%x", sha256.Sum256(raw))
	entry, ok := f.cache.entry(key)
	if !ok {
		messageMarker := "\x00campfire-" + rand.Text() + "\x00"
		loadedMarker := "campfire-loaded-" + rand.Text()
		input.MessagesHTML, input.LoadedAt = template.HTML(messageMarker), loadedMarker
		b := new(bytes.Buffer)
		if err := f.renderer.ExecuteTemplate(b, "room", input); err != nil {
			return nil, err
		}
		rendered := b.String()
		// The room controller's refresh cursor precedes its message list. Check
		// that template contract at preparation time, not on every cache hit.
		beforeTime, remainder, found := strings.Cut(rendered, loadedMarker)
		if !found || strings.Count(rendered, loadedMarker) != 1 || strings.Count(rendered, messageMarker) != 1 {
			return nil, fmt.Errorf("room template must contain one cursor and one message insertion point")
		}
		beforeMessages, afterMessages, found := strings.Cut(remainder, messageMarker)
		if !found {
			return nil, fmt.Errorf("room template cursor must precede messages")
		}
		shell := &templateShell{
			parts: []responsebody.Part{
				responsebody.NewPart([]byte(beforeTime)),
				responsebody.NewPart([]byte(beforeMessages)),
				responsebody.NewPart([]byte(afterMessages)),
			},
			bytes: len(beforeTime) + len(beforeMessages) + len(afterMessages),
		}
		entry = f.cache.putEntry(fragmentEntry{key: key, shell: shell})
	}
	shell := entry.shell
	return []responsebody.Part{shell.parts[0], responsebody.NewPart([]byte(loadedAt)), shell.parts[1], messages, shell.parts[2]}, nil
}
