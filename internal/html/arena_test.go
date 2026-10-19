package html

// Differential test: the arena-backed fragment parse must produce the exact
// same tree as the reader-backed ParseFragmentWithOptions on the golden
// corpus and on parser-tricky inputs. Rendering both trees and comparing
// bytes pins node structure, data, attributes and order.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html/atom"
)

func corpusBodies(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../richtext/testdata/rust.json.gz")
	if err != nil {
		t.Skip("corpus not readable:", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	unzipped, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []struct {
			Body string
		}
	}
	if err := json.Unmarshal(unzipped, &corpus); err != nil {
		t.Fatal(err)
	}
	bodies := make([]string, 0, len(corpus.Cases))
	for _, c := range corpus.Cases {
		bodies = append(bodies, c.Body)
	}
	return bodies
}

func renderNodes(nodes []*Node) string {
	var w strings.Builder
	for _, n := range nodes {
		Render(&w, n)
	}
	return w.String()
}

func compareFragments(t *testing.T, body string, context *Node) {
	t.Helper()
	opts := []ParseOption{ParseOptionEnableScripting(false)}
	want, err := ParseFragmentWithOptions(strings.NewReader(strings.TrimPrefix(body, "\ufeff")), context, opts...)
	a := NewArena()
	defer a.Reset()
	got, aerr := a.ParseFragment(body, context, opts...)
	if err != nil {
		// Error parity: the arena parse must fail on the same inputs.
		if aerr == nil {
			t.Fatalf("body %q: reader parse fails (%v) but arena parse succeeds", truncate(body, 80), err)
		}
		return
	}
	if aerr != nil {
		t.Fatalf("body %q: arena parse fails (%v) where reader parse succeeds", truncate(body, 80), aerr)
	}
	w, g := renderNodes(want), renderNodes(got)
	if w != g {
		t.Fatalf("body %q:\nreader: %s\narena:  %s", truncate(body, 80), truncate(w, 300), truncate(g, 300))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func TestArenaParseCorpus(t *testing.T) {
	for _, body := range corpusBodies(t) {
		compareFragments(t, body, nil)
	}
}

func TestArenaParseContexts(t *testing.T) {
	ctx := func(data string) *Node {
		return &Node{Type: ElementNode, Data: data, DataAtom: atom.Lookup([]byte(data))}
	}
	bodies := []string{
		"a<b",                 // implicit p handling
		"<table><tr><td>cell", // foster parenting
		"1 < 2 && 3 > 2 \"quoted\" 'single'",
		"<p><b>bold<i>italic</b>tail</i>", // misnesting
		"<p>1<p>2",                        // auto-closing p
		"<ul><li>a<li>b</ul>",
		"<div class=\"x\" data-a=\"1\">text</div>",
		"<script>if (a < b) { c(); }</script>after", // raw text
		"<style>p > a { color: red }</style>",
		"<textarea>&lt;not a tag&gt;</textarea>",
		"<title>a<b>c</b>d</title>",
		"<xmp>raw &amp; text</xmp>",
		"<!-- comment -- with dashes -->tail",
		"<!DOCTYPE html><p>x",
		"<svg><foreignObject><p>html</p></foreignObject><path d=\"M0 0\"/></svg>",
		"<math><mi>x</mi><annotation-xml encoding=\"text/html\"><b>y</b></annotation-xml></math>",
		"<td>cell",
		"<tr><td>a</td><td>b</td>",
		"<select><option>a<option>b",
		"<p>a\u0000b</p>",
		"a\r\nb\rc",
		"<a href=\"x&amp;y=1\">l</a>",
		"<img src=x onerror=alert(1)>",
		"<p style=\"background:url(javascript:x)\">safe</p>",
		"<b><i>mis</b>nested</i>",
		"<b><p>block</b>tail</p>x",
		"<template><td>x</td></template>",
		"<noscript><p>x</p></noscript>",
		"<pre>\n  kept\n</pre>",
		"<plaintext>everything <b>raw</b>",
		"<p>&notit;<b>a</b>&notin;</p>",
		"<span>&#x1F600;</span>",
		"<form><input name=x></form>",
		"<frameset><frame src=x></frameset>",
		"<iframe>a<b>c</b></iframe>",
		"a<",
		"<div k=v>",
		"<div k='v'>",
		"<div k=\"v\">",
		"<div k=>",
		"</p>",
		"<br/><br />",
		"<svg><script>x<1</script></svg>",
		"<ruby><rb>a<rt>b</ruby>",
		"<dl><dt>t<dd>d</dl>",
		"<p><a href=\"a\">x</a> <a href=\"b\">y</a></p>",
		"<td colspan=2 rowspan=3>x",
		"<table><tbody><tr><td>x</table>",
		"<div><div><div><div>deep",
		"<select><table><tr><td>x</table></select>",
		"<b><b><b><b>nak</b></b></b></b>x",
		"<p>émoji😄 &amp; <3</p>",
		"<body class=\"x\">content",
		"<colgroup><col span=2></colgroup>",
		"<caption>cap</caption>",
		"<p>\u00a0&nbsp;\u00a0</p>",
		"<hr><hr/>",
		"<table><p>foster</p><tr><td>x</table>y",
		"<a href=\"http://x</a>\">odd</a>",
		"</br>",
		"<p><!-- comment only -->",
		"<ul><li>a<ul><li>b</ul></li></ul>",
		"<h1>head</h1><h2>sub</h2>",
		"<blockquote>q</blockquote>",
		"<pre><code>c &lt; d</code></pre>",
		"<figure><img src=x><figcaption>cap</figcaption></figure>",
	}
	for _, body := range bodies {
		compareFragments(t, body, nil)
		compareFragments(t, body, ctx("p"))
		compareFragments(t, body, ctx("table"))
		compareFragments(t, body, ctx("select"))
		compareFragments(t, body, ctx("div"))
	}
}
