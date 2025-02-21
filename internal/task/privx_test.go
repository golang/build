// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

type privxClient struct {
	GerritClient

	privRepoDir     string
	expectedProject string
}

func (c *privxClient) Submitted(ctx context.Context, changeID string, parentCommit string) (string, bool, error) {
	return "123", true, nil
}

func (c *privxClient) ListProjects(ctx context.Context) ([]string, error) {
	return []string{"net"}, nil
}

func (c *privxClient) ReadBranchHead(ctx context.Context, project string, branch string) (string, error) {
	if project != c.expectedProject {
		return "", fmt.Errorf("wrong project: got %q, want %q", project, c.expectedProject)
	}
	return "", nil
}

func (c *privxClient) ReadFile(ctx context.Context, project string, head string, file string) ([]byte, error) {
	if project != c.expectedProject {
		return nil, fmt.Errorf("wrong project: got %q, want %q", project, c.expectedProject)
	}
	return []byte("module golang.org/x/net\n\ngo 1.24"), nil
}

func (c *privxClient) ListTags(ctx context.Context, repo string) ([]string, error) {
	return []string{"v1.0.0"}, nil
}

func (c *privxClient) Tag(ctx context.Context, repo string, tag string, commit string) error {
	return nil
}

func (c *privxClient) GetRevisionActions(ctx context.Context, changeID, revision string) (map[string]*gerrit.ActionInfo, error) {
	return map[string]*gerrit.ActionInfo{
		"submit": &gerrit.ActionInfo{Enabled: true},
	}, nil
}

func (c *privxClient) CreateBranch(ctx context.Context, project, branch string, input gerrit.BranchInput) (string, error) {
	if project != c.expectedProject {
		return "", fmt.Errorf("wrong project: got %q, want %q", project, c.expectedProject)
	}
	return "", nil
}

func (c *privxClient) MoveChange(ctx context.Context, changeID string, branch string) (gerrit.ChangeInfo, error) {
	return gerrit.ChangeInfo{}, nil
}

func (c *privxClient) RebaseChange(ctx context.Context, changeID string, revision string) (gerrit.ChangeInfo, error) {
	return gerrit.ChangeInfo{}, nil
}

func (c *privxClient) SubmitChange(ctx context.Context, changeID string) (gerrit.ChangeInfo, error) {
	return gerrit.ChangeInfo{}, nil
}

func (c *privxClient) GetChange(ctx context.Context, changeID string, opts ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error) {
	return &gerrit.ChangeInfo{
		Project:         "net",
		Status:          gerrit.ChangeStatusMerged,
		CurrentRevision: "dead",
		Submittable:     true,
		Revisions: map[string]gerrit.RevisionInfo{
			"dead": {
				Fetch: map[string]*gerrit.FetchInfo{
					"http": {
						URL: c.privRepoDir,
						Ref: "refs/changes/1234/5",
					},
				},
			},
		},
	}, nil
}

func TestPrivXPatch(t *testing.T) {
	privRepo := NewFakeRepo(t, "net")
	pubRepo := NewFakeRepo(t, "net")

	privCommit := privRepo.CommitOnBranch("master", map[string]string{"hi.go": ":)"})
	privRepo.runGit("update-ref", "refs/changes/1234/5", privCommit)

	if err := os.WriteFile(filepath.Join(pubRepo.dir.dir, ".git/hooks/pre-receive"), []byte(`#!/bin/sh
echo "Resolving deltas: 100% (5/5)"
echo "Waiting for private key checker: 1/1 objects left"
echo "Processing changes: refs: 1, new: 1, done"
echo
echo "SUCCESS"
echo
echo "  https://go-review.googlesource.com/c/net/+/558675 net/mail: remove obsolete comment [NEW]"
echo`), 0777); err != nil {
		t.Fatalf("failed to write git pre-receive hook: %s", err)
	}

	pubBase, _ := strings.CutSuffix(pubRepo.dir.dir, filepath.Base(pubRepo.dir.dir))

	var announcementHeader MailHeader
	var announcementMessage MailContent
	p := &PrivXPatch{
		Git:           &Git{},
		PrivateGerrit: &privxClient{privRepoDir: privRepo.dir.dir, expectedProject: "net"},
		PublicGerrit:  &privxClient{expectedProject: "net"},

		PublicRepoURL: func(repo string) string {
			return pubBase + "/" + repo
		},

		ApproveAction: func(*workflow.TaskContext) error { return nil },
		SendMail: func(mh MailHeader, mc MailContent) error {
			announcementHeader, announcementMessage = mh, mc
			return nil
		},
		AnnounceMailHeader: MailHeader{
			From: mail.Address{Address: "hello@google.com"},
			To:   mail.Address{Address: "there@google.com"},
		},
	}

	wd := p.NewDefinition(&TagXReposTasks{Gerrit: &privxClient{expectedProject: filepath.Base(pubRepo.dir.dir)}})
	w, err := workflow.Start(wd, map[string]any{
		"go-internal CL number":              "1234",
		reviewersParam.Name:                  []string{},
		"Repository name":                    filepath.Base(pubRepo.dir.dir),
		"Skip post submit result (optional)": true,
		"CVE":                                "CVE-2024-1234",
		"GitHub issue":                       "https://go.dev/issues/1234",
		"Release note":                       "We fixed a thing.",
		"Acknowledgement":                    "a very nice person",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Run(context.Background(), &verboseListener{t: t})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(announcementHeader, p.AnnounceMailHeader) {
		t.Errorf("unexpected announcement header: got %#v, want %#v", announcementHeader, p.AnnounceMailHeader)
	}

	expectedSubject := `[security] Vulnerability in golang.org/x/net`
	if announcementMessage.Subject != expectedSubject {
		t.Errorf("unexpected announcement subject: got %q, want %q", announcementMessage.Subject, expectedSubject)
	}
	expectedMessage := `Hello gophers,

We have tagged version v1.1.0 of golang.org/x/net in order to address a security issue.

We fixed a thing.

Thanks to a very nice person for reporting this issue.

This is CVE-2024-1234 and Go issue https://go.dev/issues/1234.

Cheers,
Go Security team
`
	if announcementMessage.BodyText != expectedMessage {
		t.Errorf("unexpected announcement plaintext: got: %s\n\nwant: %s\n", announcementMessage.BodyText, expectedMessage)
	}

	expectedHTML := `<p>Hello gophers,</p>
<p>We have tagged version v1.1.0 of golang.org/x/net in order to address a security issue.</p>
<p>We fixed a thing.</p>
<p>Thanks to a very nice person for reporting this issue.</p>
<p>This is CVE-2024-1234 and Go issue <a href="https://go.dev/issues/1234">https://go.dev/issues/1234</a>.</p>
<p>Cheers,<br>
Go Security team</p>
`
	if announcementMessage.BodyHTML != expectedHTML {
		t.Errorf("unexpected announcement HTML: got: %s\n\nwant: %s\n", announcementMessage.BodyHTML, expectedHTML)
	}
}
