// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package script

import (
	"fmt"
	"testing"
)

var lexTests = [...]struct {
	in  string
	out string
}{
	{"", ""},
	{"x", "a"},
	{"x.y", "a err: :1.2: invalid syntax at '.' (U+002e)"},
	{"x_y", "a"},
	{"αx", "err: :1.1: invalid syntax at 'α' (U+03b1)"},
	{"x y", "a a"},
	{"x!y", "a ! a"},
	{"&&||!()xy yx ", "&& || ! ( ) a a"},
	{"x~", "a ~"},
	{"x ~", "a ~"},
	{"x &", "a err: :1.3: invalid syntax at &"},
	{"x &y", "a err: :1.3: invalid syntax at &"},
	{"output !~ `content`", "a ! ~ `"},
}

func TestLex(t *testing.T) {
	for i, tt := range lexTests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			p := &parser{s: tt.in}
			out := ""
			for {
				tok, err := lex(p)
				if tok == "" && err == nil {
					break
				}
				if out != "" {
					out += " "
				}
				if err != nil {
					out += "err: " + err.Error()
					break
				}
				out += tok
			}
			if out != tt.out {
				t.Errorf("lex(%q):\nhave %s\nwant %s", tt.in, out, tt.out)
			}
		})
	}
}

func lex(p *parser) (tok string, err error) {
	defer func() {
		if e := recover(); e != nil {
			if e, ok := e.(*SyntaxError); ok {
				err = e
				return
			}
			panic(e)
		}
	}()

	p.lex()
	return p.tok, nil
}
