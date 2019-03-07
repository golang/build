// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package version implements logic to parse version
// of Go tags and release branches.
package version

import (
	"strings"
)

// ParseTag parses the major-minor-patch version triplet
// from goX, goX.Y, or goX.Y.Z tag names,
// and reports whether the tag name is valid.
//
// Tags with suffixes like "go1.2beta3" or "go1.2rc1"
// are currently not supported, and get rejected.
//
// For example, "go1" is parsed as version 1.0.0,
// "go1.2" is parsed as version 1.2.0,
// and "go1.2.3" is parsed as version 1.2.3.
func ParseTag(tagName string) (major, minor, patch int, ok bool) {
	const prefix = "go"
	if !strings.HasPrefix(tagName, prefix) {
		return 0, 0, 0, false
	}
	if strings.HasSuffix(tagName, ".0") {
		// Trailing zero version components must be omitted in Go tags,
		// so reject if we see one.
		return 0, 0, 0, false
	}
	v := strings.SplitN(tagName[len(prefix):], ".", 4)
	if len(v) > 3 {
		return 0, 0, 0, false
	}
	major, ok = parse0To999(v[0])
	if !ok || major == 0 {
		return 0, 0, 0, false
	}
	if len(v) == 2 || len(v) == 3 {
		minor, ok = parse0To999(v[1])
		if !ok {
			return 0, 0, 0, false
		}
	}
	if len(v) == 3 {
		patch, ok = parse0To999(v[2])
		if !ok {
			return 0, 0, 0, false
		}
	}
	return major, minor, patch, true
}

// ParseReleaseBranch parses the major-minor version pair
// from release-branch.goX or release-branch.goX.Y release branch names,
// and reports whether the release branch name is valid.
//
// For example, "release-branch.go1" is parsed as version 1.0,
// and "release-branch.go1.2" is parsed as version 1.2.
func ParseReleaseBranch(branchName string) (major, minor int, ok bool) {
	const prefix = "release-branch.go"
	if !strings.HasPrefix(branchName, prefix) {
		return 0, 0, false
	}
	if strings.HasSuffix(branchName, ".0") {
		// Trailing zero version components must be omitted in Go release branches,
		// so reject if we see one.
		return 0, 0, false
	}
	dottedNum := branchName[len(prefix):] // "1", "1.1", "2", "2.5"
	numDot := strings.Count(dottedNum, ".")
	if numDot > 1 {
		return 0, 0, false
	}
	majorStr, minorStr := dottedNum, ""
	if numDot == 1 {
		dot := strings.Index(dottedNum, ".")
		majorStr, minorStr = dottedNum[:dot], dottedNum[dot+1:]
	}
	major, ok = parse0To999(majorStr)
	if !ok || major == 0 {
		return 0, 0, false
	}
	if numDot > 0 {
		minor, ok = parse0To999(minorStr)
		if !ok {
			return 0, 0, false
		}
	}
	return major, minor, true
}

// parse0To999 converts the canonical ASCII string representation
// of a number in the range [0, 999] to its integer form.
// strconv.Itoa(n) will equal to s if and only if ok is true.
//
// It's similar to strconv.Atoi, except it doesn't permit
// negative numbers, leading '+'/'-' signs, leading zeros,
// or other potential valid but non-canonical string
// representations of numbers.
func parse0To999(s string) (n int, ok bool) {
	if len(s) < 1 || 3 < len(s) {
		return 0, false
	}
	if len(s) > 1 && s[0] == '0' {
		// Leading zeros are rejected.
		return 0, false
	}
	for _, c := range []byte(s) {
		if c < '0' || '9' < c {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
