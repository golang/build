// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The relnote command summarizes the Go changes in Gerrit marked with
// RELNOTE annotations for the release notes.
package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
)

func main() {
	corpus, err := godata.Get(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	ger := corpus.Gerrit()
	changes := map[string][]string{} // keyed by pkg
	ger.ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
			if relnote := clRelNote(cl); relnote != "" {
				subj := cl.Commit.Msg
				if i := strings.Index(subj, "\n"); i != -1 {
					subj = subj[:i]
				}
				pkg := "??"
				if i := strings.Index(subj, ":"); i != -1 {
					pkg = subj[:i]
				}
				if relnote != "yes" {
					subj = relnote + ": " + subj
				}
				change := fmt.Sprintf("https://golang.org/cl/%d: %s", cl.Number, subj)
				changes[pkg] = append(changes[pkg], change)
			}
			return nil
		})
		return nil
	})

	var pkgs []string
	for pkg, lines := range changes {
		pkgs = append(pkgs, pkg)
		sort.Strings(lines)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		fmt.Printf("%s\n", pkg)
		for _, change := range changes[pkg] {
			fmt.Printf("  %s\n", change)
		}
	}
}

var relNoteRx = regexp.MustCompile(`RELNOTES?=(.+)`)

func parseRelNote(s string) string {
	if m := relNoteRx.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func clRelNote(cl *maintner.GerritCL) string {
	msg := cl.Commit.Msg
	if strings.Contains(msg, "RELNOTE") {
		return parseRelNote(msg)
	}
	for _, comment := range cl.Messages {
		if strings.Contains(comment.Message, "RELNOTE") {
			return parseRelNote(comment.Message)
		}
	}
	return ""
}
