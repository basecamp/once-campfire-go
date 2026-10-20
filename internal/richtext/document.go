package richtext

import (
	xhtml "github.com/basecamp/once-campfire-go/internal/html"
	"strings"
)

// Document owns parsed content. Its transformations clone the tree so prepared
// content can be reused without mutation. Malformed content retains the existing
// consumer-specific fallback; resolving external data happens before rendering.
type Document struct {
	body string
	root *xhtml.Node
	err  error
}

func Prepare(body string) Document {
	root, err := load(body)
	return Document{body: body, root: root, err: err}
}

// Attachables identifies distinct mention tokens without resolving them or
// generating unused representations. Malformed content has no display targets.
func (doc Document) Attachables() []string {
	if doc.err != nil {
		return nil
	}
	return attachmentTokens(doc.root, displayAttachments)
}

// MentionAttachables follows recipient extraction, which only walks the original
// attachment tree rather than expanded HTML-content attachments.
func (doc Document) MentionAttachables() []string {
	if doc.err != nil {
		return nil
	}
	return attachmentTokens(doc.root, recipientAttachments)
}

// EditorAttachables follows the editor's raw-HTML parser rather than canonical
// Trix loading. Foreign stored content can expose different tokens in each form.
func (doc Document) EditorAttachables() []string {
	root, err := parse(strings.Trim(doc.body, "\x00\t\n\v\f\r "))
	if err != nil {
		return nil
	}
	return attachmentTokens(root, editorAttachments)
}

// PlainAttachables follows text replacement, which does not resolve mentions
// inside serialized HTML content and skips OpenGraph attachables entirely.
func (doc Document) PlainAttachables() []string {
	if doc.err != nil {
		return nil
	}
	var tokens []string
	seen := map[string]bool{}
	walk(doc.root, func(n *xhtml.Node) {
		if n.Data != "action-text-attachment" || opengraphType.MatchString(attr(n, "content-type")) {
			return
		}
		if token := attr(n, "sgid"); token != "" && !seen[token] {
			seen[token] = true
			tokens = append(tokens, token)
		}
	})
	return tokens
}

type attachmentConsumer uint8

const (
	displayAttachments attachmentConsumer = iota
	recipientAttachments
	editorAttachments
)

func attachmentTokens(root *xhtml.Node, consumer attachmentConsumer) []string {
	var tokens []string
	seen := map[string]bool{}
	var collect func(*xhtml.Node, int)
	collect = func(root *xhtml.Node, depth int) {
		walk(root, func(n *xhtml.Node) {
			if n.Data != "action-text-attachment" {
				return
			}
			token := attr(n, "sgid")
			if token != "" && !seen[token] {
				seen[token] = true
				tokens = append(tokens, token)
			}
			if consumer != recipientAttachments && depth < 8 && strings.Contains(attr(n, "content-type"), "html") && strings.TrimSpace(attr(n, "content")) != "" {
				content, err := attr(n, "content"), error(nil)
				// The editor loads its first HTML-content layer before nested
				// replacement sanitizes it; display sanitizes every layer first.
				if consumer != editorAttachments || depth > 0 {
					content, err = sanitizedAttachmentContent(content)
				}
				if err == nil {
					if nested, err := load(content); err == nil {
						collect(nested, depth+1)
					}
				}
			}
		})
	}
	collect(root, 0)
	return tokens
}

func (doc Document) PlainText(ctx Context) (string, error) {
	if doc.err != nil {
		return "", doc.err
	}
	root := doc.root
	if hasAttachments(root) {
		root = clone(root)
		if err := replaceAttachments(root, ctx, true, 0); err != nil {
			return "", err
		}
	}
	return chomp(plain(root)), nil
}

func (doc Document) MentionIDs(ctx Context) ([]int64, error) {
	result, err := doc.process(ctx, mentionsOutput)
	if err == nil {
		err = result.Errors["mentioned"]
	}
	return result.Mentioned, err
}

func (doc Document) Editable(ctx Context) (string, error) { return editable(doc.body, ctx) }

// Content prepares API body HTML and text, not display/editor/recipient variants.
func (doc Document) Content(ctx Context) (Result, error) {
	result, err := doc.process(ctx, bodyOutput)
	if err != nil {
		return result, err
	}
	result.Plain, err = doc.PlainText(ctx)
	if err != nil {
		result.Errors["plain"] = err
	}
	return result, nil
}

func (doc Document) Display(ctx Context) (Result, error) {
	if doc.err == nil {
		doc.root = clone(doc.root)
	}
	return doc.process(ctx, displayOutput)
}
