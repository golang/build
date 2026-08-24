// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v74/github"
	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/relmeta"
	"golang.org/x/vulndb/report"
	yaml "gopkg.in/yaml.v3"
)

type fakePrivXGerrit struct {
	*FakeGerrit

	changes map[string]*gerrit.ChangeInfo
}

type fakePrivXGitHub struct {
	*FakeGitHub

	t          *testing.T
	gerrit     *FakeGerrit
	vulndbBase string
}

func (g *fakePrivXGitHub) EditIssue(ctx context.Context, owner, repo string, number int, issue *github.IssueRequest) (*github.Issue, *github.Response, error) {
	head, err := g.gerrit.ReadBranchHead(ctx, "vulndb", "master")
	if err != nil {
		g.t.Errorf("EditIssue(%d): reading vulndb head: %v", number, err)
	} else if head == g.vulndbBase {
		g.t.Errorf("EditIssue(%d) called before vuln reports were submitted", number)
	}
	return g.FakeGitHub.EditIssue(ctx, owner, repo, number, issue)
}

func (g *fakePrivXGerrit) GetChange(_ context.Context, changeID string, _ ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return nil, NewGerritHTTPError(http.StatusNotFound, fmt.Sprintf("change %s not found\n", changeID))
	}
	return ci, nil
}

func (g *fakePrivXGerrit) GetRevisionActions(_ context.Context, changeID string, revision string) (map[string]*gerrit.ActionInfo, error) {
	if _, ok := g.changes[changeID]; !ok {
		return nil, NewGerritHTTPError(http.StatusNotFound, fmt.Sprintf("change %s not found\n", changeID))
	}
	return map[string]*gerrit.ActionInfo{
		"submit": {Enabled: true},
	}, nil
}

func (g *fakePrivXGerrit) MoveChange(ctx context.Context, changeID string, branch string) (gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, NewGerritHTTPError(http.StatusNotFound, fmt.Sprintf("change %s not found\n", changeID))
	}
	if ci.Branch == branch {
		return gerrit.ChangeInfo{}, NewGerritHTTPError(http.StatusConflict, "Change is already destined for the specified branch\n")
	}
	ci.Branch = branch
	return *ci, nil
}

func (g *fakePrivXGerrit) RebaseChange(ctx context.Context, changeID string, baseRev string) (gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, NewGerritHTTPError(http.StatusNotFound, fmt.Sprintf("change %s not found\n", changeID))
	}
	if baseRev == "" && ci.Branch != "" {
		g.changesMu.Lock()
		base, hasBase := g.clBases[changeID]
		g.changesMu.Unlock()
		if hasBase {
			project := ci.Project
			if repo, err := g.repo(project); err == nil {
				if head, err := repo.dir.RunCommand(ctx, "rev-parse", ci.Branch); err == nil {
					if base == strings.TrimSpace(string(head)) {
						return gerrit.ChangeInfo{}, NewGerritHTTPError(http.StatusConflict, "Change is already up to date.\n")
					}
				}
			}
		}
	}
	if baseRev != "" {
		g.changesMu.Lock()
		g.clBases[changeID] = baseRev
		g.changesMu.Unlock()
	} else if ci.Branch != "" {
		project := ci.Project
		if repo, err := g.repo(project); err == nil {
			if head, err := repo.dir.RunCommand(ctx, "rev-parse", ci.Branch); err == nil {
				g.changesMu.Lock()
				g.clBases[changeID] = strings.TrimSpace(string(head))
				g.changesMu.Unlock()
			}
		}
	}
	return *ci, nil
}

func (g *fakePrivXGerrit) SubmitChange(ctx context.Context, changeID string) (gerrit.ChangeInfo, error) {
	ci, ok := g.changes[changeID]
	if !ok {
		return gerrit.ChangeInfo{}, NewGerritHTTPError(http.StatusNotFound, fmt.Sprintf("change %s not found\n", changeID))
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
      target_releases:
        - 1.1.0
      cve: CVE-1970-0001
      github_issue_id: 4294967296
      vuln_report_id: GO-1970-0001
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
      target_releases:
        - 1.1.0
      cve: CVE-1970-0002
      github_issue_id: 4294967297
      vuln_report_id: GO-1970-0002
      credits:
        - a confused poet
    - id: 10003
      package: golang.org/x/net/http2
      track: PRIVATE
      changelists:
        - https://go-internal-review.git.corp.google.com/c/net/+/4444
        - https://go-internal-review.git.corp.google.com/c/net/+/5555
      release_note: |
        net/http2: turbulence in the frame buffers causes gophers to levitate.

        Sending a specially crafted SETTINGS frame with the
        ENABLE_LEVITATION=1 causes all subsequent gophers to
        float indefinitely.

        Thanks to a very levitated gopher for reporting this issue.

        This is CVE-1970-0003 and Go issue https://go.dev/issue/4294967298.
      target_releases:
        - 1.1.0
      cve: CVE-1970-0003
      github_issue_id: 4294967298
      vuln_report_id: GO-1970-0003
      credits:
        - a very levitated gopher`

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
	// privCommit3 := netRepo.CommitOnBranch("master", map[string]string{"fix3.go": "package fix"})
	// netRepo.runGit("update-ref", "refs/changes/3333/1", privCommit3)
	privCommit4 := netRepo.CommitOnBranch("master", map[string]string{"fix4.go": "package fix"})
	netRepo.runGit("update-ref", "refs/changes/4444/1", privCommit4)
	privCommit5 := netRepo.CommitOnBranch("master", map[string]string{"fix5.go": "package fix"})
	netRepo.runGit("update-ref", "refs/changes/5555/1", privCommit5)

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
			"4444": {
				ID:              "4444",
				ChangeID:        "4444",
				ChangeNumber:    4444,
				Project:         "net",
				Branch:          "public",
				Submittable:     true,
				CurrentRevision: "rev4444",
				Status:          gerrit.ChangeStatusMerged,
				Revisions: map[string]gerrit.RevisionInfo{
					"rev4444": {
						Fetch: map[string]*gerrit.FetchInfo{
							"http": {
								URL: netRepo.dir.dir,
								Ref: "refs/changes/4444/1",
							},
						},
					},
				},
			},
			"5555": {
				ID:              "5555",
				ChangeID:        "5555",
				ChangeNumber:    5555,
				Project:         "net",
				Branch:          "public",
				Submittable:     true,
				CurrentRevision: "rev5555",
				Status:          gerrit.ChangeStatusMerged,
				Revisions: map[string]gerrit.RevisionInfo{
					"rev5555": {
						Fetch: map[string]*gerrit.FetchInfo{
							"http": {
								URL: netRepo.dir.dir,
								Ref: "refs/changes/5555/1",
							},
						},
					},
				},
			},
		},
	}
	publicHead, err := netRepo.dir.RunCommand(context.Background(), "rev-parse", "public")
	if err != nil {
		t.Fatal(err)
	}
	for id := range privGerrit.changes {
		privGerrit.clBases[id] = strings.TrimSpace(string(publicHead))
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

	vulndbRepo := NewFakeRepo(t, "vulndb")
	vulndbBase := vulndbRepo.CommitOnBranch("master", map[string]string{"README": "vulndb"})

	pubBase, _ := strings.CutSuffix(pubRepo.dir.dir, filepath.Base(pubRepo.dir.dir))
	pubGerrit := NewFakeGerrit(t, pubRepo, vulndbRepo)
	pubGerrit.ConsiderChangeSubmitted(pubRepo, "558675")

	fakeGH := &FakeGitHub{Issues: map[int]*github.Issue{
		4294967296: {Number: github.Ptr(4294967296)},
		4294967297: {Number: github.Ptr(4294967297)},
		4294967298: {Number: github.Ptr(4294967298)},
	}}
	orderedGH := &fakePrivXGitHub{
		FakeGitHub: fakeGH,
		t:          t,
		gerrit:     pubGerrit,
		vulndbBase: vulndbBase,
	}

	var announcementHeader MailHeader
	var announcementMessage MailContent
	p := &PrivXPatch{
		Git:           &Git{},
		PrivateGerrit: privGerrit,
		PublicGerrit:  pubGerrit,
		PublicRepoURL: func(repo string) string {
			return pubBase + "/" + repo
		},
		GitHub:        orderedGH,
		ApproveAction: func(*wf.TaskContext) error { return nil },
		SendMail: func(_ *wf.TaskContext, mh MailHeader, mc MailContent) error {
			announcementHeader, announcementMessage = mh, mc
			return nil
		},
		AnnounceMailHeader: MailHeader{
			From: mail.Address{Address: "security@golang.org"},
			To:   mail.Address{Address: "golang-announce@googlegroups.com"},
		},
		AwaitAnnounceMail: func(_ *wf.TaskContext, m SentMail) (string, error) {
			return "https://groups.google.com/g/golang-announce/c/test", nil
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

	if diff := cmp.Diff(p.AnnounceMailHeader, announcementHeader); diff != "" {
		t.Errorf("announcement header mismatch (-want +got):\n%s", diff)
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

net/http2: turbulence in the frame buffers causes gophers to levitate.

Sending a specially crafted SETTINGS frame with the
ENABLE_LEVITATION=1 causes all subsequent gophers to
float indefinitely.

Thanks to a very levitated gopher for reporting this issue.

This is CVE-1970-0003 and Go issue https://go.dev/issue/4294967298.

Cheers,
Go Security team
`
	if diff := cmp.Diff(wantText, announcementMessage.BodyText); diff != "" {
		t.Errorf("announcement text mismatch (-want +got):\n%s", diff)
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
<p>net/http2: turbulence in the frame buffers causes gophers to levitate.</p>
<p>Sending a specially crafted SETTINGS frame with the<br>
ENABLE_LEVITATION=1 causes all subsequent gophers to<br>
float indefinitely.</p>
<p>Thanks to a very levitated gopher for reporting this issue.</p>
<p>This is CVE-1970-0003 and Go issue <a href="https://go.dev/issue/4294967298">https://go.dev/issue/4294967298</a>.</p>
<p>Cheers,<br>
Go Security team</p>
`
	if diff := cmp.Diff(wantHTML, announcementMessage.BodyHTML); diff != "" {
		t.Errorf("announcement HTML mismatch (-want +got):\n%s", diff)
	}

	// Verify that vuln reports were submitted to vulndb.
	vulndbHead, err := pubGerrit.ReadBranchHead(ctx, "vulndb", "master")
	if err != nil {
		t.Fatal(err)
	}
	var rm relmeta.ReleaseMilestone
	if err := yaml.Unmarshal([]byte(privXMilestoneYAML), &rm); err != nil {
		t.Fatal(err)
	}

	const announceURL = "https://groups.google.com/g/golang-announce/c/test"
	for _, p := range rm.Patches {
		reportPath := path.Join("data", "reports", p.VulnReportID+".yaml")
		b, err := pubGerrit.ReadFile(ctx, "vulndb", vulndbHead, reportPath)
		if err != nil {
			t.Fatalf("patch %d: reading %s: %v", p.ID, reportPath, err)
		}
		if !bytes.Contains(b, []byte(announceURL)) {
			t.Errorf("patch %d: report %s does not contain %s", p.ID, reportPath, announceURL)
		}
		var vr report.Report
		if err := yaml.Unmarshal(b, &vr); err != nil {
			t.Fatalf("patch %d: unmarshal %s: %v", p.ID, reportPath, err)
		}

		if vr.ID != p.VulnReportID {
			t.Errorf("patch %d: ID = %q, want %q", p.ID, vr.ID, p.VulnReportID)
		}

		// Module and package.
		if len(vr.Modules) != 1 {
			t.Errorf("patch %d: got %d modules, want 1", p.ID, len(vr.Modules))
			continue
		}
		mod := vr.Modules[0]
		if mod.Module != "golang.org/x/net" {
			t.Errorf("patch %d: module = %q, want %q", p.ID, mod.Module, "golang.org/x/net")
		}
		if len(mod.Packages) != 1 {
			t.Errorf("patch %d: got %d packages, want 1", p.ID, len(mod.Packages))
			continue
		}
		if mod.Packages[0].Package != p.Package {
			t.Errorf("patch %d: package = %q, want %q", p.ID, mod.Packages[0].Package, p.Package)
		}

		// CVE metadata.
		if vr.CVEMetadata == nil {
			t.Errorf("patch %d: CVEMetadata is nil", p.ID)
			continue
		}
		if vr.CVEMetadata.ID != p.CVE {
			t.Errorf("patch %d: CVEMetadata.ID = %q, want %q", p.ID, vr.CVEMetadata.ID, p.CVE)
		}

		// Credits.
		if diff := cmp.Diff(p.Credits, vr.Credits); diff != "" {
			t.Errorf("patch %d: credits mismatch (-want +got):\n%s", p.ID, diff)
		}

		// Description must be non-empty.
		if vr.Description == "" {
			t.Errorf("patch %d: description is empty", p.ID)
		}

		// Summary must be non-empty.
		if vr.Summary == "" {
			t.Errorf("patch %d: summary is empty", p.ID)
		}

		// ReviewStatus must be Reviewed.
		if vr.ReviewStatus != report.Reviewed {
			t.Errorf("patch %d: review_status = %v, want Reviewed", p.ID, vr.ReviewStatus)
		}

		// Source metadata.
		if vr.SourceMeta == nil || vr.SourceMeta.ID != "go-security-team" {
			t.Errorf("patch %d: source meta = %v, want id=go-security-team", p.ID, vr.SourceMeta)
		}

		// References: must contain a REPORT ref for the GitHub issue, FIX refs
		// for each changelist, and a WEB ref for the announcement URL.
		refsByType := map[report.ReferenceType][]string{}
		for _, ref := range vr.References {
			refsByType[ref.Type] = append(refsByType[ref.Type], ref.URL)
		}
		if p.GitHubIssueID != 0 {
			wantIssueURL := fmt.Sprintf("https://go.dev/issue/%d", p.GitHubIssueID)
			if urls := refsByType[report.ReferenceTypeReport]; len(urls) != 1 || urls[0] != wantIssueURL {
				t.Errorf("patch %d: REPORT refs = %v, want [%s]", p.ID, urls, wantIssueURL)
			}
		}
		if got, want := len(refsByType[report.ReferenceTypeFix]), len(p.Changelists); got != want {
			t.Errorf("patch %d: got %d FIX refs, want %d", p.ID, got, want)
		}
		if urls := refsByType[report.ReferenceTypeWeb]; len(urls) != 1 || urls[0] != announceURL {
			t.Errorf("patch %d: WEB refs = %v, want [%s]", p.ID, urls, announceURL)
		}
	}

	// Verify that GitHub issues were updated with the release note + trailer.
	for _, p := range rm.Patches {
		issue, ok := fakeGH.Issues[int(p.GitHubIssueID)]
		if !ok {
			t.Errorf("patch %d: GitHub issue %d not found", p.ID, p.GitHubIssueID)
			continue
		}
		body := issue.GetBody()
		if !strings.Contains(body, p.ReleaseNote) {
			t.Errorf("patch %d: issue body missing release note", p.ID)
		}
		wantTrailer := fmt.Sprintf("This was a %s issue originally tracked in http://b/%d.", p.Track, p.ID)
		if !strings.Contains(body, wantTrailer) {
			t.Errorf("patch %d: issue body missing trailer, got:\n%s", p.ID, body)
		}
	}
}

func TestResolveVulnerableVersion(t *testing.T) {
	tests := []struct {
		name       string
		tags       []string
		cutVersion string
		want       string
		wantErr    bool
	}{
		{
			"predecessor without new tag in list",
			[]string{"v0.1.0", "v0.2.0", "v0.3.0"},
			"v0.4.0",
			"0.3.0",
			false,
		},
		{
			"predecessor with new tag in list",
			[]string{"v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0"},
			"v0.4.0",
			"0.3.0",
			false,
		},
		{
			"non-version tags ignored",
			[]string{"v0.5.0", "release", "nightly", "v0.6.0"},
			"v0.7.0",
			"0.6.0",
			false,
		},
		{
			"semantic ordering not lexical",
			[]string{"v0.2.0", "v0.10.0", "v0.9.0"},
			"v0.11.0",
			"0.10.0",
			false,
		},
		{
			"no predecessor",
			[]string{"v0.1.0"},
			"v0.1.0",
			"",
			true,
		},
		{
			"no version tags",
			[]string{"release", "nightly"},
			"v0.1.0",
			"",
			true,
		},
		{
			"no tags at all",
			nil,
			"v0.1.0",
			"",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewFakeRepo(t, "net")
			head := repo.Commit(map[string]string{"go.mod": "module golang.org/x/net\n"})
			for _, tag := range tt.tags {
				repo.Tag(tag, head)
			}

			fg := NewFakeGerrit(t, repo)
			x := &PrivXPatch{PublicGerrit: fg}
			ctx := &wf.TaskContext{
				Context: context.Background(),
				Logger:  &testLogger{t: t},
			}

			tagged := TagRepo{Name: "net", NewerVersion: tt.cutVersion}
			got, err := x.ResolveVulnerableVersion(ctx, tagged)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveVulnerableVersion: err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			want := report.VulnerableAt(tt.want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ResolveVulnerableVersion = %v, want %v", got, want)
			}
		})
	}
}

func TestUpdateGitHubIssues(t *testing.T) {
	ctx := &wf.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t: t},
	}

	t.Run("nil milestone", func(t *testing.T) {
		if err := UpdateGitHubIssues(ctx, nil, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("patches", func(t *testing.T) {
		fakeGH := &FakeGitHub{}
		rm := &relmeta.ReleaseMilestone{
			Patches: []*relmeta.SecurityPatch{
				{
					ID:            100,
					Track:         relmeta.Private,
					Package:       "crypto/tls",
					ReleaseNote:   "crypto/tls: bad handshake.\n\nA crafted ClientHello causes a panic.",
					GitHubIssueID: 11111,
				},
				{
					ID:            200,
					Track:         relmeta.Public,
					Package:       "net/http",
					ReleaseNote:   "net/http: request smuggling.\n\nMalformed headers bypass validation.",
					GitHubIssueID: 22222,
				},
			},
		}
		if err := UpdateGitHubIssues(ctx, fakeGH, rm); err != nil {
			t.Fatalf("UpdateGitHubIssues: %v", err)
		}
		for _, p := range rm.Patches {
			issue, ok := fakeGH.Issues[int(p.GitHubIssueID)]
			if !ok {
				t.Errorf("patch %d: GitHub issue %d not found", p.ID, p.GitHubIssueID)
				continue
			}
			body := issue.GetBody()
			if !strings.Contains(body, p.ReleaseNote) {
				t.Errorf("patch %d: issue body missing release note", p.ID)
			}
			wantTrailer := fmt.Sprintf("This was a %s issue originally tracked in http://b/%d.", p.Track, p.ID)
			if !strings.Contains(body, wantTrailer) {
				t.Errorf("patch %d: issue body missing trailer, got:\n%s", p.ID, body)
			}
		}
	})
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
