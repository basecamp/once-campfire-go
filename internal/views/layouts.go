package views

import qt "github.com/valyala/quicktemplate"

// Page is what a page template hands the application layout (reference/app/views/layouts): the
// reference's layouts::Page (@page_title, @body_class) and its askama blocks, the content_for
// regions head, nav, footer and sidebar, and the page itself (content). A page template's blocks
// are methods on its page type, generated from `{% func (p *X) Content() %}`; embedding PageBase
// gives a page the layout's empty blocks.
type Page interface {
	PageTitle() *string
	BodyClass() *string
	StreamHead(qw *qt.Writer)
	StreamNav(qw *qt.Writer)
	StreamContent(qw *qt.Writer)
	StreamFooter(qw *qt.Writer)
	StreamSidebar(qw *qt.Writer)
}

// PageBase is a page with no title, body class or content_for regions.
type PageBase struct{}

func (PageBase) PageTitle() *string       { return nil }
func (PageBase) BodyClass() *string       { return nil }
func (PageBase) StreamHead(*qt.Writer)    {}
func (PageBase) StreamNav(*qt.Writer)     {}
func (PageBase) StreamContent(*qt.Writer) {}
func (PageBase) StreamFooter(*qt.Writer)  {}
func (PageBase) StreamSidebar(*qt.Writer) {}

// LayoutsApplicationWrapper is the application layout around page parts rendered elsewhere, for
// templates that don't extend the layout themselves (the reference's layouts::Application).
type LayoutsApplicationWrapper struct {
	Title, Class                                            *string
	HeadHTML, NavHTML, ContentHTML, FooterHTML, SidebarHTML HTML
}

// ApplicationContent is just a page body in the application layout.
func ApplicationContent(content HTML) *LayoutsApplicationWrapper {
	return &LayoutsApplicationWrapper{ContentHTML: content}
}

func (p *LayoutsApplicationWrapper) PageTitle() *string { return p.Title }
func (p *LayoutsApplicationWrapper) BodyClass() *string { return p.Class }

// Ptr is a pointer to v, for the optional values page types and helpers take.
func Ptr[T any](v T) *T { return &v }
