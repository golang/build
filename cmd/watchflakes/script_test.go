// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/cmd/watchflakes/internal/script"
)

var scriptTests = [...]struct {
	in  string
	out []*script.Rule
	err string
}{
	{
		"post <- pkg == \"cmd/go\" && test == \"\" && `unexpected files left in tmpdir`",
		[]*script.Rule{{
			Action: "post",
			Pattern: &script.AndExpr{
				X: &script.AndExpr{
					X: &script.CmpExpr{Field: "pkg", Op: "==", Literal: "cmd/go"},
					Y: &script.CmpExpr{Field: "test", Op: "==", Literal: ""},
				},
				Y: &script.RegExpr{Field: "", Not: false, Regexp: regexp.MustCompile(`(?m)unexpected files left in tmpdir`)},
			},
		}},
		"",
	},
	{
		"post <- goos == \"openbsd\" && `unlinkat .*: operation not permitted`",
		[]*script.Rule{{
			Action: "post",
			Pattern: &script.AndExpr{
				X: &script.CmpExpr{Field: "goos", Op: "==", Literal: "openbsd"},
				Y: &script.RegExpr{Field: "", Not: false, Regexp: regexp.MustCompile(`(?m)unlinkat .*: operation not permitted`)},
			},
		}},
		"",
	},
	{
		"post <- pkg ~ `^cmd/go` && `appspot.com.*: 503`",
		[]*script.Rule{{
			Action: "post",
			Pattern: &script.AndExpr{
				X: &script.RegExpr{Field: "pkg", Not: false, Regexp: regexp.MustCompile(`(?m)^cmd/go`)},
				Y: &script.RegExpr{Field: "", Not: false, Regexp: regexp.MustCompile(`(?m)appspot.com.*: 503`)},
			},
		}},
		"",
	},
	{
		`post <- goos == "windows" &&
		         (` + "`dnsquery: DNS server failure` || `getaddrinfow: This is usually a temporary error`)",
		[]*script.Rule{{
			Action: "post",
			Pattern: &script.AndExpr{
				X: &script.CmpExpr{Field: "goos", Op: "==", Literal: "windows"},
				Y: &script.OrExpr{
					X: &script.RegExpr{Field: "", Not: false, Regexp: regexp.MustCompile(`(?m)dnsquery: DNS server failure`)},
					Y: &script.RegExpr{Field: "", Not: false, Regexp: regexp.MustCompile(`(?m)getaddrinfow: This is usually a temporary error`)},
				},
			},
		}},
		"",
	},
	{
		`post <- builder == "darwin-arm64-12" && pkg == "" && test == ""`,
		[]*script.Rule{{
			Action: "post",
			Pattern: &script.AndExpr{
				X: &script.AndExpr{
					X: &script.CmpExpr{Field: "builder", Op: "==", Literal: "darwin-arm64-12"},
					Y: &script.CmpExpr{Field: "pkg", Op: "==", Literal: ""},
				},
				Y: &script.CmpExpr{Field: "test", Op: "==", Literal: ""},
			},
		}},
		"",
	},
	{
		`# note: sometimes the URL is printed with one /
		 default <- ` + "`" + `(Get|read) "https://?(goproxy.io|proxy.golang.com.cn|goproxy.cn)` + "`",
		[]*script.Rule{{
			Action:  "default",
			Pattern: &script.RegExpr{Field: "", Not: false, Regexp: regexp.MustCompile(`(?m)(Get|read) "https://?(goproxy.io|proxy.golang.com.cn|goproxy.cn)`)},
		}},
		"",
	},
	{
		`default <- pkg == "cmd/go" && test == "TestScript" &&
		            output !~ ` + "`" + `The process cannot access the file because it is being used by another process.` + "`" + `  # tracked in go.dev/issue/71112`,
		[]*script.Rule{{
			Action: "default",
			Pattern: &script.AndExpr{
				X: &script.AndExpr{
					X: &script.CmpExpr{Field: "pkg", Op: "==", Literal: "cmd/go"},
					Y: &script.CmpExpr{Field: "test", Op: "==", Literal: "TestScript"},
				},
				Y: &script.RegExpr{Field: "output", Not: true, Regexp: regexp.MustCompile(`(?m)The process cannot access the file because it is being used by another process.`)},
			},
		}},
		"",
	},
	{
		`post <- pkg ~ "^cmd/go"`,
		nil,
		"script:1.15: ~ requires backquoted regexp",
	},
}

func TestParseScript(t *testing.T) {
	for i, tt := range scriptTests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, err := script.Parse("script", tt.in, fields)
			if err != nil {
				if tt.err == "" {
					t.Errorf("Parse(%q): unexpected error: %v", tt.in, err)
				} else if !strings.Contains(fmt.Sprint(err), tt.err) {
					t.Errorf("Parse(%q): error %v, want %v", tt.in, err, tt.err)
				}
				return
			}
			if tt.err != "" {
				t.Errorf("Parse(%q) = %v, want error %v", tt.in, s, tt.err)
				return
			}
			want := &script.Script{
				File:  "script",
				Rules: tt.out,
			}
			if diff := cmp.Diff(want, s, cmp.Comparer(func(x, y *regexp.Regexp) bool { return x.String() == y.String() })); diff != "" {
				t.Errorf("Parse(%q) mismatch (-want +got):\n%s", tt.in, diff)
			}
		})
	}
}
