// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command greplogs searches Go builder logs.
//
//	greplogs [flags] (-e regexp|-E regexp) paths...
//	greplogs [flags] (-e regexp|-E regexp) -dashboard
//
// greplogs finds builder logs matching a given set of regular
// expressions in Go syntax (godoc.org/regexp/syntax) and extracts
// failures from them.
//
// greplogs can search an arbitrary set of files just like grep.
// Alternatively, the -dashboard flag causes it to search the logs
// saved locally by fetchlogs (golang.org/x/build/cmd/fetchlogs).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/kballard/go-shellquote"
	"golang.org/x/build/cmd/greplogs/internal/logparse"
)

// TODO: If searching dashboard logs, optionally print to builder URLs
// instead of local file names.

// TODO: Optionally extract failures and show only those.

// TODO: Optionally classify matched logs by failure (and show either
// file name or extracted failure).

// TODO: Option to print failure summary versus full failure message.

var (
	fileRegexps regexpList
	failRegexps regexpList
	omit        regexpList
	knownIssues regexpMap

	flagDashboard = flag.Bool("dashboard", true, "search dashboard logs from fetchlogs")
	flagMD        = flag.Bool("md", true, "output in Markdown")
	flagTriage    = flag.Bool("triage", false, "adjust Markdown output for failure triage")
	flagDetails   = flag.Bool("details", false, "surround Markdown results in a <details> tag")
	flagFilesOnly = flag.Bool("l", false, "print only names of matching files")
	flagColor     = flag.String("color", "auto", "highlight output in color: `mode` is never, always, or auto")

	color         *colorizer
	since, before timeFlag
)

const (
	colorPath      = colorFgMagenta
	colorPathColon = colorFgCyan
	colorMatch     = colorBold | colorFgRed
)

var brokenBuilders []string

func main() {
	// XXX What I want right now is just to point it at a bunch of
	// logs and have it extract the failures.
	flag.Var(&knownIssues, "known-issue", "add an issue=regexp mapping; if a log matches regexp it will be categorized under issue. One mapping per flag.")
	flag.Var(&fileRegexps, "e", "show files matching `regexp`; if provided multiple times, files must match all regexps")
	flag.Var(&failRegexps, "E", "show only errors matching `regexp`; if provided multiple times, an error must match all regexps")
	flag.Var(&omit, "omit", "omit results for builder names and/or revisions matching `regexp`; if provided multiple times, logs matching any regexp are omitted")
	flag.Var(&since, "since", "list only failures on revisions since this date, as an RFC-3339 date or date-time")
	flag.Var(&before, "before", "list only failures on revisions before this date, in the same format as -since")
	flag.Parse()

	// Validate flags.
	if *flagDashboard && flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "-dashboard and paths are incompatible\n")
		os.Exit(2)
	}
	switch *flagColor {
	case "never":
		color = newColorizer(false)
	case "always":
		color = newColorizer(true)
	case "auto":
		color = newColorizer(canColor())
	default:
		fmt.Fprintf(os.Stderr, "-color must be one of never, always, or auto")
		os.Exit(2)
	}

	if *flagTriage {
		*flagFilesOnly = true
		if len(failRegexps) == 0 && len(fileRegexps) == 0 {
			failRegexps.Set(".")
		}

		if before.Time.IsZero() {
			year, month, day := time.Now().UTC().Date()
			before = timeFlag{Time: time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
		}

		var err error
		brokenBuilders, err = listBrokenBuilders()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(brokenBuilders) > 0 {
			fmt.Fprintf(os.Stderr, "omitting builders with known issues:\n\t%s\n\n", strings.Join(brokenBuilders, "\n\t"))
		}
	}

	status := 1
	defer func() { os.Exit(status) }()

	numMatching := 0
	if *flagMD {
		args := append([]string{filepath.Base(os.Args[0])}, os.Args[1:]...)
		fmt.Printf("`%s`\n", shellquote.Join(args...))

		defer func() {
			if numMatching == 0 || *flagTriage || *flagDetails {
				fmt.Printf("\n(%d matching logs)\n", numMatching)
			}
		}()
		if *flagDetails {
			os.Stdout.WriteString("<details>\n\n")
			defer os.Stdout.WriteString("\n</details>\n")
		}
	}

	// Gather paths.
	var paths []string
	var stripDir string
	if *flagDashboard {
		revDir := filepath.Join(xdgCacheDir(), "fetchlogs", "rev")
		fis, err := os.ReadDir(revDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", revDir, err)
			os.Exit(1)
		}
		for _, fi := range fis {
			if !fi.IsDir() {
				continue
			}
			paths = append(paths, filepath.Join(revDir, fi.Name()))
		}
		sort.Sort(sort.Reverse(sort.StringSlice(paths)))
		stripDir = revDir + "/"
	} else {
		paths = flag.Args()
	}

	// Process files
	for _, path := range paths {
		filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				status = 2
				fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
				return nil
			}
			if info.IsDir() || strings.HasPrefix(filepath.Base(path), ".") {
				return nil
			}

			nicePath := path
			if stripDir != "" && strings.HasPrefix(path, stripDir) {
				nicePath = path[len(stripDir):]
			}

			found, err := process(path, nicePath)
			if err != nil {
				status = 2
				fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			} else if found {
				numMatching++
				if status == 1 {
					status = 0
				}
			}
			return nil
		})
	}
}

var pathDateRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})-([0-9a-f]+(?:-[0-9a-f]+)?)$`)

func process(path, nicePath string) (found bool, err error) {
	// If this is from the dashboard, filter by builder and date and get the builder URL.
	builder := filepath.Base(nicePath)
	for _, b := range brokenBuilders {
		if builder == b {
			return false, nil
		}
	}
	if omit.AnyMatchString(builder) {
		return false, nil
	}

	if !since.Time.IsZero() || !before.Time.IsZero() {
		revDir := filepath.Dir(nicePath)
		revDirBase := filepath.Base(revDir)
		match := pathDateRE.FindStringSubmatch(revDirBase)
		if len(match) != 3 {
			// Without a valid log date we can't filter by it.
			return false, fmt.Errorf("timestamp not found in rev dir name: %q", revDirBase)
		}
		if omit.AnyMatchString(match[2]) {
			return false, nil
		}
		revTime, err := time.Parse("2006-01-02T15:04:05", match[1])
		if err != nil {
			return false, err
		}
		if !since.Time.IsZero() && revTime.Before(since.Time) {
			return false, nil
		}
		if !before.Time.IsZero() && !revTime.Before(before.Time) {
			return false, nil
		}
	}

	// TODO: Get the URL from the rev.json metadata
	var logURL string
	if link, err := os.Readlink(path); err == nil {
		hash := filepath.Base(link)
		logURL = "https://build.golang.org/log/" + hash
	}

	// TODO: Use streaming if possible.
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	// Check regexp match.
	if !fileRegexps.AllMatch(data) || !failRegexps.AllMatch(data) {
		return false, nil
	}

	printPath := nicePath
	kiMatches := 0
	if *flagMD && logURL != "" {
		prefix := ""
		if *flagTriage {
			matches := knownIssues.Matches(data)
			if len(matches) == 0 {
				prefix = "- [ ] "
			} else {
				kiMatches++
				prefix = fmt.Sprintf("- [x] (%v) ", strings.Join(matches, ", "))
			}
		}
		printPath = fmt.Sprintf("%s[%s](%s)", prefix, nicePath, logURL)
	}

	if *flagFilesOnly {
		fmt.Printf("%s\n", color.color(printPath, colorPath))
		return true, nil
	}

	timer := time.AfterFunc(30*time.Second, func() {
		debug.SetTraceback("all")
		panic("stuck in extracting " + path)
	})

	// Extract failures.
	failures, err := logparse.Extract(string(data), "", "")
	if err != nil {
		return false, err
	}

	timer.Stop()

	// Print failures.
	for _, failure := range failures {
		var msg []byte
		if failure.FullMessage != "" {
			msg = []byte(failure.FullMessage)
		} else {
			msg = []byte(failure.Message)
		}

		if len(failRegexps) > 0 && !failRegexps.AllMatch(msg) {
			continue
		}

		fmt.Printf("%s%s\n", color.color(printPath, colorPath), color.color(":", colorPathColon))
		if *flagMD {
			fmt.Printf("```\n")
		}
		if !color.enabled {
			fmt.Printf("%s", msg)
		} else {
			// Find specific matches and highlight them.
			matches := mergeMatches(append(fileRegexps.Matches(msg),
				failRegexps.Matches(msg)...))
			printed := 0
			for _, m := range matches {
				fmt.Printf("%s%s", msg[printed:m[0]], color.color(string(msg[m[0]:m[1]]), colorMatch))
				printed = m[1]
			}
			fmt.Printf("%s", msg[printed:])
		}
		if *flagMD {
			fmt.Printf("\n```")
		}
		fmt.Printf("\n\n")
	}
	return true, nil
}

func mergeMatches(matches [][]int) [][]int {
	sort.Slice(matches, func(i, j int) bool { return matches[i][0] < matches[j][0] })
	for i := 0; i < len(matches); {
		m := matches[i]

		// Combine with later matches.
		j := i + 1
		for ; j < len(matches); j++ {
			m2 := matches[j]
			if m[1] <= m2[0] {
				// Overlapping or exactly adjacent.
				if m2[1] > m[1] {
					m[1] = m2[1]
				}
				m2[0], m2[1] = 0, 0
			} else {
				break
			}
		}
		i = j
	}

	// Clear out combined matches.
	j := 0
	for _, m := range matches {
		if m[0] == 0 && m[1] == 0 {
			continue
		}
		matches[j] = m
		j++
	}
	return matches[:j]
}
