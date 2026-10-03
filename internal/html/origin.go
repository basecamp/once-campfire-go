// Package html is golang.org/x/net/html v0.59.0 with compatibility changes for
// the pinned Rust/Gumbo parser: legacy select insertion modes from v0.43.0
// (including hr support), source attribute order, namespace-aware insertion-mode
// resets, disabled-scripting noscript fragments, and Gumbo's 400-depth/attribute
// limits. The current tokenizer, foreign-content fixes, and stack guard remain.
// The richtext corpus exercises these changes against 658 reference documents.
package html
