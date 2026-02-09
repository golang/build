// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logparse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Failure records a failure extracted from an all.bash log.
type Failure struct {
	// Package is the Go package of this failure. In the case of a
	// testing.T failure, this will be the package of the test.
	Package string

	// Test identifies the failed test function. If this is not a
	// testing.T failure, this will be "".
	Test string

	// Message is the summarized failure message. This will be one
	// line of text.
	Message string

	// FullMessage is a substring of the log that captures the
	// entire failure message. It may be many lines long.
	FullMessage string

	// Function is the fully qualified name of the function where
	// this failure happened, if known. This helps distinguish
	// between generic errors like "out of bounds" and is more
	// stable for matching errors than file/line.
	Function string

	// File is the source file where this failure happened, if
	// known.
	File string

	// Line is the source line where this failure happened, if
	// known.
	Line int

	// OS and Arch are the GOOS and GOARCH of this failure.
	OS, Arch string
}

func (f Failure) String() string {
	s := f.Package
	if f.Test != "" {
		s += "." + f.Test
	}
	if f.Function != "" || f.File != "" {
		if s != "" {
			s += " "
		}
		if f.Function != "" {
			s += "at " + f.Function
		} else {
			s += "at " + f.File
			if f.Line != 0 {
				s += fmt.Sprintf(":%d", f.Line)
			}
		}
	}
	if s != "" {
		s += ": "
	}
	s += f.Message
	return s
}

func (f *Failure) canonicalMessage() string {
	// Do we need to do anything to the message?
	for _, c := range f.Message {
		if '0' <= c && c <= '9' {
			goto rewrite
		}
	}
	return f.Message

rewrite:
	// Canonicalize any "word" of the message containing numbers.
	//
	// TODO: "Escape" any existing … to make this safe as a key
	// for later use with canonicalFields (direct use is
	// unimportant).
	return numberWords.ReplaceAllString(f.Message, "…")
}

// numberWords matches words that consist of both letters and
// digits. Since this is meant to canonicalize numeric fields
// of error messages, we accept any Unicode letter, but only
// digits 0-9. We match the whole word to catch things like
// hexadecimal and temporary file names.
var numberWords = regexp.MustCompile(`\pL*[0-9][\pL0-9]*`)

var (
	linesStar = `(?:.*\n)*?`
	linesPlus = `(?:.*\n)+?`

	// failPkg matches the FAIL line for a package.
	//
	// In case of failure the Android wrapper prints "exitcode=1" without a newline,
	// so for logs prior to the fix for https://golang.org/issue/49317 we need to
	// strip that from the beginning of the line.
	failPkg = `(?m:^(?:exitcode=1)?FAIL[ \t]+(\S+))`

	// logTruncated matches the "log truncated" line injected by the coordinator.
	logTruncated = `(?:\n\.\.\. log truncated \.\.\.)`

	endOfTest = `(?:` + failPkg + `|` + logTruncated + `)`

	canonLine = regexp.MustCompile(`\r+\n`)

	// testingHeader matches the beginning of the go test std
	// section. On Plan 9 there used to be just one #.
	testingHeader = regexp.MustCompile(`^#+ Testing packages`)

	// sectionHeader matches the header of each testing section
	// printed by go tool dist test.
	sectionHeader = regexp.MustCompile(`^##### (.*)`)

	// testingFailed matches a testing.T failure. This may be a
	// T.Error or a recovered panic. There was a time when the
	// test name included GOMAXPROCS (like how benchmark names
	// do), so we strip that out.
	testingFailed = regexp.MustCompile(`^--- FAIL: ([^-\s]+).*\n(` + linesStar + `)` + endOfTest)

	// testingError matches the file name and message of the last
	// T.Error in a testingFailed log.
	testingError = regexp.MustCompile(`(?:.*\n)*\t([^:]+):([0-9]+): (.*)\n`)

	// testingPanic matches a recovered panic in a testingFailed
	// log.
	testingPanic = regexp.MustCompile(`panic: (.*?)(?: \[recovered\])`)

	// gotestFailed matches a $GOROOT/test failure.
	gotestFailed = regexp.MustCompile(`^# go run run\.go.*\n(` + linesPlus + `)` + endOfTest)

	// buildFailed matches build failures from the testing package.
	buildFailed = regexp.MustCompile(`^` + failPkg + `\s+\[build failed\]`)

	// timeoutPanic1 matches a test timeout detected by the testing package.
	timeoutPanic1 = regexp.MustCompile(`^panic: test timed out after .*\n(` + linesStar + `)` + endOfTest)

	// timeoutPanic2 matches a test timeout detected by go test.
	timeoutPanic2 = regexp.MustCompile(`^\*\*\* Test killed.*ran too long\n` + endOfTest)

	// coordinatorTimeout matches a test timeout detected by the
	// coordinator, for both non-sharded and sharded tests.
	coordinatorTimeout = regexp.MustCompile(`(?m)^Build complete.*Result: error: timed out|^Test "[^"]+" ran over [0-9a-z]+ limit`)

	// tbEntry is a regexp string that matches a single
	// function/line number entry in a traceback. Group 1 matches
	// the fully qualified function name. Groups 2 and 3 match the
	// file name and line number.
	// Most entries have trailing stack metadata for each frame,
	// but inlined calls, lacking a frame, may omit that metadata.
	tbEntry = `(\S+)\(.*\)\n\t(.*):([0-9]+)(?: .*)?\n`

	// runtimeFailed matches a runtime throw or testing package
	// panic. Matching the panic is fairly loose because in some
	// cases a "fatal error:" can be preceded by a "panic:" if
	// we've started the panic and then realize we can't (e.g.,
	// sigpanic). Also gather up the "runtime:" prints preceding a
	// throw.
	runtimeFailed        = regexp.MustCompile(`^(?:runtime:.*\n)*.*(?:panic: |fatal error: )(.*)`)
	runtimeLiterals      = []string{"runtime:", "panic:", "fatal error:"}
	runtimeFailedTrailer = regexp.MustCompile(`^(?:exit status.*\n)?(?:\*\*\* Test killed.*\n)?` + endOfTest + `?`)

	// apiCheckerFailed matches an API checker failure.
	apiCheckerFailed = regexp.MustCompile(`^Error running API checker: (.*)`)

	// goodLine matches known-good lines so we can ignore them
	// before doing more aggressive/fuzzy failure extraction.
	goodLine = regexp.MustCompile(`^#|^ok\s|^\?\s|^Benchmark|^PASS|^=== |^--- `)

	// testingUnknownFailed matches the last line of some unknown
	// failure detected by the testing package.
	testingUnknownFailed = regexp.MustCompile(`^` + endOfTest)

	// miscFailed matches the log.Fatalf in go tool dist test when
	// a test fails. We use this as a last resort, mostly to pick
	// up failures in sections that don't use the testing package.
	miscFailed = regexp.MustCompile(`^.*Failed: (?:exit status|test failed)`)
)

// An extractCache speeds up failure extraction from multiple logs by
// caching known lines. It is *not* thread-safe, so we track it in a
// sync.Pool.
type extractCache struct {
	boringLines map[string]bool
}

var extractCachePool sync.Pool

func init() {
	extractCachePool.New = func() any {
		return &extractCache{make(map[string]bool)}
	}
}

// Extract parses the failures from all.bash log m.
func Extract(m string, os, arch string) ([]*Failure, error) {
	fs := []*Failure{}
	testingStarted := false
	section := ""
	sectionHeaderFailures := 0 // # failures at section start
	unknown := []string{}
	cache := extractCachePool.Get().(*extractCache)
	defer extractCachePool.Put(cache)

	// Canonicalize line endings. Note that some logs have a mix
	// of line endings and some somehow have multiple \r's.
	m = canonLine.ReplaceAllString(m, "\n")

	var s []string
	matcher := newMatcher(m)
	consume := func(r *regexp.Regexp) bool {
		matched := matcher.consume(r)
		s = matcher.groups
		if matched && !strings.HasSuffix(s[0], "\n") {
			// Consume the rest of the line.
			matcher.line()
		}
		return matched
	}
	firstBadLine := func() string {
		for _, u := range unknown {
			if len(u) > 0 {
				return u
			}
		}
		return ""
	}

	for !matcher.done() {
		// Check for a cached result.
		line, nextLinePos := matcher.peekLine()
		isGoodLine, cached := cache.boringLines[line]

		// Process the line.
		isKnown := true
		switch {
		case cached:
			matcher.pos = nextLinePos
			if !isGoodLine {
				// This line is known to not match any
				// regexps. Follow the default case.
				isKnown = false
				unknown = append(unknown, line)
			}

		case consume(testingHeader):
			testingStarted = true

		case consume(sectionHeader):
			section = s[1]
			sectionHeaderFailures = len(fs)

		case consume(testingFailed):
			f := &Failure{
				Test:        s[1],
				Package:     s[3],
				FullMessage: s[0],
				Message:     "unknown testing.T failure",
			}

			// TODO: Can have multiple errors per FAIL:
			// ../fetchlogs/rev/2015-03-24T19:51:21-41f9c43/linux-arm64-canonical

			sError := testingError.FindStringSubmatch(s[2])
			sPanic := testingPanic.FindStringSubmatch(s[2])
			if sError != nil {
				f.File, f.Line, f.Message = sError[1], atoi(sError[2]), sError[3]
			} else if sPanic != nil {
				f.Function, f.File, f.Line = panicWhere(s[2])
				f.Message = sPanic[1]
			}

			fs = append(fs, f)

		case consume(gotestFailed):
			fs = append(fs, &Failure{
				Package:     "test/" + s[2],
				FullMessage: s[0],
				Message:     firstLine(s[1]),
			})

		case consume(buildFailed):
			// This may have an accompanying compiler
			// crash, but it's interleaved with other "ok"
			// lines, so it's hard to find.
			fs = append(fs, &Failure{
				FullMessage: s[0],
				Message:     "build failed",
				Package:     s[1],
			})

		case consume(timeoutPanic1):
			fs = append(fs, &Failure{
				Test:        testFromTraceback(s[1]),
				FullMessage: s[0],
				Message:     "test timed out",
				Package:     s[2],
			})

		case consume(timeoutPanic2):
			tb := strings.Join(unknown, "\n")
			fs = append(fs, &Failure{
				Test:        testFromTraceback(tb),
				FullMessage: tb + "\n" + s[0],
				Message:     "test timed out",
				Package:     s[1],
			})

		case matcher.lineHasLiteral(runtimeLiterals...) && consume(runtimeFailed):
			start := matcher.matchPos
			msg := s[1]
			pkg := "testing"
			if strings.Contains(s[0], "fatal error:") {
				pkg = "runtime"
			}
			traceback := consumeTraceback(matcher)
			matcher.consume(runtimeFailedTrailer)
			fn, file, line := panicWhere(traceback)
			fs = append(fs, &Failure{
				Package:     pkg,
				FullMessage: matcher.str[start:matcher.pos],
				Message:     msg,
				Function:    fn,
				File:        file,
				Line:        line,
			})

		case consume(apiCheckerFailed):
			fs = append(fs, &Failure{
				Package:     "API checker",
				FullMessage: s[0],
				Message:     s[1],
			})

		case consume(goodLine):
			// Ignore. Just cache and clear unknown.
			cache.boringLines[line] = true

		case consume(testingUnknownFailed):
			fs = append(fs, &Failure{
				Package:     s[1],
				FullMessage: s[0],
				Message:     "unknown failure: " + firstBadLine(),
			})

		case len(fs) == sectionHeaderFailures && consume(miscFailed):
			fs = append(fs, &Failure{
				Package:     section,
				FullMessage: s[0],
				Message:     "unknown failure: " + firstBadLine(),
			})

		default:
			isKnown = false
			unknown = append(unknown, line)
			cache.boringLines[line] = false
			matcher.pos = nextLinePos
		}

		// Clear unknown lines on any known line.
		if isKnown {
			unknown = unknown[:0]
		}
	}

	// TODO: FullMessages for these.
	if len(fs) == 0 && strings.Contains(m, "no space left on device") {
		fs = append(fs, &Failure{
			Message: "build failed (no space left on device)",
		})
	}
	if len(fs) == 0 && coordinatorTimeout.MatchString(m) {
		// all.bash was killed by coordinator.
		fs = append(fs, &Failure{
			Message: "build failed (timed out)",
		})
	}
	if len(fs) == 0 && strings.Contains(m, "Failed to schedule") {
		// Test sharding failed.
		fs = append(fs, &Failure{
			Message: "build failed (failed to schedule)",
		})
	}
	if len(fs) == 0 && strings.Contains(m, "nosplit stack overflow") {
		fs = append(fs, &Failure{
			Message: "build failed (nosplit stack overflow)",
		})
	}

	// If the same (message, where) shows up in more than five
	// packages, it's probably a systemic issue, so collapse it
	// down to one failure with no package.
	type dedup struct {
		packages map[string]bool
		kept     bool
	}
	msgDedup := map[Failure]*dedup{}
	failureMap := map[*Failure]*dedup{}
	maxCount := 0
	for _, f := range fs {
		key := Failure{
			Message:  f.canonicalMessage(),
			Function: f.Function,
			File:     f.File,
			Line:     f.Line,
		}

		d := msgDedup[key]
		if d == nil {
			d = &dedup{packages: map[string]bool{}}
			msgDedup[key] = d
		}
		d.packages[f.Package] = true
		if len(d.packages) > maxCount {
			maxCount = len(d.packages)
		}
		failureMap[f] = d
	}
	if maxCount >= 5 {
		fsn := []*Failure{}
		for _, f := range fs {
			d := failureMap[f]
			if len(d.packages) < 5 {
				fsn = append(fsn, f)
			} else if !d.kept {
				d.kept = true
				f.Test, f.Package = "", ""
				fsn = append(fsn, f)
			}
		}
		fs = fsn
	}

	// Check if we even got as far as testing. Note that there was
	// a period when we didn't print the "testing" header, so as
	// long as we found failures, we don't care if we found the
	// header.
	if !testingStarted && len(fs) == 0 {
		fs = append(fs, &Failure{
			Message: "toolchain build failed",
		})
	}

	for _, f := range fs {
		f.OS, f.Arch = os, arch

		// Clean up package. For misc/cgo tests, this will be
		// something like
		// _/tmp/buildlet-scatch825855615/go/misc/cgo/test.
		if strings.HasPrefix(f.Package, "_/tmp/") {
			f.Package = strings.SplitN(f.Package, "/", 4)[3]
		}

		// Trim trailing newlines from FullMessage.
		f.FullMessage = strings.TrimRight(f.FullMessage, "\n")
	}
	return fs, nil
}

func atoi(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		panic("expected number, got " + s)
	}
	return v
}

// firstLine returns the first line from s, not including the line
// terminator.
func firstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before
	}
	return s
}

var (
	tracebackStart = regexp.MustCompile(`^(goroutine [0-9]+.*:|runtime stack:)\n`)
	tracebackEntry = regexp.MustCompile(`^` + tbEntry)
)

// consumeTraceback consumes a traceback from m.
func consumeTraceback(m *matcher) string {
	// Find the beginning of the traceback.
	for !m.done() && !m.peek(tracebackStart) {
		m.line()
	}

	start := m.pos
loop:
	for !m.done() {
		switch {
		case m.hasPrefix("\n") || m.hasPrefix("\t") ||
			m.hasPrefix("goroutine ") || m.hasPrefix("runtime stack:") ||
			m.hasPrefix("created by "):
			m.line()

		case m.consume(tracebackEntry):
			// Do nothing.

		default:
			break loop
		}
	}
	return m.str[start:m.pos]
}

var (
	// testFromTracebackRe matches a traceback entry from a
	// function named Test* in a file named *_test.go. It ignores
	// "created by" lines.
	testFromTracebackRe = regexp.MustCompile(`\.(Test[^(\n]+)\(.*\n.*_test\.go`)

	panicWhereRe = regexp.MustCompile(`(?m:^)` + tbEntry)
)

// testFromTraceback attempts to return the test name from a
// traceback.
func testFromTraceback(tb string) string {
	s := testFromTracebackRe.FindStringSubmatch(tb)
	if s == nil {
		return ""
	}
	return s[1]
}

// panicWhere attempts to return the fully qualified name, source
// file, and line number of the panicking function in traceback tb.
func panicWhere(tb string) (fn string, file string, line int) {
	m := matcher{str: tb}
	for m.consume(panicWhereRe) {
		fn := m.groups[1]

		// Ignore functions involved in panic handling.
		if strings.HasPrefix(fn, "runtime.panic") || fn == "runtime.throw" || fn == "runtime.sigpanic" {
			continue
		}
		return fn, m.groups[2], atoi(m.groups[3])
	}
	return "", "", 0
}
