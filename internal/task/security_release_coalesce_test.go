// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
)

type fakeCoalesceGerrit struct {
	*FakeGerrit

	changes        map[string]*gerrit.ChangeInfo
	commitMessages map[string]string
	cherryPicks    map[string][]cherryPickedCommit
}

type cherryPickedCommit struct {
	changeID string
	message  string
}

func (g *fakeCoalesceGerrit) GetChange(_ context.Context, changeID string, _ ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return nil, errors.New("GetChange: not found")
	}
	return ci, nil
}

func (g *fakeCoalesceGerrit) GetRevisionActions(_ context.Context, changeID string, revision string) (map[string]*gerrit.ActionInfo, error) {
	if _, ok := g.changes[changeID]; !ok {
		return nil, nil
	}
	action := &gerrit.ActionInfo{Enabled: true}
	return map[string]*gerrit.ActionInfo{"submit": action}, nil
}

func (g *fakeCoalesceGerrit) MoveChange(ctx context.Context, changeID string, branch string, keepAllVotes bool) (gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, errors.New("MoveChange: not found")
	}
	ci.Branch = branch
	return *ci, nil
}

func (g *fakeCoalesceGerrit) SubmitChange(ctx context.Context, changeID string) (gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, errors.New("SubmitChange: not found")
	}

	r := make([]byte, 4)
	rand.Read(r)
	g.repos["go"].CommitOnBranch(ci.Branch, map[string]string{"patch": fmt.Sprintf("%x", r)})

	ci.Status = gerrit.ChangeStatusMerged

	return *ci, nil
}

func (g *fakeCoalesceGerrit) RebaseChange(ctx context.Context, changeID string, baseRev string) (gerrit.ChangeInfo, error) {
	return *g.changes[changeID], nil
}

func (g *fakeCoalesceGerrit) CreateCherryPick(ctx context.Context, changeID string, branch string, message string) (gerrit.ChangeInfo, bool, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, false, errors.New("CreateCherryPick: not found")
	}

	g.cherryPicks[branch] = append(g.cherryPicks[branch], cherryPickedCommit{ci.ChangeID, message})
	return *ci, false, nil
}

func (g *fakeCoalesceGerrit) GetCommitMessage(ctx context.Context, changeID string) (string, error) {
	return g.commitMessages[changeID], nil
}

type securityVersionClient struct {
	GerritClient
	tags []string
}

func (c *securityVersionClient) ListTags(ctx context.Context, project string) ([]string, error) {
	return c.tags, nil
}

func (c *securityVersionClient) GetTag(ctx context.Context, project, tag string) (gerrit.TagInfo, error) {
	for _, t := range c.tags {
		if tag == t {
			return gerrit.TagInfo{Created: gerrit.TimeStamp(time.Now())}, nil
		}
	}
	return gerrit.TagInfo{}, errors.New("not found")
}

func TestSecurityReleaseCoalesceTask(t *testing.T) {
	privRepo := NewFakeRepo(t, "go")
	privGerrit := &fakeCoalesceGerrit{FakeGerrit: NewFakeGerrit(t, privRepo), cherryPicks: map[string][]cherryPickedCommit{}, commitMessages: map[string]string{}}
	task := &SecurityReleaseCoalesceTask{
		PrivateGerrit: privGerrit,
		Version: &VersionTasks{
			Gerrit: &securityVersionClient{
				tags: []string{"go1.3", "go1.3.1", "go1.4", "go1.4.1"},
			},
			GoProject: "go",
		},
	}

	privGerrit.changes = map[string]*gerrit.ChangeInfo{
		"1234": {
			ID:           "1234",
			ChangeID:     "1234",
			ChangeNumber: 1234,
			Branch:       "public",
			Submittable:  true,
			Mergeable:    true,
		},
		"5678": {
			ID:           "5678",
			ChangeID:     "5678",
			ChangeNumber: 5678,
			Branch:       "public",
			Submittable:  true,
			Mergeable:    true,
		},
	}

	privGerrit.commitMessages = map[string]string{
		"1234": `subject: 1234

body`,
		"5678": `subject: 5678

other body`,
	}

	head := privRepo.History()[0]
	privRepo.Branch("public", head)
	privRepo.Branch("release-branch.go1.3", head)
	privRepo.Branch("release-branch.go1.4", head)

	wd := task.NewDefinition()
	w, err := wf.Start(wd, map[string]interface{}{
		"Security Patch CL Numbers": []string{"1234", "5678"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, err = w.Run(&wf.TaskContext{Context: ctx, Logger: &testLogger{t: t}}, &verboseListener{t: t})
	if err != nil {
		t.Fatal(err)
	}

	// Check checkpoint branch has the expected number of submitted changes
	commits := len(strings.Split(string(privRepo.runGit("log", "go1.4.2-and-go1.3.2-checkpoint", "--format=%H")), "\n")) - 1
	if commits != 3 {
		t.Errorf("unexpected number of commits on checkpoint branch: got %d, want 3", commits)
	}

	// Check each internal release branch has the expected cherry-picks
	expected := map[string][]cherryPickedCommit{
		"internal-release-branch.go1.4.2": []cherryPickedCommit{
			{
				changeID: "1234",
				message: `[release-branch.go1.4] subject: 1234

body`,
			},
			{
				changeID: "5678",
				message: `[release-branch.go1.4] subject: 5678

other body`,
			},
		},
		"internal-release-branch.go1.3.2": []cherryPickedCommit{
			{
				changeID: "1234",
				message: `[release-branch.go1.3] subject: 1234

body`,
			},
			{
				changeID: "5678",
				message: `[release-branch.go1.3] subject: 5678

other body`,
			},
		},
	}

	for branch, commits := range privGerrit.cherryPicks {
		if !slices.Equal(commits, expected[branch]) {
			t.Errorf("unexpected cherry-picks on %s: got %s, want %s", branch, commits, expected[branch])
		}
	}
}
