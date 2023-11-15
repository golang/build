// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package relnote supports working with release notes.
//
// Its main feature is the ability to merge Markdown fragments into a single
// document.
//
// This package has minimal imports, so that it can be vendored into the
// main go repo.
//
// # Fragments
//
// A release note fragment is designed to be merged into a final document.
// The merging is done by matching headings, and inserting the contents
// of that heading (that is, the non-heading blocks following it) into
// the merged document.
//
// If the text of a heading begins with '+', then it doesn't have to match
// with an existing heading. If it doesn't match, the heading and its contents
// are both inserted into the result.
//
// A fragment must begin with a non-empty matching heading.
package relnote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	md "rsc.io/markdown"
)

// NewParser returns a properly configured Markdown parser.
func NewParser() *md.Parser {
	var p md.Parser
	p.HeadingIDs = true
	return &p
}

// CheckFragment reports problems in a release-note fragment.
func CheckFragment(data string) error {
	doc := NewParser().Parse(data)
	if len(doc.Blocks) == 0 {
		return errors.New("empty content")
	}
	if !isHeading(doc.Blocks[0]) {
		return errors.New("does not start with a heading")
	}
	htext := text(doc.Blocks[0])
	if strings.TrimSpace(htext) == "" {
		return errors.New("starts with an empty heading")
	}
	if !headingTextMustMatch(htext) {
		return errors.New("starts with a non-matching heading (text begins with a '+')")
	}
	// Check that the content of each heading either contains a TODO or at least one sentence.
	cur := doc.Blocks[0] // the heading beginning the current section
	found := false       // did we find the content we were looking for in this section?
	for _, b := range doc.Blocks[1:] {
		if isHeading(b) {
			if !found {
				break
			}
			cur = b
			found = false
		} else {
			t := text(b)
			// Check for a TODO or standard end-of-sentence punctuation
			// (as a crude approximation to a full sentence).
			found = strings.Contains(t, "TODO") || strings.ContainsAny(t, ".?!")
		}
	}
	if !found {
		return fmt.Errorf("section with heading %q needs a TODO or a sentence", text(cur))
	}
	return nil
}

// isHeading reports whether b is a Heading node.
func isHeading(b md.Block) bool {
	_, ok := b.(*md.Heading)
	return ok
}

// headingTextMustMatch reports whether s is the text of a heading
// that must be matched against another heading.
//
// Headings beginning with '+' don't require a match; all others do.
func headingTextMustMatch(s string) bool {
	return len(s) == 0 || s[0] != '+'
}

// text returns all the text in a block, without any formatting.
func text(b md.Block) string {
	switch b := b.(type) {
	case *md.Heading:
		return text(b.Text)
	case *md.Text:
		return inlineText(b.Inline)
	case *md.CodeBlock:
		return strings.Join(b.Text, "\n")
	case *md.HTMLBlock:
		return strings.Join(b.Text, "\n")
	case *md.List:
		return blocksText(b.Items)
	case *md.Item:
		return blocksText(b.Blocks)
	case *md.Empty:
		return ""
	case *md.Paragraph:
		return text(b.Text)
	case *md.Quote:
		return blocksText(b.Blocks)
	default:
		panic(fmt.Sprintf("unknown block type %T", b))
	}
}

// blocksText returns all the text in a slice of block nodes.
func blocksText(bs []md.Block) string {
	var d strings.Builder
	for _, b := range bs {
		io.WriteString(&d, text(b))
		fmt.Fprintln(&d)
	}
	return d.String()
}

// inlineText returns all the next in a slice of inline nodes.
func inlineText(ins []md.Inline) string {
	var buf bytes.Buffer
	for _, in := range ins {
		in.PrintText(&buf)
	}
	return buf.String()
}
