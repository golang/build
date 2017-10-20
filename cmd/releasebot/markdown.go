// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"strings"
)

// mdChangeLink returns the markdown for a link to CL n.
func mdChangeLink(n int) string {
	return fmt.Sprintf("[CL %d](https://golang.org/cl/%d)", n, n)
}

// mdEscape escapes text so that it does not have any special meaning in Markdown.
func mdEscape(text string) string {
	return mdEscaper.Replace(text)
}

var mdEscaper = strings.NewReplacer(
	`\`, `\\`,
	`{`, `\{`,
	`}`, `\}`,
	"`", "\\`",
	`#`, `\#`,
	`*`, `\*`,
	`+`, `\+`,
	`_`, `\_`,
	`-`, `\-`,
	`(`, `\(`,
	`)`, `\)`,
	`.`, `\.`,
	`[`, `\[`,
	`]`, `\]`,
	`!`, `\!`,
)
