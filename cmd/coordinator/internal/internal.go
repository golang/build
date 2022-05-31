// Copyright 2022 Go Authors All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package internal

import (
	"os"
	"path/filepath"
)

// FilePath returns the path to the specified file. If the file is not found
// in the current directory, it will return a relative path for the prefix
// that the file exists at.
func FilePath(base string, prefixes ...string) string {
	// First, attempt to find the file with no prefix.
	prefixes = append([]string{""}, prefixes...)
	for _, p := range prefixes {
		if _, err := os.Stat(filepath.Join(p, base)); err == nil {
			return filepath.Join(p, base)
		}
	}
	return base
}
