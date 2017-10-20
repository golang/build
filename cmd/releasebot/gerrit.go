// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"golang.org/x/build/gerrit"
)

var gerritClient *gerrit.Client

func loadGerritAuth() {
	gerritClient = gerrit.NewClient("https://go-review.googlesource.com", gerrit.GitCookiesAuth())
}

func changeIDLine(bigID string) string {
	return bigID[strings.LastIndex(bigID, "~")+1:]
}

func (w *Work) gerritQuery(q string) []*gerrit.ChangeInfo {
	for failures := 0; ; {
		changes, err := gerritClient.QueryChanges(context.TODO(), q, gerrit.QueryChangesOpt{Fields: []string{"LABELS", "DETAILED_LABELS", "CURRENT_REVISION", "DETAILED_ACCOUNTS"}})
		if err == nil {
			return changes
		}
		w.log.Printf("gerritQuery: %v", err)
		if failures++; failures >= 5 || !strings.Contains(err.Error(), "HTTP status 500") {
			w.log.Panic(err)
		}
	}
}

// queryGerritCLs fills in Gerrit information for each CL in w.CLs.
func (w *Work) queryGerritCLs() {
	q := ""
	sep := ""
	for _, cl := range w.CLs {
		q += sep + fmt.Sprintf("change:%d", cl.Num)
		sep = " OR "
	}
	changes := w.gerritQuery(q)
	q = ""
	sep = ""
	for _, change := range changes {
		for _, cl := range w.CLs {
			if cl.Num == change.ChangeNumber {
				if change.Branch == w.ReleaseBranch {
					cl.ReleaseBranchGerrit = change
				} else {
					q += sep + fmt.Sprintf("change:go~%s~%s", w.ReleaseBranch, changeIDLine(change.ID))
					sep = " OR "
				}
				cl.Gerrit = change
				cl.Title = cl.Gerrit.Subject
				want := ""
				if cl.Gerrit.Branch == w.ReleaseBranch {
					want = "NEW"
				} else {
					want = "MERGED"
				}
				if cl.Gerrit.Status != want {
					w.logError(cl, fmt.Sprintf("bad Gerrit status: %s (not %s)", cl.Gerrit.Status, want))
				}
				rev := cl.Gerrit.Revisions[cl.Gerrit.CurrentRevision]
				cl.Ref = rev.Ref
				cl.Commit = cl.Gerrit.CurrentRevision
			}
		}
	}

	if q != "" {
		changes := w.gerritQuery(q)
		log.Printf("GERRIT %d\n", len(changes))
		for _, change := range changes {
			for _, cl := range w.CLs {
				if cl.Gerrit != nil && changeIDLine(cl.Gerrit.ID) == changeIDLine(change.ID) {
					log.Printf("found GERRIT %s\n", change.ID)
					cl.ReleaseBranchGerrit = change
				}
			}
		}
	}
}

// findGerritChangeForReleaseBranch finds and returns
// the pending release-branch Gerrit CL with the given Change-Id,
// if any.
func (w *Work) findGerritChangeForReleaseBranch(changeID string) *gerrit.ChangeInfo {
	q := "change:" + changeID + " branch:" + w.ReleaseBranch
	changes, _ := gerritClient.QueryChanges(context.TODO(), q, gerrit.QueryChangesOpt{Fields: []string{"LABELS", "DETAILED_LABELS", "CURRENT_REVISION", "DETAILED_ACCOUNTS"}})
	if len(changes) == 1 {
		return changes[0]
	}
	return nil
}

// labelValue returns the effective value of the named label
// on the given CL.
func labelValue(change *gerrit.ChangeInfo, name string) int {
	v := 0
	for _, ai := range change.Labels[name].All {
		if ai.Value == -2 {
			v = -2
		}
		if ai.Value > v && v != -2 {
			v = ai.Value
		}
	}
	return v
}
