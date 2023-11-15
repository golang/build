// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relnote

import (
	"strings"
	"testing"
)

func TestCheckFragment(t *testing.T) {
	for _, test := range []struct {
		in string
		// part of err.Error(), or empty if success
		want string
	}{
		{
			// has a TODO
			"# heading\nTODO(jba)",
			"",
		},
		{
			// has a sentence
			"# heading\nSomething.",
			"",
		},
		{
			// sentence is inside some formatting
			"# heading\n- _Some_*thing.*",
			"",
		},
		{
			// multiple sections have what they need
			"# H1\n\nTODO\n\n## H2\nOk.",
			"",
		},
		{
			// questions and exclamations are OK
			"# H1\n Are questions ok? \n# H2\n Must write this note!",
			"",
		},
		{
			"TODO\n# heading",
			"does not start with a heading",
		},
		{
			"#   \t\nTODO",
			"starts with an empty heading",
		},
		{
			"# +heading\nTODO",
			"starts with a non-matching head",
		},
		{
			"# heading",
			"needs",
		},
		{
			"# H1\n non-final section has a problem\n## H2\n TODO",
			"needs",
		},
	} {
		got := CheckFragment(test.in)
		if test.want == "" {
			if got != nil {
				t.Errorf("%q: got %q, want nil", test.in, got)
			}
		} else if got == nil || !strings.Contains(got.Error(), test.want) {
			t.Errorf("%q: got %q, want error containing %q", test.in, got, test.want)
		}
	}
}
