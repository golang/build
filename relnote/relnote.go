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
	"io/fs"
	"slices"
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

// Merge combines the markdown documents (files ending in ".md") in the tree rooted
// at fs into a single document.
// The blocks of the documents are concatenated in lexicographic order by filename.
// Heading with no content are removed.
// The link keys must be unique, and are combined into a single map.
func Merge(fsys fs.FS) (*md.Document, error) {
	filenames, err := sortedMarkdownFilenames(fsys)
	if err != nil {
		return nil, err
	}
	doc := &md.Document{}
	for _, filename := range filenames {
		fd, err := parseFile(fsys, filename)
		if err != nil {
			return nil, err
		}
		if len(fd.Blocks) == 0 {
			continue
		}
		if len(doc.Blocks) > 0 {
			// Put a blank line between the current and new blocks.
			lastLine := lastBlock(doc).Pos().EndLine
			delta := lastLine + 2 - fd.Blocks[0].Pos().StartLine
			for _, b := range fd.Blocks {
				addLines(b, delta)
			}
		}
		doc.Blocks = append(doc.Blocks, fd.Blocks...)
		// TODO(jba): merge links
		// TODO(jba): add headings for package sections under "Minor changes to the library".
	}
	// TODO(jba): remove headings with empty contents
	return doc, nil
}

func sortedMarkdownFilenames(fsys fs.FS) ([]string, error) {
	var filenames []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			filenames = append(filenames, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// '.' comes before '/', which comes before alphanumeric characters.
	// So just sorting the list will put a filename like "net.md" before
	// the directory "net". That is what we want.
	slices.Sort(filenames)
	return filenames, nil
}

// lastBlock returns the last block in the document.
// It panics if the document has no blocks.
func lastBlock(doc *md.Document) md.Block {
	return doc.Blocks[len(doc.Blocks)-1]
}

func addLines(b md.Block, n int) {
	pos := position(b)
	pos.StartLine += n
	pos.EndLine += n
}

func position(b md.Block) *md.Position {
	switch b := b.(type) {
	case *md.Heading:
		return &b.Position
	case *md.Text:
		return &b.Position
	case *md.CodeBlock:
		return &b.Position
	case *md.HTMLBlock:
		return &b.Position
	case *md.List:
		return &b.Position
	case *md.Item:
		return &b.Position
	case *md.Empty:
		return &b.Position
	case *md.Paragraph:
		return &b.Position
	case *md.Quote:
		return &b.Position
	default:
		panic(fmt.Sprintf("unknown block type %T", b))
	}
}

func parseFile(fsys fs.FS, path string) (*md.Document, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	in := string(data)
	doc := NewParser().Parse(in)
	return doc, nil
}
