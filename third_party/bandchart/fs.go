// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package bandchart provides an embedded bandchart.js.
package bandchart

import (
	"embed"
)

//go:embed bandchart.js
var FS embed.FS
