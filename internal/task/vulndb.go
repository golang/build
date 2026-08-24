// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/relmeta"
	"golang.org/x/mod/semver"
	"golang.org/x/vulndb/report"
	"gopkg.in/yaml.v3"
)

// VulnReport derives a complete [report.Report] using p, mod, and announceURL.
func VulnReport(p *relmeta.SecurityPatch, mod VulnModuleInfo, announceURL string) (*report.Report, error) {
	if p.GitHubIssueID == 0 {
		return nil, errors.New("missing github issue id")
	}
	refs := []*report.Reference{
		{
			Type: report.ReferenceTypeReport,
			URL:  fmt.Sprintf("https://go.dev/issue/%d", p.GitHubIssueID),
		},
	}

	if len(p.Changelists) == 0 {
		return nil, errors.New("missing changelists")
	}
	for _, cl := range p.Changelists {
		refs = append(refs, &report.Reference{
			Type: report.ReferenceTypeFix,
			URL:  cl,
		})
	}

	if announceURL == "" {
		return nil, errors.New("missing golang-announce url")
	}
	refs = append(refs, &report.Reference{
		Type: report.ReferenceTypeWeb,
		URL:  announceURL,
	})

	versions, err := VulnReportVersions(p.TargetReleases)
	if err != nil {
		return nil, err
	}

	// Derive the vuln report's summary and description
	// from the release note, using the VulnReportDesc
	// field if it is set.
	//
	// We expect these fields to be extremely well-formed
	// and correctly linted upstream; therefore, we make
	// no attempt at handling errors beyond failing the
	// workflow in its tracks.
	subject, body, found := strings.Cut(p.ReleaseNote, "\n")
	if !found {
		return nil, fmt.Errorf("malformed release note: %q", p.ReleaseNote)
	}
	body = strings.TrimSpace(body)
	if p.VulnReportDesc != "" {
		body = p.VulnReportDesc
	}
	pkg, summary, found := strings.Cut(subject, ":")
	if !found {
		return nil, fmt.Errorf("malformed release note subject: %q", subject)
	}
	summary = strings.TrimSpace(summary)
	summary = strings.TrimSuffix(summary, ".")
	if len(summary) == 0 || !startsWithASCII(summary) {
		return nil, fmt.Errorf("malformed release note subject: %q", subject)
	}
	summary = fmt.Sprintf("%s%s in %s", strings.ToUpper(summary[:1]), summary[1:], pkg)

	// At this point, we have a complete vuln report that
	// is ready to lint and generate the osv/cve5 entries.
	r := &report.Report{
		ID: p.VulnReportID,
		Modules: []*report.Module{
			{
				Module:       mod.Module,
				Versions:     versions,
				VulnerableAt: mod.VulnerableAt,
				// TODO(nealpatel): for completeness, we should
				// support the edge case of vendored x/ repo fix
				// having a std counterpart; however, for now, in
				// the worst case, we have to pull down a mailed
				// CL, make a small edit, re-push to submit.
				Packages: []*report.Package{{Package: p.Package}},
			},
		},
		Summary:      report.Summary(summary),
		Description:  report.Description(body),
		Credits:      p.Credits,
		References:   refs,
		CVEMetadata:  &report.CVEMeta{ID: p.CVE, CWE: p.CWE},
		SourceMeta:   &report.SourceMeta{ID: "go-security-team"},
		ReviewStatus: report.Reviewed,
	}

	return r, nil
}

type VulnModuleInfo struct {
	Module       string
	VulnerableAt *report.Version
}

// VulnReportVersions constructs the expected
// list of [report.Versions] that flags which
// major.minor.patch semvers are affected.
//
// targetReleases are expected to conform to
// the [report.Version] semantics which notably
// do not include the prefixed 'v'.
func VulnReportVersions(targetReleases []string) (report.Versions, error) {
	if len(targetReleases) == 0 {
		return nil, errors.New("missing target releases")
	}
	semVers := make([]string, len(targetReleases))
	for i := range targetReleases {
		semVers[i] = "v" + targetReleases[i]
		if !semver.IsValid(semVers[i]) {
			return nil, fmt.Errorf("invalid semver %q (target: %q)", semVers[i], targetReleases[i])
		}
	}
	semver.Sort(semVers)
	var versions report.Versions
	for i, v := range semVers {
		// If a second Go semantic version is
		// present, this indicates a minor point
		// release wherein we use a semantically
		// zero sorted version to break up the
		// various supported Go versions as a
		// standard convention in golang/vulndb.
		if i > 0 {
			mm := semver.MajorMinor(v) + ".0-0"
			versions = append(versions, report.Introduced(mm))
		}
		versions = append(versions, report.Fixed(v))
	}
	return versions, nil
}

// DeriveVulnModuleInfo derives the [VulnModuleInfo] for a std/cmd
// security patch without a network call. The module value is taken
// from [VulnModule] and the VulnerableAt version is derived from
// its [VulnerableAtFromTargetReleases].
//
// The x-repo path uses [ResolveVulnerableVersion] instead, which
// resolves over the network; see [PrivXPatch.CreateVulnReports].
func DeriveVulnModuleInfo(p *relmeta.SecurityPatch) (VulnModuleInfo, error) {
	vulnerableAt, err := VulnerableAtFromTargetReleases(p.TargetReleases)
	if err != nil {
		return VulnModuleInfo{}, err
	}
	return VulnModuleInfo{
		Module:       VulnModule(p.Package),
		VulnerableAt: vulnerableAt,
	}, nil
}

func VulnModule(pkg string) string {
	if pkg == "cmd" || strings.HasPrefix(pkg, "cmd/") {
		return "cmd"
	}
	if rest, ok := strings.CutPrefix(pkg, "golang.org/x/"); ok {
		if repo, _, _ := strings.Cut(rest, "/"); repo != "" {
			return "golang.org/x/" + repo
		}
	}
	return "std"
}

func VulnerableAtFromTargetReleases(targetReleases []string) (*report.Version, error) {
	if len(targetReleases) == 0 {
		return nil, errors.New("missing target releases")
	}
	semVers := make([]string, len(targetReleases))
	for i := range targetReleases {
		semVers[i] = "v" + targetReleases[i]
		if !semver.IsValid(semVers[i]) {
			return nil, fmt.Errorf("invalid semver %q (target: %q)", semVers[i], targetReleases[i])
		}
	}
	semver.Sort(semVers)
	highest := semVers[len(semVers)-1] // e.g. "v1.26.3"
	mm := semver.MajorMinor(highest)   // e.g. "v1.26"
	if len(highest) <= len(mm)+1 {
		return nil, fmt.Errorf("version %q has no patch component", highest)
	}
	patch, err := strconv.Atoi(highest[len(mm)+1:])
	if err != nil {
		return nil, fmt.Errorf("non-numeric patch in %q: %w", highest, err)
	}
	if patch <= 0 {
		return nil, fmt.Errorf("patch component is %d in highest fixed %q; cannot derive vulnerable_at", patch, highest)
	}
	// Strip the leading "v" from MajorMinor; vulndb versions are bare.
	return report.VulnerableAt(fmt.Sprintf("%s.%d", mm[1:], patch-1)), nil
}

// MailVulnReports prepares the canonical [report.Report] for all
// patches and mails the change for golang/vulndb:master.
//
// It's a precondition that reviewers contain users with @google.com
// email domains; if this precondition is violated, the resulting CL
// may be routed to the wrong reviewers (harmless).
func MailVulnReports(ctx *wf.TaskContext, gc GerritClient, reports []*report.Report, reviewers []string) (string, error) {
	if len(reports) == 0 {
		return "", nil
	}

	mailed, err := vulnReportsCL(ctx, gc, reports)
	if err != nil {
		return "", fmt.Errorf("checking open CLs: %w", err)
	}
	if mailed != "" {
		return mailed, nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "vulndb",
		Branch:  "master",
		Subject: Subject(reports),
	}
	files := make(map[string]string, len(reports))
	for _, r := range reports {
		buf, err := yaml.Marshal(r)
		if err != nil {
			return "", err
		}
		files[path.Join("data", "reports", r.ID+".yaml")] = string(buf)
	}
	// TODO(nealpatel): Trybots are expected to fail for this change
	// since it does not generate the reports that the upstream CI expects.
	return gc.CreateAutoSubmitChange(ctx, changeInput, reviewers, files)
}

func vulnReportsCL(ctx *wf.TaskContext, gc GerritClient, reports []*report.Report) (string, error) {
	cls, err := gc.QueryChanges(ctx, "project:vulndb branch:master status:open")
	if err != nil {
		return "", err
	}
	var fixes []string
	for _, r := range reports {
		i := strings.LastIndex(r.ID, "-")
		fixes = append(fixes, "Fixes golang/vulndb#"+r.ID[i+1:])
	}
	for _, cl := range cls {
		msg, err := gc.GetCommitMessage(ctx, cl.ID)
		if err != nil {
			return "", fmt.Errorf("getting commit message for CL %s: %w", cl.ID, err)
		}
		for line := range strings.SplitSeq(msg, "\n") {
			if slices.Contains(fixes, strings.TrimSpace(line)) {
				return cl.ID, nil
			}
		}
	}
	return "", nil
}

func startsWithASCII(s string) bool {
	return len(s) > 0 && s[0]|0x20 >= 'a' && s[0]|0x20 <= 'z'
}

// Subject takes the reports and crafts the
// standardized golang/vulndb commit message.
func Subject(reports []*report.Report) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "data/reports: add %d first party reports", len(reports))
	sb.WriteString("\n\n")
	for _, r := range reports {
		i := strings.LastIndex(r.ID, "-")
		fmt.Fprintf(&sb, "Fixes golang/vulndb#%s\n", r.ID[i+1:])
	}
	// TODO(nealpatel) Do we need to put the change-id here?
	return sb.String()
}
