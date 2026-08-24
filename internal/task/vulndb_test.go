// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"path"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/relmeta"
	"golang.org/x/vulndb/report"
	yaml "gopkg.in/yaml.v3"
)

func TestStartsWithASCII(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"hello", true},
		{"Hello", true},
		{"zoo", true},
		{"Zoo", true},
		{"aBC", true},
		{"ZBC", true},
		{"1abc", false},
		{" abc", false},
		{"", false},
		{"日本語", false},
		{"{bad", false},
		{"@at", false},
		{"[bracket", false},
	}
	for _, tt := range tests {
		if got := startsWithASCII(tt.in); got != tt.want {
			t.Errorf("startsWithAscii(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSubject(t *testing.T) {
	reports := []*report.Report{
		{ID: "GO-2026-0001"},
		{ID: "GO-2026-0002"},
	}
	got := Subject(reports)
	if !strings.Contains(got, "add 2 first party reports") {
		t.Errorf("subject missing report count:\n%s", got)
	}
	if !strings.Contains(got, "Fixes golang/vulndb#0001") {
		t.Errorf("subject missing first fix line:\n%s", got)
	}
	if !strings.Contains(got, "Fixes golang/vulndb#0002") {
		t.Errorf("subject missing second fix line:\n%s", got)
	}
}

func TestVulnReportVersions(t *testing.T) {
	tests := []struct {
		name    string
		targets []string
		want    report.Versions
		wantErr bool
	}{
		{
			name:    "single",
			targets: []string{"1.1.0"},
			want:    report.Versions{report.Fixed("v1.1.0")},
		},
		{
			name:    "two versions",
			targets: []string{"1.24.1", "1.23.5"},
			want: report.Versions{
				report.Fixed("v1.23.5"),
				report.Introduced("v1.24.0-0"),
				report.Fixed("v1.24.1"),
			},
		},
		{
			name:    "empty",
			targets: nil,
			wantErr: true,
		},
		{
			name:    "invalid semver",
			targets: []string{"not-a-version"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VulnReportVersions(tt.targets)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VulnReportVersions(%v): err = %v, wantErr = %v", tt.targets, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("VulnReportVersions(%v):\ngot  %v\nwant %v", tt.targets, got, tt.want)
			}
		})
	}
}

func TestVulnReport(t *testing.T) {
	mod := VulnModuleInfo{Module: "golang.org/x/net", VulnerableAt: report.VulnerableAt("1.0.0")}
	const announceURL = "https://groups.google.com/g/golang-announce/c/test"

	t.Run("valid", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "golang.org/x/net/http2",
			GitHubIssueID:  12345,
			Changelists:    []string{"https://go.dev/cl/111"},
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "net/http2: bad things happen.\n\nDetails about the bad things.",
			CVE:            "CVE-2026-0001",
			CWE:            "CWE-400",
			Credits:        []string{"Alice"},
			VulnReportID:   "GO-2026-0001",
		}
		r, err := VulnReport(p, mod, announceURL)
		if err != nil {
			t.Fatal(err)
		}
		if r.ID != "GO-2026-0001" {
			t.Errorf("ID = %q, want GO-2026-0001", r.ID)
		}
		if got := string(r.Summary); got != "Bad things happen in net/http2" {
			t.Errorf("Summary = %q", got)
		}
		if got := string(r.Description); got != "Details about the bad things." {
			t.Errorf("Description = %q", got)
		}
		if r.CVEMetadata.ID != "CVE-2026-0001" {
			t.Errorf("CVE = %q", r.CVEMetadata.ID)
		}
		if len(r.Modules) != 1 || r.Modules[0].Module != "golang.org/x/net" {
			t.Errorf("Module = %v", r.Modules)
		}
		if got := r.Modules[0].VulnerableAt; got == nil || got.Version != "1.0.0" {
			t.Errorf("VulnerableAt = %v, want 1.0.0", got)
		}
		if r.Modules[0].Packages[0].Package != "golang.org/x/net/http2" {
			t.Errorf("Package = %q", r.Modules[0].Packages[0].Package)
		}
		if r.ReviewStatus != report.Reviewed {
			t.Errorf("ReviewStatus = %v", r.ReviewStatus)
		}
	})

	t.Run("dotted identifier preserves interior periods", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "net/http",
			GitHubIssueID:  12345,
			Changelists:    []string{"https://go.dev/cl/111"},
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "net/http: TLS 1.3 handshake panics.\n\nDetails about the panic.",
			CVE:            "CVE-2026-0002",
			CWE:            "CWE-400",
			Credits:        []string{"Bob"},
			VulnReportID:   "GO-2026-0002",
		}
		stdMod := VulnModuleInfo{Module: "std", VulnerableAt: report.VulnerableAt("1.0.0")}
		r, err := VulnReport(p, stdMod, announceURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(r.Summary); got != "TLS 1.3 handshake panics in net/http" {
			t.Errorf("Summary = %q, want %q", got, "TLS 1.3 handshake panics in net/http")
		}
	})

	t.Run("VulnReportDesc override", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "golang.org/x/net/http2",
			GitHubIssueID:  12345,
			Changelists:    []string{"https://go.dev/cl/111"},
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "net/http2: bad things happen.\n\nOriginal description.",
			VulnReportDesc: "Overridden description.",
			VulnReportID:   "GO-2026-0001",
			CVE:            "CVE-2026-0001",
		}
		r, err := VulnReport(p, mod, announceURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(r.Description); got != "Overridden description." {
			t.Errorf("Description = %q, want overridden", got)
		}
	})

	t.Run("missing github issue", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "golang.org/x/net/http2",
			Changelists:    []string{"https://go.dev/cl/111"},
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "net/http2: bad.\n\nDetails.",
		}
		if _, err := VulnReport(p, mod, announceURL); err == nil {
			t.Fatal("expected error for missing github issue")
		}
	})

	t.Run("missing changelists", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "golang.org/x/net/http2",
			GitHubIssueID:  12345,
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "net/http2: bad.\n\nDetails.",
		}
		if _, err := VulnReport(p, mod, announceURL); err == nil {
			t.Fatal("expected error for missing changelists")
		}
	})

	t.Run("missing announce URL", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "golang.org/x/net/http2",
			GitHubIssueID:  12345,
			Changelists:    []string{"https://go.dev/cl/111"},
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "net/http2: bad.\n\nDetails.",
		}
		if _, err := VulnReport(p, mod, ""); err == nil {
			t.Fatal("expected error for missing announce URL")
		}
	})

	t.Run("malformed release note no newline", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "golang.org/x/net/http2",
			GitHubIssueID:  12345,
			Changelists:    []string{"https://go.dev/cl/111"},
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "no newline here",
		}
		if _, err := VulnReport(p, mod, announceURL); err == nil {
			t.Fatal("expected error for malformed release note")
		}
	})

	t.Run("malformed release note no colon", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "golang.org/x/net/http2",
			GitHubIssueID:  12345,
			Changelists:    []string{"https://go.dev/cl/111"},
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "no colon in subject\n\nDetails.",
		}
		if _, err := VulnReport(p, mod, announceURL); err == nil {
			t.Fatal("expected error for malformed subject")
		}
	})

	t.Run("non-ascii summary", func(t *testing.T) {
		p := &relmeta.SecurityPatch{
			Package:        "golang.org/x/net/http2",
			GitHubIssueID:  12345,
			Changelists:    []string{"https://go.dev/cl/111"},
			TargetReleases: []string{"1.1.0"},
			ReleaseNote:    "net/http2: 日本語\n\nDetails.",
		}
		if _, err := VulnReport(p, mod, announceURL); err == nil {
			t.Fatal("expected error for non-ascii summary")
		}
	})
}

func TestVulnModule(t *testing.T) {
	tests := []struct {
		pkg  string
		want string
	}{
		{"net/http", "std"},
		{"crypto/tls", "std"},
		{"cmd/go", "cmd"},
		{"cmd", "cmd"},
		{"cmd/compile/internal/ssa", "cmd"},
		{"golang.org/x/net/http2", "golang.org/x/net"},
		{"golang.org/x/crypto/ssh", "golang.org/x/crypto"},
		{"golang.org/x/net", "golang.org/x/net"},
		{"", "std"},
	}
	for _, tc := range tests {
		t.Run(tc.pkg, func(t *testing.T) {
			if got := VulnModule(tc.pkg); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVulnerableAtFromTargetReleases(t *testing.T) {
	tests := []struct {
		name    string
		targets []string
		wantVer string
		wantErr bool
	}{
		{
			name:    "two release lines",
			targets: []string{"1.25.10", "1.26.3"},
			wantVer: "1.26.2",
		},
		{
			name:    "single release",
			targets: []string{"1.26.3"},
			wantVer: "1.26.2",
		},
		{
			name:    "result patch zero",
			targets: []string{"1.24.1", "1.25.1"},
			wantVer: "1.25.0",
		},
		{
			name:    "empty input",
			targets: []string{},
			wantErr: true,
		},
		{
			name:    "patch zero",
			targets: []string{"1.26.0"},
			wantErr: true,
		},
		{
			name:    "invalid semver",
			targets: []string{"not.a.version"},
			wantErr: true,
		},
		{
			name:    "two component version",
			targets: []string{"1.26"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VulnerableAtFromTargetReleases(tt.targets)
			if (err != nil) != tt.wantErr {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Version != tt.wantVer {
				t.Errorf("got %q, want %q", got.Version, tt.wantVer)
			}
		})
	}
}

func TestVulnerableAtFromTargetReleasesPrerelease(t *testing.T) {
	// A pre-release suffix like "1.26.3-rc1" passes semver.IsValid
	// but makes the patch extraction fail (strconv.Atoi on "3-rc1").
	_, err := VulnerableAtFromTargetReleases([]string{"1.26.3-rc1"})
	if err == nil {
		t.Fatal("expected error for pre-release target, got nil")
	}
	if !strings.Contains(err.Error(), "non-numeric patch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDeriveVulnModuleInfo(t *testing.T) {
	p := &relmeta.SecurityPatch{
		Package:        "net/http",
		TargetReleases: []string{"1.25.10", "1.26.3"},
	}
	mod, err := DeriveVulnModuleInfo(p)
	if err != nil {
		t.Fatal(err)
	}
	if mod.Module != "std" {
		t.Errorf("got %q, want std", mod.Module)
	}
	if mod.VulnerableAt == nil || mod.VulnerableAt.Version != "1.26.2" {
		t.Errorf("got %v, want 1.26.2", mod.VulnerableAt)
	}
}

type fakeVulnGerrit struct {
	*FakeGerrit

	gotInput     gerrit.ChangeInput
	gotReviewers []string
	gotFiles     map[string]string
}

func (g *fakeVulnGerrit) CreateAutoSubmitChange(ctx *wf.TaskContext, input gerrit.ChangeInput, reviewers []string, files map[string]string) (string, error) {
	g.gotInput = input
	g.gotReviewers = reviewers
	g.gotFiles = files
	return g.FakeGerrit.CreateAutoSubmitChange(ctx, input, reviewers, files)
}

func TestMailVulnReports(t *testing.T) {
	t.Run("empty reports", func(t *testing.T) {
		ctx := &wf.TaskContext{Context: context.Background(), Logger: &testLogger{t: t}}
		changeID, err := MailVulnReports(ctx, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if changeID != "" {
			t.Errorf("got change ID %q, want empty", changeID)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		vulnRepo := NewFakeRepo(t, "vulndb")
		gc := &fakeVulnGerrit{
			FakeGerrit: NewFakeGerrit(t, vulnRepo),
		}
		ctx := &wf.TaskContext{Context: context.Background(), Logger: &testLogger{t: t}}

		reports := []*report.Report{
			{ID: "GO-2026-0001"},
			{ID: "GO-2026-0002"},
		}
		wantReviewers := []string{"reviewer-a@google.com", "reviewer-b@google.com"}
		changeID, err := MailVulnReports(ctx, gc, reports, wantReviewers)
		if err != nil {
			t.Fatal(err)
		}
		if changeID == "" {
			t.Fatal("expected non-empty change ID")
		}
		if gc.gotInput.Project != "vulndb" {
			t.Errorf("project = %q, want vulndb", gc.gotInput.Project)
		}
		if gc.gotInput.Branch != "master" {
			t.Errorf("branch = %q, want master", gc.gotInput.Branch)
		}
		if !reflect.DeepEqual(gc.gotReviewers, wantReviewers) {
			t.Errorf("reviewers = %v, want %v", gc.gotReviewers, wantReviewers)
		}
		for _, id := range []string{"GO-2026-0001", "GO-2026-0002"} {
			key := path.Join("data", "reports", id+".yaml")
			content, ok := gc.gotFiles[key]
			if !ok {
				t.Errorf("missing file %q in submitted files", key)
				continue
			}
			var got report.Report
			if err := yaml.Unmarshal([]byte(content), &got); err != nil {
				t.Errorf("unmarshal %q: %v", key, err)
				continue
			}
			if got.ID != id {
				t.Errorf("file %q: ID = %q, want %q", key, got.ID, id)
			}
		}
	})
}
