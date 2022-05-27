// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logparse

import (
	"regexp"
	"strings"
)

// A matcher implements incrementally consuming a string using
// regexps.
type matcher struct {
	str    string // string being matched
	pos    int
	groups []string // match groups

	// matchPos is the byte position of the beginning of the
	// match in str.
	matchPos int

	// literals maps from literal strings to the index of the
	// next occurrence of that string.
	literals map[string]int
}

func newMatcher(str string) *matcher {
	return &matcher{str: str, literals: map[string]int{}}
}

func (m *matcher) done() bool {
	return m.pos >= len(m.str)
}

// consume searches for r in the remaining text. If found, it consumes
// up to the end of the match, fills m.groups with the matched groups,
// and returns true.
func (m *matcher) consume(r *regexp.Regexp) bool {
	idx := r.FindStringSubmatchIndex(m.str[m.pos:])
	if idx == nil {
		m.groups = m.groups[:0]
		return false
	}
	if len(idx)/2 <= cap(m.groups) {
		m.groups = m.groups[:len(idx)/2]
	} else {
		m.groups = make([]string, len(idx)/2, len(idx))
	}
	for i := range m.groups {
		if idx[i*2] >= 0 {
			m.groups[i] = m.str[m.pos+idx[i*2] : m.pos+idx[i*2+1]]
		} else {
			m.groups[i] = ""
		}
	}
	m.matchPos = m.pos + idx[0]
	m.pos += idx[1]
	return true
}

// peek returns whether r matches the remaining text.
func (m *matcher) peek(r *regexp.Regexp) bool {
	return r.MatchString(m.str[m.pos:])
}

// lineHasLiteral returns whether any of literals is found before the
// end of the current line.
func (m *matcher) lineHasLiteral(literals ...string) bool {
	// Find the position of the next literal.
	nextLiteral := len(m.str)
	for _, literal := range literals {
		next, ok := m.literals[literal]

		if !ok || next < m.pos {
			// Update the literal position.
			i := strings.Index(m.str[m.pos:], literal)
			if i < 0 {
				next = len(m.str)
			} else {
				next = m.pos + i
			}
			m.literals[literal] = next
		}

		if next < nextLiteral {
			nextLiteral = next
		}
	}
	// If the next literal comes after this line, this line
	// doesn't have any of literals.
	if nextLiteral != len(m.str) {
		eol := strings.Index(m.str[m.pos:], "\n")
		if eol >= 0 && eol+m.pos < nextLiteral {
			return false
		}
	}
	return true
}

// hasPrefix returns whether the remaining text begins with s.
func (m *matcher) hasPrefix(s string) bool {
	return strings.HasPrefix(m.str[m.pos:], s)
}

// line consumes and returns the remainder of the current line, not
// including the line terminator.
func (m *matcher) line() string {
	if i := strings.Index(m.str[m.pos:], "\n"); i >= 0 {
		line := m.str[m.pos : m.pos+i]
		m.pos += i + 1
		return line
	} else {
		line := m.str[m.pos:]
		m.pos = len(m.str)
		return line
	}
}

// peekLine returns the remainder of the current line, not including
// the line terminator, and the position of the beginning of the next
// line.
func (m *matcher) peekLine() (string, int) {
	if i := strings.Index(m.str[m.pos:], "\n"); i >= 0 {
		return m.str[m.pos : m.pos+i], m.pos + i + 1
	} else {
		return m.str[m.pos:], len(m.str)
	}
}
