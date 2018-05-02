// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package owners

var (
	bradfitz = Owner{GitHubUsername: "bradfitz", GerritEmail: "bradfitz@golang.org"}
	filippo  = Owner{GitHubUsername: "FiloSottile", GerritEmail: "filippo@golang.org"}
	iant     = Owner{GitHubUsername: "ianlancetaylor", GerritEmail: "iant@golang.org"}
	joetsai  = Owner{GitHubUsername: "dsnet", GerritEmail: "joetsai@google.com"}
	rsc      = Owner{GitHubUsername: "rsc", GerritEmail: "rsc@golang.org"}
)

// entries is a map of <repo name>/<path> to Owner entries.
// It should not be modified at runtime.
var entries = map[string]*Entry{
	"crypto": {
		Primary: []Owner{filippo},
	},
	"go/": {
		Primary: []Owner{rsc, iant, bradfitz},
	},
	"go/src/archive/tar": {
		Primary:   []Owner{joetsai},
		Secondary: []Owner{bradfitz},
	},
}
