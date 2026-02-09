// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package logparser parses build.golang.org dashboard logs.
package logparser

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A Fail is a single failure mentioned in a dashboard log.
// (There can be multiple failures in a single log.)
type Fail struct {
	Section string
	Pkg     string
	Test    string
	Mode    string
	Output  string
	Snippet string
}

// compileRE matches compiler errors, with file:line:[col:] at the start of the line.
var compileRE = regexp.MustCompile(`^[a-zA-Z0-9_./\\]+:\d+:(\d+:)? `)

// runningRE matches the buildlet :: Running messages,
// which are displayed at the start of each operation the buildlet does.
var runningRE = regexp.MustCompile(`:: Running [^ ]+ with args \[([^\[\]]+)\] and env`)

// stringRE matches a single Go quoted string.
var stringRE = regexp.MustCompile(`"([^"\\]|\\.)*"`)

var testExecEnv = []string{
	"# GOARCH:",
	"# CPU:",
	"# GOOS:",
	"# OS Version:",
}

// Parse parses a build log, returning all the failures it finds.
// It always returns at least one failure.
func Parse(log string) []*Fail {
	// Some logs have \r\n lines.
	log = strings.ReplaceAll(log, "\r", "")

	// Parsing proceeds line at a time, tracking the "section" of the build
	// we are currently in.
	// When we see lines that might be output associated with a failure,
	// we accumulate them in the hold slice.
	// When we see a line that definitely indicates a failure, we start a
	// new failure f, append it to fails, and also append the lines associated
	// with f to lines. (That is, fails[i]'s output lines are lines[i].)
	// When we see a line that definitely indicates a non-failure, such as
	// an "ok" result from go test, we "flush" the current failure, meaning
	// we set f to nil so there's no current failure anymore, and we clear
	// the hold slice as well. Before clearing the hold slice, we check to
	// see if there are any compile failures in it that should be extracted.
	var (
		section string
		hold    []string
		fails   []*Fail
		lines   [][]string
		f       *Fail
	)

	// flush is called when we've reached a non-failing test in the output,
	// meaning that any failing test is over. It clears f and the hold space,
	// but it also processes the hold space to find any build failures lurking.
	flush := func() {
		// Any unattributed compile-failure-looking lines turn into a build failure.
		if slices.ContainsFunc(hold, compileRE.MatchString) {
			f = &Fail{
				Section: section,
				Mode:    "build",
			}
			fails = append(fails, f)
			lines = append(lines, hold)
			hold = nil
		}
		f = nil
		hold = hold[:0]
	}

	// Process the log, a line at a time.
Lines:
	for i, line := range strings.SplitAfter(log, "\n") {
		// first line is "<builder> at <hash> ..."; ignore.
		if i == 0 && strings.Contains(line, " at ") {
			continue
		}
		// ignore empty string left by Split at end of slice
		if line == "" {
			continue
		}
		// ignore "go: downloading" chatter.
		if strings.HasPrefix(line, "go: downloading ") {
			continue
		}
		// replace lines with trailing spaces with empty line
		if strings.TrimSpace(line) == "" {
			line = "\n"
			if len(hold) == 0 && f == nil {
				continue
			}
		}

		if strings.TrimSpace(line) == "XXXBANNERXXX:Test execution environment." {
			continue
		}

		// :: Running line says what command the buildlet is running now.
		// Use this as the initial section.
		if strings.HasPrefix(line, ":: Running") {
			flush()
			if m := runningRE.FindStringSubmatch(line); m != nil {
				var args []string
				for _, q := range stringRE.FindAllString(m[1], -1) {
					s, _ := strconv.Unquote(q)
					args = append(args, s)
				}
				args[0] = args[0][strings.LastIndex(args[0], `/`)+1:]
				args[0] = args[0][strings.LastIndex(args[0], `\`)+1:]
				section = strings.Join(args, " ")
			}
			continue
		}

		// cmd/dist in the main repo prints Building lines during bootstrap.
		// Use them as sections.
		if strings.HasPrefix(line, "Building ") {
			flush()
			section = strings.TrimSpace(line)
			continue
		}

		// cmd/dist prints ##### lines between test sections in the main repo.
		if strings.HasPrefix(line, "##### ") {
			flush()
			if p := strings.TrimSpace(line[len("##### "):]); p != "" {
				section = p
				continue
			}
		}

		// skipped or passing test from go test marks end of previous failure
		if strings.HasPrefix(line, "?   \t") || strings.HasPrefix(line, "ok  \t") {
			flush()
			continue
		}

		// test binaries print FAIL at the end of execution, as does "go test".
		// If we've already found a specific failure, these are noise and can be dropped.
		if line == "FAIL\n" && len(fails) > 0 {
			continue
		}

		// --- FAIL: marks package testing's report of a failed test.
		// Lines may have been printed above it, during test execution;
		// they are picked up from the hold slice for inclusion in the report.
		if strings.HasPrefix(line, "--- FAIL: ") {
			if fields := strings.Fields(line); len(fields) >= 3 {
				// Found start of test function failure.
				f = &Fail{
					Section: section,
					Test:    fields[2],
					Mode:    "test",
				}
				if strings.HasPrefix(section, "../") {
					f.Pkg = strings.TrimPrefix(section, "../")
				}
				fails = append(fails, f)
				// Include held lines printed above the --- FAIL
				// since they could have been printed from the test.
				lines = append(lines, append(hold, line))
				hold = nil
				continue
			}
		}

		// During go test, build failures are reported starting with a "# pkg/path" or
		// "# pkg/path [foo.test]" line. We have to distinguish these from the # lines
		// printed during the "../test" part of all.bash, and we have to distinguish them
		// from the # key: value lines printed in the # Test execution environment section.
		if strings.HasPrefix(line, "# ") && strings.Contains(line, ":") {
			for _, env := range testExecEnv {
				if strings.HasPrefix(line, env) {
					continue Lines
				}
			}
		}
		if strings.HasPrefix(line, "# ") && section != "../test" {
			if fields := strings.Fields(line); len(fields) >= 2 {
				flush()
				// Found start of build failure.
				f = &Fail{
					Section: section,
					Pkg:     fields[1],
					Mode:    "build",
				}
				fails = append(fails, f)
				lines = append(lines, nil)
				continue
			}
		}

		// In the ../test phase, run.go prints "go run run.go" lines for each failing test.
		if strings.HasPrefix(line, "# go run run.go -- ") {
			f = &Fail{
				Section: section,
				Pkg:     strings.TrimSpace(strings.TrimPrefix(line, "# go run run.go -- ")),
				Mode:    "test",
			}
			fails = append(fails, f)
			lines = append(lines, append(hold, line))
			hold = nil
			continue
		}

		// go test prints "FAIL\tpkg\t0.1s\n" after a failing test's output has been printed.
		// We've seen the failing test cases already but didn't know what package they were from.
		// Update them. If there is no active failure, it could be that the test panicked or
		// otherwise exited without printing the usual test case failures.
		// Create a new Fail in that case, recording whatever output we did see (from the hold slice).
		//
		// In the ../test phase, run.go prints "FAIL\ttestcase.go 0.1s" (space not tab).
		// For those, we don't need to update any test cases.
		//
		if strings.HasPrefix(line, "FAIL\t") {
			if fields := strings.Fields(line); len(fields) >= 3 {
				if strings.Contains(line, "[build failed]") {
					flush()
					continue
				}
				// Found test package failure line printed by cmd/go after test output.
				pkg := fields[1]
				if f != nil && f.Section == "../test" {
					// already collecting
				} else if f != nil {
					for i := len(fails) - 1; i >= 0 && fails[i].Test != ""; i-- {
						fails[i].Pkg = pkg
					}
				} else {
					f = &Fail{
						Section: section,
						Pkg:     pkg,
						Mode:    "test",
					}
					fails = append(fails, f)
					lines = append(lines, hold)
					hold = nil
				}
				flush()
				continue
			}
		}
		if f == nil {
			hold = append(hold, line)
		} else {
			lines[len(fails)-1] = append(lines[len(fails)-1], line)
		}
	}

	// If we didn't find any failures in the log, at least grab the current hold slice.
	// It's not much, but it's something.
	if len(fails) == 0 {
		f = &Fail{
			Section: section,
		}
		fails = append(fails, f)
		lines = append(lines, hold)
		hold = nil
	}
	flush()

	// Now that we have the full output for each failure,
	// build the Output and Snippet fields.
	for i, f := range fails {
		// Trim trailing blank lines.
		out := lines[i]
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		f.Output = strings.Join(out, "")
		f.Snippet = strings.Join(shorten(out, true), "")
		if f.Test == "" && strings.Contains(f.Output, "\n\ngoroutine ") {
			// If a test binary panicked, it doesn't report what test was running.
			// Figure that out by parsing the goroutine stacks.
			findRunningTest(f, out)
		}
	}

	return fails
}

var goroutineStack = regexp.MustCompile(`^goroutine \d+ \[(.*)\]:$`)

// findRunningTest looks at the test output to find the running test goroutine,
// extracts the test name from it, and then updates f.Test.
func findRunningTest(f *Fail, lines []string) {
	goroutineStart := -1 // index of current goroutine's "goroutine N" line.
Scan:
	for i, line := range lines {
		s := strings.TrimSpace(line)
		if s == "" { // blank line marks end of goroutine stack
			goroutineStart = -1
			continue
		}
		if goroutineStack.MatchString(s) {
			goroutineStart = i
		}

		// If we find testing.tRunner on the stack, the frame above it is a test.
		// But in the case of tests using t.Parallel, what usually happens is that
		// many tests are blocked in t.Parallel and one is actually running.
		// Take the first that hasn't called t.Parallel.
		if goroutineStart >= 0 && strings.HasPrefix(s, "testing.tRunner(") && i > 2 {
			// Frame above tRunner should be a test.
			testLine := strings.TrimSpace(lines[i-2])
			if name, _, ok := strings.Cut(testLine, "("); ok {
				if _, test, ok := strings.Cut(name, ".Test"); ok {
					// Ignore this goroutine if it is blocked in t.Parallel.
					for _, line := range lines[goroutineStart+1 : i] {
						if strings.HasPrefix(strings.TrimSpace(line), "testing.(*T).Parallel(") {
							goroutineStart = -1
							continue Scan
						}
					}
					test, _, _ = strings.Cut(test, ".func")
					f.Test = "Test" + test

					// Append the stack trace down to tRunner,
					// but without the goroutine line, and then re-shorten the snippet.
					// We pass false to shorten to discard all the other goroutines:
					// we've found the one we want, and we deleted its goroutine header
					// so that shorten won't remove it.
					var big []string
					big = append(big, lines...)
					big = append(big, "\n")
					big = append(big, lines[goroutineStart+1:i+1]...)
					f.Snippet = strings.Join(shorten(big, false), "")
					return
				}
			}
		}
	}
}

// shorten shortens the output lines to form a snippet.
func shorten(lines []string, keepRunning bool) []string {
	// First, remove most goroutine stacks.
	// Those are often irrelevant and easy to drop from the snippet.
	// If keepRunning is true, we keep the [running] goroutines.
	// If keepRunning is false, we keep no goroutine stacks at all.
	{
		var keep []string
		var wasBlank bool
		var inGoroutine bool
		for _, line := range lines {
			s := strings.TrimSpace(line)
			if inGoroutine {
				if s == "" {
					inGoroutine = false
					wasBlank = true
				}
				continue
			}
			if wasBlank {
				if m := goroutineStack.FindStringSubmatch(s); m != nil && (!keepRunning || m[1] != "running") {
					inGoroutine = true
					continue
				}
			}
			keep = append(keep, line)
			wasBlank = s == ""
		}
		lines = keep
	}

	// Remove trailing blank lines.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	// If we have more than 30 lines, make the snippet by taking the first 10,
	// the last 10, and possibly a middle 10. The middle 10 is included when
	// the interior lines (between the first and last 10) contain an important-looking
	// message like "panic:" or "--- FAIL:". The middle 10 start at the important-looking line.
	// such as
	if len(lines) > 30 {
		var keep []string
		keep = append(keep, lines[:10]...)
		dots := true
		for i := 10; i < len(lines)-10; i++ {
			s := strings.TrimSpace(lines[i])
			if strings.HasPrefix(s, "panic:") || strings.HasPrefix(s, "fatal error:") || strings.HasPrefix(s, "--- FAIL:") || strings.Contains(s, ": internal compiler error:") {
				if i > 10 {
					keep = append(keep, "...\n")
				}
				end := i + 10
				if end >= len(lines)-10 {
					dots = false
					end = len(lines) - 10
				}
				keep = append(keep, lines[i:end]...)
				break
			}
		}
		if dots {
			keep = append(keep, "...\n")
		}
		keep = append(keep, lines[len(lines)-10:]...)
		lines = keep
	}

	return lines
}
