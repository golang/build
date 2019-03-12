// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"path"
	"strings"
)

// ParsePrefixedChangeTitle parses a prefixed change title.
// It returns a list of paths from the prefix joined with root, and the remaining change title.
// It does not try to verify whether each path is an existing Go package.
//
// Supported forms include:
//
// 	"root", "import/path: change title"   -> ["root/import/path"],         "change title"
// 	"root", "path1, path2: change title"  -> ["root/path1", "root/path2"], "change title"  # Multiple comma-separated paths.
//
// If there's no path prefix (preceded by ": "), title is returned unmodified
// with a paths list containing root:
//
// 	"root", "change title"                -> ["root"], "change title"
//
// If there's a branch prefix in square brackets, title is returned with said prefix:
//
// 	"root", "[branch] path: change title" -> ["root/path"], "[branch] change title"
//
func ParsePrefixedChangeTitle(root, prefixedTitle string) (paths []string, title string) {
	// Parse branch prefix in square brackets, if any.
	// E.g., "[branch] path: change title" -> "[branch] ", "path: change title".
	var branch string // "[branch] " or empty string.
	if strings.HasPrefix(prefixedTitle, "[") {
		if idx := strings.Index(prefixedTitle, "] "); idx != -1 {
			branch, prefixedTitle = prefixedTitle[:idx+len("] ")], prefixedTitle[idx+len("] "):]
		}
	}

	// Parse the rest of the prefixed change title.
	// E.g., "path1, path2: change title" -> ["path1", "path2"], "change title".
	idx := strings.Index(prefixedTitle, ": ")
	if idx == -1 {
		return []string{root}, branch + prefixedTitle
	}
	prefix, title := prefixedTitle[:idx], prefixedTitle[idx+len(": "):]
	if strings.ContainsAny(prefix, "{}") {
		// TODO: Parse "image/{png,jpeg}" as ["image/png", "image/jpeg"], maybe?
		return []string{path.Join(root, strings.TrimSpace(prefix))}, branch + title
	}
	paths = strings.Split(prefix, ",")
	for i := range paths {
		paths[i] = path.Join(root, strings.TrimSpace(paths[i]))
	}
	return paths, branch + title
}
