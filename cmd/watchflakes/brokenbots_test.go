// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"
	"testing"

	"rsc.io/github"
)

func TestBotIssuesByID(t *testing.T) {
	issue := func(n int, bot string, closed bool) *Issue {
		return &Issue{Issue: &github.Issue{
			Number: n,
			Closed: closed,
			Body:   "The bot is broken.\n<!-- DO NOT EDIT: " + bot + " -->",
		}}
	}
	tests := []struct {
		name   string
		issues []*Issue
		want   int // issue number chosen for botA, 0 if none
	}{
		{"no issues", nil, 0},
		{"open", []*Issue{issue(100, "botA", false)}, 100},
		{"closed", []*Issue{issue(100, "botA", true)}, 100},
		{"newest closed wins", []*Issue{issue(100, "botA", true), issue(200, "botA", true)}, 200},
		{"open beats newer closed", []*Issue{issue(100, "botA", false), issue(200, "botA", true)}, 100},
		{"other bot", []*Issue{issue(100, "botB", true)}, 0},
		{"no footer", []*Issue{{Issue: &github.Issue{Number: 100, Body: "no footer"}}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := 0
			if i, ok := botIssuesByID(tt.issues)["botA"]; ok {
				got = i.Number
			}
			if got != tt.want {
				t.Errorf("botIssuesByID()[botA] = #%d, want #%d", got, tt.want)
			}
		})
	}
}

func TestBrokenBotIssueBody(t *testing.T) {
	bot := Bot{ID: "botA", Dead: true}

	body := brokenBotIssueBody(bot, nil, 0)
	if strings.Contains(body, "previously reported") {
		t.Errorf("body without prev mentions a previous issue:\n%s", body)
	}
	if id, ok := botIDFromIssueBody(body); !ok || id != "botA" {
		t.Errorf("botIDFromIssueBody = %q, %v; want %q, true", id, ok, "botA")
	}

	body = brokenBotIssueBody(bot, nil, 100)
	if !strings.Contains(body, "previously reported as broken in #100") {
		t.Errorf("body with prev=100 does not mention #100:\n%s", body)
	}
	if id, ok := botIDFromIssueBody(body); !ok || id != "botA" {
		t.Errorf("botIDFromIssueBody = %q, %v; want %q, true", id, ok, "botA")
	}
}
