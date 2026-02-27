// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/build/internal/workflow"
)

func TestReleaseGovulncheckActionTasks_NewDefinition(t *testing.T) {
	fakeRepo := NewFakeRepo(t, "govulncheck-action")
	fakeRepo.Tag("v1.1.0", "master")
	commit := fakeRepo.Commit(map[string]string{"README.md": "New content"})

	fakeGerrit := NewFakeGerrit(t, fakeRepo)
	fakeGitHub := &FakeGitHub{
		Tags: map[string]bool{"v1.2.0": true},
	}

	tasks := &ReleaseGovulncheckActionTasks{
		Gerrit: fakeGerrit,
		GitHub: fakeGitHub,
		ApproveAction: func(ctx *workflow.TaskContext) error {
			return nil
		},
	}

	wd := tasks.NewDefinition()

	ctx := t.Context()

	// Start the workflow with version v1.2.0
	w, err := workflow.Start(wd, map[string]any{
		"Version": "v1.2.0",
	})
	if err != nil {
		t.Fatalf("workflow.Start failed: %v", err)
	}

	if _, err := w.Run(ctx, &govulncheckActionVerboseListener{t: t}); err != nil {
		t.Fatalf("workflow.Run failed: %v", err)
	}

	// Verify tags
	tags, err := fakeGerrit.ListTags(ctx, "govulncheck-action")
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}

	foundv120 := false
	foundv1 := false
	for _, tag := range tags {
		if tag == "v1.2.0" {
			foundv120 = true
		}
		if tag == "v1" {
			foundv1 = true
		}
	}

	if !foundv120 {
		t.Errorf("tag v1.2.0 not found in %v", tags)
	}
	if !foundv1 {
		t.Errorf("major tag v1 not found in %v", tags)
	}

	// Verify GitHub release
	if len(fakeGitHub.Releases) != 1 {
		t.Errorf("got %d GitHub releases, want 1", len(fakeGitHub.Releases))
	} else {
		rel := fakeGitHub.Releases[0]
		if *rel.TagName != "v1.2.0" {
			t.Errorf("GitHub release TagName = %q, want %q", *rel.TagName, "v1.2.0")
		}
		if rel.GenerateReleaseNotes == nil || !*rel.GenerateReleaseNotes {
			t.Error("GitHub release GenerateReleaseNotes NOT set to true")
		}
	}

	// Verify major tag points to the right commit
	info, err := fakeGerrit.GetTag(ctx, "govulncheck-action", "v1")
	if err != nil {
		t.Fatalf("GetTag v1 failed: %v", err)
	}
	if info.Revision != commit {
		t.Errorf("v1 tag points to %s, want %s", info.Revision, commit)
	}
}

func TestReleaseGovulncheckActionTasks_Validation(t *testing.T) {
	testCases := []struct {
		name    string
		latest  string
		new     string
		wantErr bool
	}{
		{
			name:    "valid minor increment",
			latest:  "v1.1.0",
			new:     "v1.2.0",
			wantErr: false,
		},
		{
			name:    "valid major increment",
			latest:  "v1.5.0",
			new:     "v2.0.0",
			wantErr: false,
		},
		{
			name:    "invalid regression",
			latest:  "v1.2.0",
			new:     "v1.1.0",
			wantErr: true,
		},
		{
			name:    "invalid same version",
			latest:  "v1.2.0",
			new:     "v1.2.0",
			wantErr: true,
		},
		{
			name:    "invalid skip major",
			latest:  "v1.5.0",
			new:     "v3.0.0",
			wantErr: true,
		},
		{
			name:    "invalid skip minor",
			latest:  "v1.3.0",
			new:     "v1.5.0",
			wantErr: true,
		},
		{
			name:    "invalid skip patch",
			latest:  "v1.3.0",
			new:     "v1.3.2",
			wantErr: true,
		},
		{
			name:    "valid first version",
			latest:  "",
			new:     "v1.0.0",
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeRepo := NewFakeRepo(t, "govulncheck-action")
			if tc.latest != "" {
				fakeRepo.Tag(tc.latest, "master")
			}
			fakeGerrit := NewFakeGerrit(t, fakeRepo)
			fakeGitHub := &FakeGitHub{
				Tags: map[string]bool{tc.new: true},
			}
			tasks := &ReleaseGovulncheckActionTasks{
				Gerrit: fakeGerrit,
				GitHub: fakeGitHub,
				ApproveAction: func(ctx *workflow.TaskContext) error {
					return nil
				},
			}

			wd := tasks.NewDefinition()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			w, err := workflow.Start(wd, map[string]any{
				"Version": tc.new,
			})
			if err != nil {
				t.Fatalf("workflow.Start failed: %v", err)
			}

			_, err = w.Run(ctx, &govulncheckActionVerboseListener{t: t, cancel: cancel})
			if (err != nil) != tc.wantErr {
				t.Errorf("workflow.Run error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

type govulncheckActionVerboseListener struct {
	t      *testing.T
	cancel context.CancelFunc
}

func (l *govulncheckActionVerboseListener) WorkflowStalled(workflowID uuid.UUID) error {
	l.t.Logf("workflow %q: stalled", workflowID.String())
	if l.cancel != nil {
		l.cancel()
	}
	return nil
}

func (l *govulncheckActionVerboseListener) TaskStateChanged(_ uuid.UUID, _ string, st *workflow.TaskState) error {
	if st.Finished && st.Error != "" {
		l.t.Logf("task %-10v: error: %v", st.Name, st.Error)
	} else if st.Finished {
		l.t.Logf("task %-10v: done: %v", st.Name, st.Result)
	}
	return nil
}

func (l *govulncheckActionVerboseListener) Logger(_ uuid.UUID, task string) workflow.Logger {
	return &govulncheckActionTestLogger{t: l.t, task: task}
}

type govulncheckActionTestLogger struct {
	t    *testing.T
	task string
}

func (l *govulncheckActionTestLogger) Printf(format string, v ...any) {
	l.t.Logf("task %-10v: LOG: %s", l.task, fmt.Sprintf(format, v...))
}
