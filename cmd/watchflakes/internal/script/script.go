// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package script implements a simple classification scripting language.
// A script is a sequence of rules of the form “action <- pattern”,
// meaning send results matching pattern to the named action.
package script

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A Script is a sequence of Action <- Pattern rules.
type Script struct {
	File  string
	Rules []*Rule
}

// A Rule is a single Action <- Pattern rule.
type Rule struct {
	Action  string // "skip", "post", and so on
	Pattern Expr   // pattern expression
}

// Action returns the action specified by the script for the given record.
func (s *Script) Action(record Record) string {
	for _, r := range s.Rules {
		if r.Pattern.Match(record) {
			return r.Action
		}
	}
	return ""
}

// A Record is a set of key:value pairs.
type Record map[string]string

// An Expr is a pattern expression that can evaluate itself on a Record.
// The underlying concrete type is *CmpExpr, *AndExpr, *OrExpr, *NotExpr, or *RegExpr.
type Expr interface {
	// String returns the syntax for the pattern.
	String() string

	// Match reports whether the pattern matches the record.
	Match(record Record) bool
}

// A CmpExpr is an Expr for a string comparison.
type CmpExpr struct {
	Field   string
	Op      string
	Literal string
}

func (x *CmpExpr) Match(record Record) bool {
	f := record[x.Field]
	l := x.Literal
	switch x.Op {
	case "==":
		return f == l
	case "!=":
		return f != l
	case "<":
		return f < l
	case "<=":
		return f <= l
	case ">":
		return f > l
	case ">=":
		return f >= l
	}
	return false
}

func (x *CmpExpr) String() string {
	s := strconv.Quote(x.Literal)
	if x.Field == "" {
		return s
	}
	return x.Field + " " + x.Op + " " + s
}

func cmp(field, op, literal string) Expr { return &CmpExpr{field, op, literal} }

// A RegExpr is an Expr for a regular expression test.
type RegExpr struct {
	Field  string
	Not    bool
	Regexp *regexp.Regexp
}

func (x *RegExpr) Match(record Record) bool {
	ok := x.Regexp.MatchString(record[x.Field])
	if x.Not {
		return !ok
	}
	return ok
}

func (x *RegExpr) String() string {
	s := x.Regexp.String()
	s = "`" + strings.ReplaceAll(s, "`", `\x60`) + "`"
	if x.Field == "" {
		return s
	}
	op := " ~ "
	if x.Not {
		op = " !~ "
	}
	return x.Field + op + s
}

func regx(field string, not bool, re *regexp.Regexp) Expr { return &RegExpr{field, not, re} }
func regcomp(s string) (*regexp.Regexp, error) {
	return regexp.Compile("(?m)" + s)
}

// A NotExpr represents the expression !X (the negation of X).
type NotExpr struct {
	X Expr
}

func (x *NotExpr) Match(record Record) bool {
	return !x.X.Match(record)
}

func (x *NotExpr) String() string {
	return "!(" + x.X.String() + ")"
}

func not(x Expr) Expr { return &NotExpr{x} }

// An AndExpr represents the expression X && Y.
type AndExpr struct {
	X, Y Expr
}

func (x *AndExpr) Match(record Record) bool {
	return x.X.Match(record) && x.Y.Match(record)
}

func (x *AndExpr) String() string {
	return andArg(x.X) + " && " + andArg(x.Y)
}

func andArg(x Expr) string {
	s := x.String()
	if _, ok := x.(*OrExpr); ok {
		s = "(" + s + ")"
	}
	return s
}

func and(x, y Expr) Expr {
	return &AndExpr{x, y}
}

// An OrExpr represents the expression X || Y.
type OrExpr struct {
	X, Y Expr
}

func (x *OrExpr) Match(record Record) bool {
	return x.X.Match(record) || x.Y.Match(record)
}

func (x *OrExpr) String() string {
	return orArg(x.X) + " || " + orArg(x.Y)
}

func orArg(x Expr) string {
	s := x.String()
	if _, ok := x.(*AndExpr); ok {
		s = "(" + s + ")"
	}
	return s
}

func or(x, y Expr) Expr {
	return &OrExpr{x, y}
}

// A SyntaxError reports a syntax error in a parsed match expression.
type SyntaxError struct {
	File   string // input file
	Line   int    // line number where error was detected (1-indexed)
	Offset int    // byte offset in line where error was detected (1-indexed)
	Err    string // description of error
}

func (e *SyntaxError) Error() string {
	if e.Offset == 0 {
		return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Err)
	}
	return fmt.Sprintf("%s:%d.%d: %s", e.File, e.Line, e.Offset, e.Err)
}

// A parser holds state for parsing a build expression.
type parser struct {
	file   string          // input file, for errors
	s      string          // input string
	i      int             // next read location in s
	fields map[string]bool // known input fields for comparisons

	tok string // last token read; "`", "\"", "a" for backquoted regexp, literal string, identifier
	lit string // text of backquoted regexp, literal string, or identifier
	pos int    // position (start) of last token
}

// Parse parses text as a script,
// returning the parsed form and any parse errors found.
// (The parser attempts to recover after parse errors by starting over
// at the next newline, so multiple parse errors are possible.)
// The file argument is used for reporting the file name in errors
// and in the Script's File field;
// Parse does not read from the file itself.
func Parse(file, text string, fields []string) (*Script, []*SyntaxError) {
	p := &parser{
		file: file,
		s:    text,
	}
	p.fields = make(map[string]bool)
	for _, f := range fields {
		p.fields[f] = true
	}
	var s Script
	s.File = file
	var errs []*SyntaxError
	for {
		r, err := p.parseRule()
		if err != nil {
			errs = append(errs, err.(*SyntaxError))
			i := strings.Index(p.s[p.i:], "\n")
			if i < 0 {
				break
			}
			p.i += i + 1
			continue
		}
		if r == nil {
			break
		}
		s.Rules = append(s.Rules, r)
	}
	return &s, errs
}

// parseRule parses a single rule from a script.
// On entry, the next input token has not been lexed.
// On exit, the next input token has been lexed and is in p.tok.
// If there is an error, it is guaranteed to be a *SyntaxError.
// parseRule returns nil, nil at end of file.
func (p *parser) parseRule() (x *Rule, err error) {
	defer func() {
		if e := recover(); e != nil {
			if e, ok := e.(*SyntaxError); ok {
				err = e
				return
			}
			panic(e) // unreachable unless parser has a bug
		}
	}()

	x = p.rule()
	if p.tok != "" && p.tok != "\n" {
		p.unexpected()
	}
	return x, nil
}

// unexpected reports a parse error due to an unexpected token
func (p *parser) unexpected() {
	what := p.tok
	switch what {
	case "a":
		what = "identifier " + p.lit
	case "\"":
		what = "quoted string " + p.lit
	case "`":
		what = "backquoted string " + p.lit
	case "\n":
		what = "end of line"
	case "":
		what = "end of script"
	}
	p.parseError("unexpected " + what)
}

// rule parses a single rule.
// On entry, the next input token has not yet been lexed.
// On exit, the next input token has been lexed and is in p.tok.
// If there is no next rule (the script has been read in its entirety), rule returns nil.
func (p *parser) rule() *Rule {
	p.lex()
	for p.tok == "\n" {
		p.lex()
	}
	if p.tok == "" {
		return nil
	}
	if p.tok != "a" {
		p.unexpected()
	}
	action := p.lit
	p.lex()
	if p.tok != "<-" {
		p.unexpected()
	}
	return &Rule{Action: action, Pattern: p.or()}
}

// or parses a sequence of || expressions.
// On entry, the next input token has not yet been lexed.
// On exit, the next input token has been lexed and is in p.tok.
func (p *parser) or() Expr {
	x := p.and()
	for p.tok == "||" {
		x = or(x, p.and())
	}
	return x
}

// and parses a sequence of && expressions.
// On entry, the next input token has not yet been lexed.
// On exit, the next input token has been lexed and is in p.tok.
func (p *parser) and() Expr {
	x := p.cmp()
	for p.tok == "&&" {
		x = and(x, p.cmp())
	}
	return x
}

// cmp parses a comparison expression or atom.
// On entry, the next input token has not been lexed.
// On exit, the next input token has been lexed and is in p.tok.
func (p *parser) cmp() Expr {
	p.lex()
	switch p.tok {
	default:
		p.unexpected()
	case "!":
		p.lex()
		return not(p.atom())
	case "(", "\"", "`":
		return p.atom()
	case "a":
		// comparison
		field := p.lit
		if !p.fields[field] {
			p.parseError("unknown field " + field)
		}
		p.lex()
		switch p.tok {
		default:
			p.unexpected()
		case "==", "!=", "<", "<=", ">", ">=":
			op := p.tok
			p.lex()
			if p.tok != "\"" {
				p.parseError(op + " requires quoted string")
			}
			s := p.lit
			p.lex()
			return cmp(field, op, s)
		case "~", "!~":
			op := p.tok
			p.lex()
			if p.tok != "`" {
				p.parseError(op + " requires backquoted regexp")
			}
			re, err := regcomp(p.lit)
			if err != nil {
				p.parseError("invalid regexp: " + err.Error())
			}
			p.lex()
			return regx(field, op == "!~", re)
		}
	}
	panic("unreachable")
}

// atom parses a regexp or string comparison or a parenthesized expression.
// On entry, the next input token HAS been lexed.
// On exit, the next input token has been lexed and is in p.tok.
func (p *parser) atom() Expr {
	// first token already in p.tok
	switch p.tok {
	default:
		p.unexpected()

	case "(":
		defer func() {
			if e := recover(); e != nil {
				if e, ok := e.(*SyntaxError); ok && e.Err == "unexpected end of expression" {
					e.Err = "missing close paren"
				}
				panic(e)
			}
		}()
		x := p.or()
		if p.tok != ")" {
			p.parseError("missing close paren")
		}
		p.lex()
		return x

	case "`":
		re, err := regcomp(p.lit)
		if err != nil {
			p.parseError("invalid regexp: " + err.Error())
		}
		p.lex()
		return regx("", false, re)
	}
	panic("unreachable")
}

// lex finds and consumes the next token in the input stream.
// On return, p.tok is set to the token text
// and p.pos records the byte offset of the start of the token in the input stream.
// If lex reaches the end of the input, p.tok is set to the empty string.
// For any other syntax error, lex panics with a SyntaxError.
func (p *parser) lex() {
Top:
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
	if p.i >= len(p.s) {
		p.tok = ""
		p.pos = p.i
		return
	}
	switch p.s[p.i] {
	case '#':
		// line comment
		for p.i < len(p.s) && p.s[p.i] != '\n' {
			p.i++
		}
		goto Top
	case '\n':
		// like in Go, not a line ending if it follows a continuation token.
		switch p.tok {
		case "(", "&&", "||", "==", "!=", "~", "!~", "!", "<-":
			p.i++
			goto Top
		}
		p.pos = p.i
		p.i++
		p.tok = p.s[p.pos:p.i]
		return
	case '<': // < <- <=
		p.pos = p.i
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '-' || p.s[p.i] == '=') {
			p.i++
		}
		p.tok = p.s[p.pos:p.i]
		return
	case '!': // ! !~ !=
		p.pos = p.i
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '~' || p.s[p.i] == '=') {
			p.i++
		}
		p.tok = p.s[p.pos:p.i]
		return
	case '>': // > >=
		p.pos = p.i
		p.i++
		if p.i < len(p.s) && p.s[p.i] == '=' {
			p.i++
		}
		p.tok = p.s[p.pos:p.i]
		return
	case '(', ')', '~': // ( ) ~
		p.pos = p.i
		p.i++
		p.tok = p.s[p.pos:p.i]
		return
	case '&', '|', '=': // && || ==
		if p.i+1 >= len(p.s) || p.s[p.i+1] != p.s[p.i] {
			p.lexError("invalid syntax at " + string(rune(p.s[p.i])))
		}
		p.pos = p.i
		p.i += 2
		p.tok = p.s[p.pos:p.i]
		return
	case '`':
		j := p.i + 1
		for j < len(p.s) && p.s[j] != '`' {
			if p.s[j] == '\n' {
				p.lexError("newline in backquoted regexp")
			}
			j++
		}
		if j >= len(p.s) {
			p.lexError("unterminated backquoted regexp")
		}
		p.pos = p.i
		p.i = j + 1
		p.tok = "`"
		p.lit = p.s[p.pos+1 : j]
		return
	case '"':
		j := p.i + 1
		for j < len(p.s) && p.s[j] != '"' {
			if p.s[j] == '\n' {
				p.lexError("newline in quoted string")
			}
			if p.s[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(p.s) {
			p.lexError("unterminated quoted string")
		}
		s, err := strconv.Unquote(p.s[p.i : j+1])
		if err != nil {
			p.lexError("invalid quoted string: " + err.Error())
		}
		p.pos = p.i
		p.i = j + 1
		p.tok = "\""
		p.lit = s
		return
	case '\'':
		p.lexError("single-quoted strings not allowed")
	}

	// ascii name
	if isalpha(p.s[p.i]) {
		j := p.i
		for j < len(p.s) && isalnum(p.s[j]) {
			j++
		}
		p.pos = p.i
		p.i = j
		p.tok = "a"
		p.lit = p.s[p.pos:p.i]
		return
	}

	c, _ := utf8.DecodeRuneInString(p.s[p.i:])
	p.lexError(fmt.Sprintf("invalid syntax at %q (U+%04x)", c, c))
}

// lexError reports a lex error with the given error text.
func (p *parser) lexError(err string) {
	p.errorAt(p.i, err)
}

// parseError reports a parse error with the given error text.
// (A parse error differs from a lex error in which parser position
// the error is attributed to.)
func (p *parser) parseError(err string) {
	p.errorAt(p.pos, err)
}

// errorAt reports a syntax error at the given position.
func (p *parser) errorAt(pos int, err string) {
	line := 1 + strings.Count(p.s[:pos], "\n")
	i := pos - strings.LastIndex(p.s[:pos], "\n")
	panic(&SyntaxError{File: p.file, Line: line, Offset: i, Err: err})
}

// isalpha reports whether c is an ASCII alphabetic or _.
func isalpha(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || c == '_'
}

// isalnum reports whether c is an ASCII alphanumeric or _.
func isalnum(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_'
}
