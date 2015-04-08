// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command cl prints a list of open Go code reviews.
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
)

var (
	flagAll = flag.Bool("all", false, "Print all open CLs, not just those needing attention.")
)

func main() {
	flag.Parse()

	c := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
	cis, err := c.QueryChanges("is:open -project:scratch "+strings.Join(flag.Args(), " "), gerrit.QueryChangesOpt{
		N: 5000,
		Fields: []string{
			"LABELS",
			"CURRENT_REVISION",
			"CURRENT_COMMIT",
			"MESSAGES",
			"DETAILED_ACCOUNTS", // fill out Owner.AuthorInfo, etc
			"DETAILED_LABELS",
		},
	})
	if err != nil {
		log.Fatalf("error querying changes: %v", err)
	}
	for _, ci := range cis {
		if !*flagAll && (doNotReviewSubmit(ci) || isRejected(ci) || awaitingAuthor(ci)) {
			continue
		}
		fmt.Printf("https://golang.org/cl/%-5d %-10s %-15s %s\n", ci.ChangeNumber, ci.Project, shortOwner(ci.Owner), ci.Subject)
	}
}

func awaitingAuthor(ci *gerrit.ChangeInfo) bool {
	var amax = ci.Created.Time()
	var rmax time.Time
	for _, msg := range ci.Messages {
		t := msg.Time.Time()
		if msg.Author.Equal(ci.Owner) {
			amax = maxTime(amax, t)
		} else {
			rmax = maxTime(rmax, t)
		}
	}
	return rmax.After(amax)
}

func isRejected(ci *gerrit.ChangeInfo) bool {
	for _, ai := range ci.Labels["Code-Review"].All {
		if ai.Value == -2 {
			return true
		}
	}
	return false
}

func doNotReviewSubmit(ci *gerrit.ChangeInfo) bool {
	if _, ok := ci.Labels["Do-Not-Review"]; ok {
		return true
	}
	if _, ok := ci.Labels["Do-Not-Submit"]; ok {
		return true
	}
	if revInfo, ok := ci.Revisions[ci.CurrentRevision]; ok && revInfo.Commit != nil {
		msg := revInfo.Commit.Message
		if strings.HasPrefix(msg, "dummy:") {
			return true
		}
		for _, phrase := range []string{
			"DO NOT REVIEW",
			"DO NOT SUBMIT",
			"NOT FOR REVIEW",
			"NOT FOR SUBMISSION",
			"WORK IN PROGRESS",
			"NOT READY FOR REVIEW",
		} {
			if strings.Contains(msg, phrase) {
				return true
			}
		}
	}
	return false
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func shortOwner(v *gerrit.AccountInfo) string {
	if v.Username != "" {
		return v.Username
	}
	if v.Email == "r@rcrowley.org" {
		return "rcrowley" // not the commander
	}
	if i := strings.Index(v.Email, "@"); i != -1 {
		return v.Email[:i]
	}
	return ""
}
