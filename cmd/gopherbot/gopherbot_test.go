// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestLabelCommandsFromComments(t *testing.T) {
	created := time.Now()
	testCases := []struct {
		desc string
		body string
		cmds []labelCommand
	}{
		{
			"basic add/remove",
			"We should fix this issue, but we need help\n\n@gopherbot please add help wanted, needsfix and remove needsinvestigation",
			[]labelCommand{
				{action: "add", label: "help wanted", created: created},
				{action: "add", label: "needsfix", created: created},
				{action: "remove", label: "needsinvestigation", created: created},
			},
		},
		{
			"no please",
			"@gopherbot add NeedsFix",
			[]labelCommand{
				{action: "add", label: "needsfix", created: created},
			},
		},
		{
			"with comma",
			"@gopherbot, NeedsFix",
			[]labelCommand{
				{action: "add", label: "needsfix", created: created},
			},
		},
		{
			"with semicolons",
			"@gopherbot NeedsFix;help wanted; remove needsinvestigation",
			[]labelCommand{
				{action: "add", label: "needsfix", created: created},
				{action: "add", label: "help wanted", created: created},
				{action: "remove", label: "needsinvestigation", created: created},
			},
		},
		{
			"case insensitive",
			"@gopherbot please add HelP WanteD",
			[]labelCommand{
				{action: "add", label: "help wanted", created: created},
			},
		},
		{
			"fun input",
			"@gopherbot please add help wanted,;needsfix;",
			[]labelCommand{
				{action: "add", label: "help wanted", created: created},
				{action: "add", label: "needsfix", created: created},
			},
		},
		{
			"with hyphen",
			"@gopherbot please add label OS-macOS",
			[]labelCommand{
				{action: "add", label: "os-macos", created: created},
			},
		},
		{
			"unlabel keyword",
			"@gopherbot please unlabel needsinvestigation, NeedsDecision",
			[]labelCommand{
				{action: "remove", label: "needsinvestigation", created: created},
				{action: "remove", label: "needsdecision", created: created},
			},
		},
		{
			"with label[s] keyword",
			"@gopherbot please add label help wanted and remove labels needsinvestigation, NeedsDecision",
			[]labelCommand{
				{action: "add", label: "help wanted", created: created},
				{action: "remove", label: "needsinvestigation", created: created},
				{action: "remove", label: "needsdecision", created: created},
			},
		},
		{
			"no label commands",
			"The cake was a lie",
			nil,
		},
	}
	for _, tc := range testCases {
		cmds := labelCommandsFromBody(tc.body, created)
		if diff := cmp.Diff(cmds, tc.cmds, cmp.AllowUnexported(labelCommand{})); diff != "" {
			t.Errorf("%s: commands differ: (-got +want)\n%s", tc.desc, diff)
		}
	}
}

func TestLabelMutations(t *testing.T) {
	testCases := []struct {
		desc   string
		cmds   []labelCommand
		add    []string
		remove []string
	}{
		{
			"basic",
			[]labelCommand{
				{action: "add", label: "foo"},
				{action: "remove", label: "baz"},
			},
			[]string{"foo"},
			[]string{"baz"},
		},
		{
			"add/remove of same label",
			[]labelCommand{
				{action: "add", label: "foo"},
				{action: "remove", label: "foo"},
				{action: "remove", label: "bar"},
				{action: "add", label: "bar"},
			},
			nil,
			nil,
		},
		{
			"deduplication of labels",
			[]labelCommand{
				{action: "add", label: "foo"},
				{action: "add", label: "foo"},
				{action: "remove", label: "bar"},
				{action: "remove", label: "bar"},
			},
			[]string{"foo"},
			[]string{"bar"},
		},
		{
			"forbidden actions",
			[]labelCommand{
				{action: "add", label: "Proposal-Accepted"},
				{action: "add", label: "CherryPickApproved"},
				{action: "add", label: "cla: yes"},
				{action: "remove", label: "Security"},
			},
			nil,
			nil,
		},
		{
			"can add Security",
			[]labelCommand{
				{action: "add", label: "Security"},
			},
			[]string{"Security"},
			nil,
		},
	}
	for _, tc := range testCases {
		add, remove := mutationsFromCommands(tc.cmds)
		if diff := cmp.Diff(add, tc.add); diff != "" {
			t.Errorf("%s: label additions differ: (-got, +want)\n%s", tc.desc, diff)
		}
		if diff := cmp.Diff(remove, tc.remove); diff != "" {
			t.Errorf("%s: label removals differ: (-got, +want)\n%s", tc.desc, diff)
		}
	}
}
