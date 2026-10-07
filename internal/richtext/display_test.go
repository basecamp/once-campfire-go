package richtext

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestDisplayKeepsContextAndFailureSemantics(t *testing.T) {
	body := `<div><action-text-attachment sgid="bad"></action-text-attachment><action-text-attachment sgid="valid"></action-text-attachment></div>`
	for _, fail := range []bool{false, true} {
		for _, name := range []string{"First & user", "Changed user"} {
			verified := 0
			ctx := Context{Resolve: func(sgid string, verify bool) (*Mention, error) {
				if verify {
					verified++
				}
				if sgid == "bad" {
					if fail && !verify {
						return nil, errors.New("resolver failed")
					}
					return nil, nil
				}
				return &Mention{ID: 7, Name: name, SGID: "valid", Path: "/users/7", Avatar: "/avatar/7"}, nil
			}}
			full, err := Process(body, ctx)
			if err != nil || !reflect.DeepEqual(full.Mentioned, []int64{7}) {
				t.Fatalf("verified recipients lost after rendering/failure: %+v, %v", full, err)
			}
			verified = 0
			display, err := Display(body, ctx)
			if err != nil || verified != 0 || display.Plain != full.Plain || display.Filtered != full.Filtered || display.Presentation != full.Presentation {
				t.Fatalf("focused rendering or resolution changed: %+v, %v", display, err)
			}
			for _, field := range []string{"plain", "filtered"} {
				if fmt.Sprint(display.Errors[field]) != fmt.Sprint(full.Errors[field]) {
					t.Fatalf("focused %s failure changed", field)
				}
			}
			if !fail && display.Plain != "@"+name {
				t.Fatalf("stale user context: %q", display.Plain)
			}
		}
	}
	// Filtering removes this entire foreign subtree, but recipient extraction
	// must still use the loaded attachment tree, not the display-owned tree.
	body = `<svg><foreignObject><action-text-attachment sgid="valid"></action-text-attachment></foreignObject></svg>`
	ctx := Context{Resolve: func(string, bool) (*Mention, error) {
		return &Mention{ID: 7, Name: "User", SGID: "valid"}, nil
	}}
	full, err := Process(body, ctx)
	if err != nil || full.Filtered != "" || !reflect.DeepEqual(full.Mentioned, []int64{7}) {
		t.Fatalf("filtered subtree lost original recipients: %+v, %v", full, err)
	}
	display, err := Display(body, ctx)
	if err != nil || display.Filtered != full.Filtered || display.Presentation != full.Presentation || display.Plain != full.Plain {
		t.Fatalf("foreign subtree display differs: %+v, %v", display, err)
	}
	body = `<action-text-attachment content-type="application/vnd.actiontext.opengraph-embed" href="https://same.example/story" filename="Story"></action-text-attachment>`
	for _, host := range []string{"same.example", "other.example", "same.example"} {
		display, err := Display(body, Context{Host: host})
		if err != nil || strings.Contains(display.Presentation, `href="https://same.example/story"`) != (host != "same.example") {
			t.Fatalf("stale host context %q: %q, %v", host, display.Presentation, err)
		}
	}
}
