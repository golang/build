// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
)

type ToDo struct {
	message    string // what is to be done
	provenance string // where the TODO came from
}

// todo prints a report to w on which release notes need to be written.
// It takes the doc/next directory of the repo and the date of the last release.
func todo(w io.Writer, fsys fs.FS, prevRelDate time.Time) error {
	var todos []ToDo

	add := func(td ToDo) { todos = append(todos, td) }

	if err := todosFromDocFiles(fsys, add); err != nil {
		return err
	}
	if !prevRelDate.IsZero() {
		if err := todosFromRelnoteCLs(prevRelDate, add); err != nil {
			return err
		}
	}
	return writeToDos(w, todos)
}

// Collect TODOs from the markdown files in the main repo.
func todosFromDocFiles(fsys fs.FS, add func(ToDo)) error {
	// This is essentially a grep.
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			if err := todosFromFile(fsys, path, add); err != nil {
				return err
			}
		}
		return nil
	})
}

func todosFromFile(dir fs.FS, filename string, add func(ToDo)) error {
	f, err := dir.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	ln := 0
	for scan.Scan() {
		ln++
		if line := scan.Text(); strings.Contains(line, "TODO") {
			add(ToDo{
				message:    line,
				provenance: fmt.Sprintf("%s:%d", filename, ln),
			})
		}
	}
	return scan.Err()
}

func todosFromRelnoteCLs(cutoff time.Time, add func(ToDo)) error {
	ctx := context.Background()
	// The maintner corpus doesn't track inline comments. See go.dev/issue/24863.
	// So we need to use a Gerrit API client to fetch them instead. If maintner starts
	// tracking inline comments in the future, this extra complexity can be dropped.
	gerritClient := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
	matchedCLs, err := findCLsWithRelNote(gerritClient, cutoff)
	if err != nil {
		return err
	}
	corpus, err := godata.Get(ctx)
	if err != nil {
		return err
	}
	return corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
			if cl.Status != "merged" {
				return nil
			}
			if cl.Branch() != "master" {
				// Ignore CLs sent to development or release branches.
				return nil
			}
			if cl.Commit.CommitTime.Before(cutoff) {
				// Was in a previous release; not for this one.
				return nil
			}
			// TODO(jba): look for accepted proposals that don't have release notes.
			if _, ok := matchedCLs[int(cl.Number)]; ok {
				comments, err := gerritClient.ListChangeComments(context.Background(), fmt.Sprint(cl.Number))
				if err != nil {
					return err
				}
				if rn := clRelNote(cl, comments); rn != "" {
					if rn == "yes" || rn == "y" {
						rn = "UNKNOWN"
					}
					add(ToDo{
						message:    "TODO:" + rn,
						provenance: fmt.Sprintf("RELNOTE comment in https://go.dev/cl/%d", cl.Number),
					})
				}
			}
			return nil
		})
	})
}

func writeToDos(w io.Writer, todos []ToDo) error {
	for _, td := range todos {
		if _, err := fmt.Fprintf(w, "%s (from %s)\n", td.message, td.provenance); err != nil {
			return err
		}
	}
	return nil
}
