// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"errors"
	"net/mail"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
)

type fakePrivXGerrit struct {
	*FakeGerrit

	changes map[string]*gerrit.ChangeInfo
}

func (g *fakePrivXGerrit) GetChange(_ context.Context, changeID string, _ ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return nil, errors.New("GetChange: not found")
	}
	return ci, nil
}

func (g *fakePrivXGerrit) GetRevisionActions(_ context.Context, changeID string, revision string) (map[string]*gerrit.ActionInfo, error) {
	if _, ok := g.changes[changeID]; !ok {
		return nil, nil
	}
	return map[string]*gerrit.ActionInfo{
		"submit": {Enabled: true},
	}, nil
}

func (g *fakePrivXGerrit) MoveChange(ctx context.Context, changeID string, branch string) (gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, errors.New("MoveChange: not found")
	}
	ci.Branch = branch
	return *ci, nil
}

func (g *fakePrivXGerrit) RebaseChange(ctx context.Context, changeID string, baseRev string) (gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, errors.New("RebaseChange: not found")
	}
	return *ci, nil
}

func (g *fakePrivXGerrit) SubmitChange(ctx context.Context, changeID string) (gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, errors.New("SubmitChange: not found")
	}
	ci.Status = gerrit.ChangeStatusMerged
	return *ci, nil
}

const privXMilestoneYAML = `id: 88810010
security_patches:
    - id: 10001
      package: golang.org/x/net/http2
      track: PRIVATE
      changelists:
        - https://go-internal-review.git.corp.google.com/c/net/+/1111
        - https://go-internal-review.git.corp.google.com/c/net/+/2222
      release_note: |
        net/http2: turbulence in the frame buffers causes gophers to levitate.

        Sending a specially crafted SETTINGS frame with the
        ENABLE_LEVITATION=1 causes all subsequent gophers to
        float indefinitely.

        Thanks to a very levitated gopher for reporting this issue.

        This is CVE-1970-0001 and Go issue https://go.dev/issue/4294967296.
      cve: CVE-1970-0001
      github_issue_id: 4294967296
      credits:
        - a very levitated gopher
    - id: 10002
      package: golang.org/x/net/html
      track: PUBLIC
      changelists:
        - https://go.dev/cl/3333
      release_note: |
        net/html: tokenizer emits poetry instead of tokens under a full moon.

        When the system clock aligns with a lunar cycle, the HTML
        tokenizer replaces all div elements with haikus about the
        Go garbage collector.

        Thanks to a confused poet for reporting this issue.

        This is CVE-1970-0002 and Go issue https://go.dev/issue/4294967297.
      cve: CVE-1970-0002
      github_issue_id: 4294967297
      credits:
        - a confused poet`

func TestPrivXPatch(t *testing.T) {
	netRepo := NewFakeRepo(t, "net")
	smRepo := NewFakeRepo(t, "security-metadata")

	head := smRepo.History()[0]
	smRepo.Branch("main", head)
	smRepo.CommitOnBranch("main", map[string]string{
		path.Join("data", "milestones", "88810010.yaml"): privXMilestoneYAML,
	})

	netHead := netRepo.History()[0]
	netRepo.Branch("public", netHead)

	privCommit := netRepo.CommitOnBranch("master", map[string]string{"fix.go": "package fix"})
	netRepo.runGit("update-ref", "refs/changes/1111/1", privCommit)
	privCommit2 := netRepo.CommitOnBranch("master", map[string]string{"fix2.go": "package fix"})
	netRepo.runGit("update-ref", "refs/changes/2222/1", privCommit2)
	privCommit3 := netRepo.CommitOnBranch("master", map[string]string{"fix3.go": "package fix"})
	netRepo.runGit("update-ref", "refs/changes/3333/1", privCommit3)

	privGerrit := &fakePrivXGerrit{
		FakeGerrit: NewFakeGerrit(t, netRepo, smRepo),
		changes: map[string]*gerrit.ChangeInfo{
			"1111": {
				ID:              "1111",
				ChangeID:        "1111",
				ChangeNumber:    1111,
				Project:         "net",
				Branch:          "public",
				Submittable:     true,
				CurrentRevision: "rev1111",
				Status:          gerrit.ChangeStatusMerged,
				Revisions: map[string]gerrit.RevisionInfo{
					"rev1111": {
						Fetch: map[string]*gerrit.FetchInfo{
							"http": {
								URL: netRepo.dir.dir,
								Ref: "refs/changes/1111/1",
							},
						},
					},
				},
			},
			"2222": {
				ID:              "2222",
				ChangeID:        "2222",
				ChangeNumber:    2222,
				Project:         "net",
				Branch:          "public",
				Submittable:     true,
				CurrentRevision: "rev2222",
				Status:          gerrit.ChangeStatusMerged,
				Revisions: map[string]gerrit.RevisionInfo{
					"rev2222": {
						Fetch: map[string]*gerrit.FetchInfo{
							"http": {
								URL: netRepo.dir.dir,
								Ref: "refs/changes/2222/1",
							},
						},
					},
				},
			},
			"3333": {
				ID:              "3333",
				ChangeID:        "3333",
				ChangeNumber:    3333,
				Project:         "net",
				Branch:          "public",
				Submittable:     true,
				CurrentRevision: "rev3333",
				Status:          gerrit.ChangeStatusMerged,
				Revisions: map[string]gerrit.RevisionInfo{
					"rev3333": {
						Fetch: map[string]*gerrit.FetchInfo{
							"http": {
								URL: netRepo.dir.dir,
								Ref: "refs/changes/3333/1",
							},
						},
					},
				},
			},
		},
	}

	pubRepo := NewFakeRepo(t, "net")
	pubRepo.CommitOnBranch("master", map[string]string{"go.mod": "module golang.org/x/net\n\ngo 1.24"})
	pubRepo.runGit("tag", "v1.0.0")
	pubRepo.SetHook("post-receive", `#!/bin/bash -eu
read old new refname
git update-ref refs/heads/master "$new"
echo "Resolving deltas: 100% (5/5)"
echo "Waiting for private key checker: 1/1 objects left"
echo "Processing changes: refs: 1, new: 1, done"
echo
echo "SUCCESS"
echo
echo "  https://go-review.googlesource.com/c/net/+/558675 some change [NEW]"
echo`)

	pubBase, _ := strings.CutSuffix(pubRepo.dir.dir, filepath.Base(pubRepo.dir.dir))
	pubGerrit := NewFakeGerrit(t, pubRepo)
	pubGerrit.ConsiderChangeSubmitted(pubRepo, "558675")

	var announcementHeader MailHeader
	var announcementMessage MailContent
	p := &PrivXPatch{
		Git:           &Git{},
		PrivateGerrit: privGerrit,
		PublicGerrit:  pubGerrit,
		PublicRepoURL: func(repo string) string {
			return pubBase + "/" + repo
		},
		ApproveAction: func(*wf.TaskContext) error { return nil },
		SendMail: func(_ *wf.TaskContext, mh MailHeader, mc MailContent) error {
			announcementHeader, announcementMessage = mh, mc
			return nil
		},
		AnnounceMailHeader: MailHeader{
			From: mail.Address{Address: "security@golang.org"},
			To:   mail.Address{Address: "golang-announce@googlegroups.com"},
		},
	}

	tagxGerrit := NewFakeGerrit(t, pubRepo)
	wd := p.NewDefinition(&TagXReposTasks{Gerrit: tagxGerrit})
	w, err := wf.Start(wd, map[string]any{
		"Release Milestone":                  "88810010",
		reviewersParam.Name:                  []string{},
		"Repository name":                    "net",
		"Skip post submit result (optional)": true,
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

	if !reflect.DeepEqual(announcementHeader, p.AnnounceMailHeader) {
		t.Errorf("announcement header: got %#v, want %#v", announcementHeader, p.AnnounceMailHeader)
	}
	wantSubject := `[security] Vulnerabilities in golang.org/x/net`
	if announcementMessage.Subject != wantSubject {
		t.Errorf("announcement subject:\ngot  %q\nwant %q", announcementMessage.Subject, wantSubject)
	}

	wantText := `Hello gophers,

We have tagged version v1.1.0 of golang.org/x/net in order to address the following security issues:

net/http2: turbulence in the frame buffers causes gophers to levitate.

Sending a specially crafted SETTINGS frame with the
ENABLE_LEVITATION=1 causes all subsequent gophers to
float indefinitely.

Thanks to a very levitated gopher for reporting this issue.

This is CVE-1970-0001 and Go issue https://go.dev/issue/4294967296.

net/html: tokenizer emits poetry instead of tokens under a full moon.

When the system clock aligns with a lunar cycle, the HTML
tokenizer replaces all div elements with haikus about the
Go garbage collector.

Thanks to a confused poet for reporting this issue.

This is CVE-1970-0002 and Go issue https://go.dev/issue/4294967297.

Cheers,
Go Security team
`
	if announcementMessage.BodyText != wantText {
		t.Errorf("announcement text:\ngot:\n%s\nwant:\n%s", announcementMessage.BodyText, wantText)
	}

	wantHTML := `<p>Hello gophers,</p>
<p>We have tagged version v1.1.0 of golang.org/x/net in order to address the following security issues:</p>
<p>net/http2: turbulence in the frame buffers causes gophers to levitate.</p>
<p>Sending a specially crafted SETTINGS frame with the<br>
ENABLE_LEVITATION=1 causes all subsequent gophers to<br>
float indefinitely.</p>
<p>Thanks to a very levitated gopher for reporting this issue.</p>
<p>This is CVE-1970-0001 and Go issue <a href="https://go.dev/issue/4294967296">https://go.dev/issue/4294967296</a>.</p>
<p>net/html: tokenizer emits poetry instead of tokens under a full moon.</p>
<p>When the system clock aligns with a lunar cycle, the HTML<br>
tokenizer replaces all div elements with haikus about the<br>
Go garbage collector.</p>
<p>Thanks to a confused poet for reporting this issue.</p>
<p>This is CVE-1970-0002 and Go issue <a href="https://go.dev/issue/4294967297">https://go.dev/issue/4294967297</a>.</p>
<p>Cheers,<br>
Go Security team</p>
`
	if announcementMessage.BodyHTML != wantHTML {
		t.Errorf("announcement HTML:\ngot:\n%s\nwant:\n%s", announcementMessage.BodyHTML, wantHTML)
	}
}

func TestRepoName(t *testing.T) {
	tests := []struct {
		name    string
		pkg     string
		want    string
		wantErr bool
	}{
		{"subpackage", "golang.org/x/net/http2", "net", false},
		{"root module", "golang.org/x/net", "net", false},
		{"different repo", "golang.org/x/crypto/ssh", "crypto", false},
		{"non-x module", "github.com/foo/bar", "", true},
		{"trailing slash", "golang.org/x/", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repoName(tt.pkg)
			if (err != nil) != tt.wantErr {
				t.Errorf("repoName(%q): err = %v, wantErr = %v", tt.pkg, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("repoName(%q) = %q, want %q", tt.pkg, got, tt.want)
			}
		})
	}
}
