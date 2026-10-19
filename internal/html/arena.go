package html

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unsafe"

	at "golang.org/x/net/html/atom"
)

// Arena pools every per-parse allocation — node records, attribute records and
// byte storage — in slabs that are rewound between parses instead of being
// freed, so repeated parses of similar documents allocate nothing once the
// slabs have reached their working size.
//
// Thread contract: an Arena is owned by one goroutine at a time. Handing it
// between goroutines without synchronization is a race.
//
// Lifetime contract: every Node, []Attribute and string an Arena grants is
// valid until the next Reset. The caller must copy (strings.Clone) any value
// that outlives the parse — that is the documented rule the richtext layer
// follows for its Result fields and public return values.
//
// Node slab: nodes are individually addressable (the tree links are
// pointers), so the record slab is a chunk list — once a chunk is handed out
// it is appended to, never moved, so node pointers stay valid across slab
// growth. Attribute and byte storage are single growing slabs whose
// consumers hold slice/string headers, not interior pointers, so a
// reallocation only affects future allocations.
type Arena struct {
	nodeChunks [][]Node // chunks of Node records, grown geometrically
	nodeI      int      // next free slot within the last chunk
	attr       []Attribute
	byteSlab   []byte
	runes      []rune // transient rune slices (autoLink trimming)
	ints       []int  // transient int slices (autoLink tag index)
	pairs      [][2]int
	ptrs       []*Node
	parser     parser    // recycled parser state
	tok        Tokenizer // recycled tokenizer state
}

// NewArena returns an arena with no slabs allocated.
func NewArena() *Arena {
	return &Arena{}
}

const arenaNodeChunk = 128

// AllocNode allocates a Node record from the slab.
func (a *Arena) AllocNode(t NodeType, dataAtom at.Atom, data, namespace string, attr []Attribute) *Node {
	if len(a.nodeChunks) == 0 || a.nodeI == len(a.nodeChunks[len(a.nodeChunks)-1]) {
		n := arenaNodeChunk
		if l := len(a.nodeChunks); l > 0 {
			n = 2 * len(a.nodeChunks[l-1])
		}
		a.nodeChunks = append(a.nodeChunks, make([]Node, n))
		a.nodeI = 0
	}
	chunk := a.nodeChunks[len(a.nodeChunks)-1]
	out := &chunk[a.nodeI]
	a.nodeI++
	*out = Node{Type: t, DataAtom: dataAtom, Data: data, Namespace: namespace, Attr: attr}
	return out
}

// CloneNode is the arena form of Node.clone: same type, data, atom,
// namespace and a copy of n's attributes, no parent, no siblings, no
// children.
func (a *Arena) CloneNode(n *Node) *Node {
	return a.AllocNode(n.Type, n.DataAtom, n.Data, n.Namespace, a.AttrView(n.Attr))
}

// AllocNodeAttr allocates a node whose attributes are a slab copy of attrs.
func (a *Arena) AllocNodeAttr(t NodeType, dataAtom at.Atom, data, namespace string, attrs []Attribute) *Node {
	return a.AllocNode(t, dataAtom, data, namespace, a.AttrView(attrs))
}

// AttrView copies attrs into the slab and returns an exact-capacity view.
func (a *Arena) AttrView(attrs []Attribute) []Attribute {
	if len(attrs) == 0 {
		return nil
	}
	out := a.AllocAttrs(len(attrs))
	copy(out, attrs)
	return out
}

// AllocAttrs allocates n attribute records with no spare capacity.
func (a *Arena) AllocAttrs(n int) []Attribute {
	if n == 0 {
		return nil
	}
	if len(a.attr)+n > cap(a.attr) {
		a.attr = growAttrSlab(a.attr, n)
	}
	start := len(a.attr)
	a.attr = a.attr[:start+n]
	return a.attr[start : start+n : start+n]
}

func growAttrSlab(attr []Attribute, need int) []Attribute {
	n := 2 * cap(attr)
	if n < len(attr)+need {
		n = len(attr) + need
	}
	out := make([]Attribute, len(attr), n)
	copy(out, attr)
	return out
}

// AttrAppend appends one attribute to an existing (slab-backed) attribute
// view, returning a new exact-capacity view.
func (a *Arena) AttrAppend(attrs []Attribute, x Attribute) []Attribute {
	out := a.AllocAttrs(len(attrs) + 1)
	copy(out, attrs)
	out[len(attrs)] = x
	return out
}

// Bytes allocates n slab bytes (len == n == cap).
func (a *Arena) Bytes(n int) []byte {
	if len(a.byteSlab)+n > cap(a.byteSlab) {
		a.byteSlab = growByteSlab(a.byteSlab, n)
	}
	start := len(a.byteSlab)
	a.byteSlab = a.byteSlab[:start+n]
	return a.byteSlab[start : start+n : start+n]
}

// BytesCap allocates a slab region with len 0 and capacity at least n, for
// builders that append.
func (a *Arena) BytesCap(n int) []byte {
	if cap(a.byteSlab)-len(a.byteSlab) < n {
		a.byteSlab = growByteSlab(a.byteSlab, n)
	}
	start := len(a.byteSlab)
	a.byteSlab = a.byteSlab[:start+n]
	return a.byteSlab[start : start : start+n]
}

func growByteSlab(slab []byte, need int) []byte {
	c := 2 * cap(slab)
	if c < len(slab)+need {
		c = len(slab) + need
	}
	out := make([]byte, len(slab), c)
	copy(out, slab)
	return out
}

// Dup copies b into the slab.
func (a *Arena) Dup(b []byte) []byte {
	out := a.Bytes(len(b))
	copy(out, b)
	return out
}

// View returns a string over the slab bytes b. The caller guarantees the
// region is not written again for the lifetime of the returned string — the
// arena grants regions exactly once per Reset.
func (a *Arena) View(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// Concat appends x and y into the slab and views the result.
func (a *Arena) Concat(x, y string) string {
	if len(x)+len(y) == 0 {
		return ""
	}
	b := a.Bytes(len(x) + len(y))
	copy(b, x)
	copy(b[len(x):], y)
	return a.View(b)
}

// Concat3 appends x, y and z into the slab and views the result.
func (a *Arena) Concat3(x, y, z string) string {
	n := len(x) + len(y) + len(z)
	if n == 0 {
		return ""
	}
	b := a.Bytes(n)
	copy(b, x)
	copy(b[len(x):], y)
	copy(b[len(x)+len(y):], z)
	return a.View(b)
}

// Grow returns a slab view of b with capacity for at least need more bytes,
// copying b into a fresh region when its current region cannot take them.
// The fresh region carries geometric headroom so byte-at-a-time writers stay
// linear. Old regions stay reserved (bump allocator), which costs slab space
// but keeps the steady-state allocation count at zero.
func (a *Arena) Grow(b []byte, need int) []byte {
	if cap(b)-len(b) >= need {
		return b
	}
	n := 2 * (len(b) + need)
	if floor := len(b) + need + 64; n < floor {
		n = floor
	}
	out := a.BytesCap(n)
	out = append(out, b...)
	return out
}

// ReplaceAll returns a slab view of s with all old replaced by new. The
// caller must have checked that old occurs in s — the no-match fast path
// returns the input region unchanged, which is only sound when the caller
// knows old is absent (or when the input region outlives the use, as it does
// inside a single parse).
func (a *Arena) ReplaceAll(s, old, new string) string {
	if len(old) == 0 || !strings.Contains(s, old) {
		return s
	}
	var w Builder
	w.A = a
	last := 0
	for {
		i := strings.Index(s[last:], old)
		if i < 0 {
			break
		}
		w.WriteString(s[last : last+i])
		w.WriteString(new)
		last += i + len(old)
	}
	w.WriteString(s[last:])
	return w.String()
}

// PtrSlice allocates n *Node records from the pointer slab (transient
// result lists).
func (a *Arena) PtrSlice(n int) []*Node {
	if n == 0 {
		return nil
	}
	if len(a.ptrs)+n > cap(a.ptrs) {
		c := 2 * cap(a.ptrs)
		if c < n {
			c = n
		}
		buf := make([]*Node, len(a.ptrs), c)
		copy(buf, a.ptrs)
		a.ptrs = buf
	}
	start := len(a.ptrs)
	a.ptrs = a.ptrs[:start+n]
	return a.ptrs[start : start+n : start+n]
}

// RunesCap allocates a rune-slab region with len 0 and capacity n for
// transient rune slices. Growth reallocates the slab; consumers hold slice
// headers, and granted views keep the old backing alive through them.
func (a *Arena) RunesCap(n int) []rune {
	if n == 0 {
		return nil
	}
	if cap(a.runes)-len(a.runes) < n {
		c := 2 * cap(a.runes)
		if c < len(a.runes)+n {
			c = len(a.runes) + n
		}
		buf := make([]rune, len(a.runes), c)
		copy(buf, a.runes)
		a.runes = buf
	}
	start := len(a.runes)
	a.runes = a.runes[:start+n]
	return a.runes[start : start : start+n]
}

// IntsCap allocates an int-slab region with len 0 and capacity n.
func (a *Arena) IntsCap(n int) []int {
	if n == 0 {
		return nil
	}
	return a.intView(n)
}

// PairsCap allocates a [2]int-slab region with len 0 and capacity n.
func (a *Arena) PairsCap(n int) [][2]int {
	if n == 0 {
		return nil
	}
	if cap(a.pairs)-len(a.pairs) < n {
		c := 2 * cap(a.pairs)
		if c < len(a.pairs)+n {
			c = len(a.pairs) + n
		}
		buf := make([][2]int, len(a.pairs), c)
		copy(buf, a.pairs)
		a.pairs = buf
	}
	start := len(a.pairs)
	a.pairs = a.pairs[:start+n]
	return a.pairs[start : start : start+n]
}

func (a *Arena) intView(n int) []int {
	if cap(a.ints)-len(a.ints) < n {
		c := 2 * cap(a.ints)
		if c < len(a.ints)+n {
			c = len(a.ints) + n
		}
		buf := make([]int, len(a.ints), c)
		copy(buf, a.ints)
		a.ints = buf
	}
	start := len(a.ints)
	a.ints = a.ints[:start+n]
	return a.ints[start : start : start+n]
}

// Reset rewinds every slab: nodes, attributes, pointers and bytes granted
// before the call become invalid. The parser and tokenizer state is reset by
// the next ParseFragment call, which re-initializes every field.
func (a *Arena) Reset() {
	a.nodeI = 0
	a.attr = a.attr[:0]
	a.byteSlab = a.byteSlab[:0]
	a.runes = a.runes[:0]
	a.ints = a.ints[:0]
	a.pairs = a.pairs[:0]
	a.ptrs = a.ptrs[:0]
}

// ParseFragment parses an HTML fragment exactly as ParseFragmentWithOptions
// does — same tokenizer, same tree-construction algorithm, same errors — but
// every node, attribute and string lives in the arena. The body is copied
// into the slab once; the tokenizer works on that copy in place, exactly as
// it works on its internal buffer in the reader path.
func (a *Arena) ParseFragment(body string, context *Node, opts ...ParseOption) ([]*Node, error) {
	body = strings.TrimPrefix(body, "\ufeff")
	p := &a.parser
	// Field-by-field re-initialization: oe, afe and templateStack keep their
	// backings across parses so steady-state reuse reallocates nothing. The
	// zero values below match a freshly allocated parser.
	p.fragment = true
	p.scripting = true
	p.context = context
	p.a = a
	p.tok = Token{}
	p.hasSelfClosingToken = false
	p.doc = p.newNode(DocumentNode, 0, "", "", nil)
	p.oe = p.oe[:0]
	p.afe = p.afe[:0]
	p.head, p.form = nil, nil
	p.framesetOK = false
	p.templateStack = p.templateStack[:0]
	p.im, p.originalIM = nil, nil
	p.fosterParenting = false
	p.quirks = false

	contextTag := ""
	foreign := false
	if context != nil {
		if context.Type != ElementNode {
			return nil, errors.New("html: ParseFragment of non-element Node")
		}
		if context.DataAtom != at.Lookup([]byte(context.Data)) {
			return nil, fmt.Errorf("html: inconsistent Node: DataAtom=%q, Data=%q", context.DataAtom, context.Data)
		}
		contextTag = context.DataAtom.String()
		foreign = context.Namespace != ""
	}

	a.tok.resetArena(a, body, contextTag, foreign)
	p.tokenizer = &a.tok

	for _, f := range opts {
		f(p)
	}

	root := p.newNode(ElementNode, at.Html, at.Html.String(), "", nil)
	p.doc.AppendChild(root)
	p.oe = append(p.oe, root)
	if context != nil && context.DataAtom == at.Template {
		p.templateStack = append(p.templateStack, inTemplateIM)
	}
	p.resetInsertionMode()
	if !p.scripting && contextTag == "noscript" {
		p.tokenizer.NextIsNotRawText()
	}

	for n := context; n != nil; n = n.Parent {
		if n.Type == ElementNode && n.DataAtom == at.Form {
			p.form = n
			break
		}
	}

	if err := p.parse(); err != nil {
		return nil, err
	}

	parent := p.doc
	if context != nil {
		parent = root
	}
	n := 0
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		n++
	}
	result := a.PtrSlice(n)
	i := 0
	for c := parent.FirstChild; c != nil; {
		next := c.NextSibling
		parent.RemoveChild(c)
		result[i] = c
		i++
		c = next
	}
	return result, nil
}

// newNode allocates from the arena when the parser is arena-backed, else from
// the heap — the exact literal shape of every &Node{} site in the tree
// constructor.
func (p *parser) newNode(t NodeType, dataAtom at.Atom, data, namespace string, attr []Attribute) *Node {
	if p.a != nil {
		return p.a.AllocNode(t, dataAtom, data, namespace, attr)
	}
	return &Node{Type: t, DataAtom: dataAtom, Data: data, Namespace: namespace, Attr: attr}
}

// concat merges two strings through the arena when available.
func (p *parser) concat(x, y string) string {
	if p.a != nil {
		return p.a.Concat(x, y)
	}
	return x + y
}

// clone returns an arena clone when available, else Node.clone.
func (p *parser) clone(n *Node) *Node {
	if p.a != nil {
		return p.a.CloneNode(n)
	}
	return n.clone()
}

// cloneAttrs copies an attribute slice into the arena when available.
func (p *parser) cloneAttrs(attr []Attribute) []Attribute {
	if p.a != nil {
		return p.a.AttrView(attr)
	}
	return slices.Clone(attr)
}

// Builder is the arena byte writer every output builder uses: it appends
// into slab regions, grows through the arena, and views the result at the
// end. The view is valid until the owning Arena's next Reset.
type Builder struct {
	A *Arena
	b []byte
}

// NewBuilder returns a Builder writing into a's slab.
func (a *Arena) NewBuilder() Builder {
	return Builder{A: a, b: a.BytesCap(64)}
}

func (w *Builder) WriteString(s string) {
	if len(w.b)+len(s) > cap(w.b) {
		w.b = w.A.Grow(w.b, len(s))
	}
	w.b = append(w.b, s...)
}

func (w *Builder) PutByte(c byte) {
	if len(w.b) == cap(w.b) {
		w.b = w.A.Grow(w.b, 1)
	}
	w.b = append(w.b, c)
}

// String returns a view of the accumulated bytes.
func (w *Builder) String() string {
	if len(w.b) == 0 {
		return ""
	}
	return unsafe.String(&w.b[0], len(w.b))
}

// Len returns the number of accumulated bytes.
func (w *Builder) Len() int { return len(w.b) }

// Bytes returns the accumulated bytes (a slab view; valid until reset).
func (w *Builder) Bytes() []byte { return w.b }

// Reset discards the accumulated bytes, keeping the region for reuse.
func (w *Builder) Reset() { w.b = w.b[:0] }
