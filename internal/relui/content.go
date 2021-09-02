// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package relui

import "embed"

// static is our static web server content.
//go:embed static
var static embed.FS

// templates are our template files.
//go:embed templates
var templates embed.FS
