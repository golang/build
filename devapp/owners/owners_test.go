// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package owners

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMatch(t *testing.T) {
	testCases := []struct {
		path  string
		entry *Entry
	}{
		{
			"crypto/chacha20poly1305/chacha20poly1305.go",
			&Entry{
				Primary: []Owner{filippo},
			},
		},
		{
			"go/src/archive/tar/a.go",
			&Entry{
				Primary:   []Owner{joetsai},
				Secondary: []Owner{bradfitz},
			},
		},
		{
			"go/path/with/no/owners",
			&Entry{
				Primary: []Owner{rsc, iant, bradfitz},
			},
		},
		{
			"nonexistentrepo/foo/bar", nil,
		},
	}
	for _, tc := range testCases {
		matches := match(tc.path)
		if diff := cmp.Diff(matches, tc.entry); diff != "" {
			t.Errorf("%s: owners differ (-got +want)\n%s", tc.path, diff)
		}
	}
}
