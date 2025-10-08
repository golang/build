// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	goversion "go/version"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/go-cmp/cmp"
	pb "go.chromium.org/luci/buildbucket/proto"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/gcsfs"
	"golang.org/x/build/internal/installer/darwinpkg"
	"golang.org/x/build/internal/installer/windowsmsi"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/relui/groups"
	"golang.org/x/build/internal/relui/sign"
	"golang.org/x/build/internal/task"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/exp/maps"
	"golang.org/x/net/context/ctxhttp"
	"google.golang.org/protobuf/types/known/structpb"
)

// DefinitionHolder holds workflow definitions.
type DefinitionHolder struct {
	mu          sync.Mutex
	definitions map[string]*wf.Definition
}

// NewDefinitionHolder creates a new DefinitionHolder,
// initialized with a sample "echo" wf.
func NewDefinitionHolder() *DefinitionHolder {
	return &DefinitionHolder{definitions: map[string]*wf.Definition{
		"echo": newEchoWorkflow(),
	}}
}

// Definition returns the initialized wf.Definition registered
// for a given name.
func (h *DefinitionHolder) Definition(name string) *wf.Definition {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.definitions[name]
}

// RegisterDefinition registers a definition with a name.
// If a definition with the same name already exists, RegisterDefinition panics.
func (h *DefinitionHolder) RegisterDefinition(name string, d *wf.Definition) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exist := h.definitions[name]; exist {
		panic("relui: multiple registrations for " + name)
	}
	h.definitions[name] = d
}

// Definitions returns the names of all registered definitions.
func (h *DefinitionHolder) Definitions() map[string]*wf.Definition {
	h.mu.Lock()
	defer h.mu.Unlock()
	defs := make(map[string]*wf.Definition)
	for k, v := range h.definitions {
		defs[k] = v
	}
	return defs
}

// Release parameter definitions.
var (
	targetDateParam = wf.ParamDef[task.Date]{
		Name: "Target Release Date",
		ParamType: wf.ParamType[task.Date]{
			HTMLElement:   "input",
			HTMLInputType: "date",
		},
		Doc: `Target Release Date is the date on which the release is scheduled.

It must be three to seven days after the pre-announcement as documented in the security policy.`,
	}
	securityPreAnnParam = wf.ParamDef[string]{
		Name: "Security Content",
		ParamType: wf.ParamType[string]{
			HTMLElement: "select",
			HTMLSelectOptions: []string{
				"the standard library",
				"the toolchain",
				"the standard library and the toolchain",
			},
		},
		Doc: `Security Content is the security content to be included in the release pre-announcement.

It must not reveal details beyond what's allowed by the security policy.`,
	}
	securityPreAnnCVEsParam = wf.ParamDef[[]string]{
		Name:      "PRIVATE-track CVEs",
		ParamType: wf.SliceShort,
		Example:   "CVE-2023-XXXX",
		Doc:       "List of CVEs for PRIVATE track fixes contained in the release to be included in the pre-announcement.",
		Check: func(cves []string) error {
			var m = make(map[string]bool)
			for _, c := range cves {
				switch {
				case !cveRE.MatchString(c):
					return fmt.Errorf("CVE ID %q doesn't match %s", c, cveRE)
				case m[c]:
					return fmt.Errorf("duplicate CVE ID %q", c)
				}
				m[c] = true
			}
			return nil
		},
	}
	cveRE = regexp.MustCompile(`^CVE-\d{4}-\d{4,7}$`)

	securitySummaryParameter = wf.ParamDef[string]{
		Name: "Security Summary (optional)",
		Doc: `Security Summary is an optional sentence describing security fixes included in this release.

It shows up in the release tweet.

The empty string means there are no security fixes to highlight.

Past examples:
• "Includes a security fix for crypto/tls (CVE-2021-34558)."
• "Includes a security fix for the Wasm port (CVE-2021-38297)."
• "Includes security fixes for encoding/pem (CVE-2022-24675), crypto/elliptic (CVE-2022-28327), crypto/x509 (CVE-2022-27536)."`,
	}

	securityFixesParameter = wf.ParamDef[[]string]{
		Name:      "Security Fixes (optional)",
		ParamType: wf.SliceLong,
		Doc: `Security Fixes is a list of descriptions, one for each distinct security fix included in this release, in Markdown format.

It shows up in the announcement mail.

The empty list means there are no security fixes included.

Past examples:
• "encoding/pem: fix stack overflow in Decode

   A large (more than 5 MB) PEM input can cause a stack overflow in Decode,
   leading the program to crash.

   Thanks to Juho Nurminen of Mattermost who reported the error.

   This is CVE-2022-24675 and Go issue https://go.dev/issue/51853."
• "crypto/elliptic: tolerate all oversized scalars in generic P-256

   A crafted scalar input longer than 32 bytes can cause P256().ScalarMult
   or P256().ScalarBaseMult to panic. Indirect uses through crypto/ecdsa and
   crypto/tls are unaffected. amd64, arm64, ppc64le, and s390x are unaffected.

   This was discovered thanks to a Project Wycheproof test vector.

   This is CVE-2022-28327 and Go issue https://go.dev/issue/52075."`,
		Example: `encoding/pem: fix stack overflow in Decode

A large (more than 5 MB) PEM input can cause a stack overflow in Decode,
leading the program to crash.

Thanks to Juho Nurminen of Mattermost who reported the error.

This is CVE-2022-24675 and Go issue https://go.dev/issue/51853.`,
	}

	releaseCoordinators = wf.ParamDef[[]string]{
		Name:      "Release Coordinator Usernames (optional)",
		ParamType: wf.SliceShort,
		Doc: `Release Coordinator Usernames is an optional list of the coordinators of the release.

Their first names will be included at the end of the release announcement, and CLs will be mailed to them.`,
		Example: "heschi",
		Check:   task.CheckCoordinators,
	}
)

// newEchoWorkflow returns a runnable wf.Definition for
// development.
func newEchoWorkflow() *wf.Definition {
	wd := wf.New(wf.ACL{})
	wf.Output(wd, "greeting", wf.Task1(wd, "greeting", echo, wf.Param(wd, wf.ParamDef[string]{Name: "greeting"})))
	wf.Output(wd, "farewell", wf.Task1(wd, "farewell", echo, wf.Param(wd, wf.ParamDef[string]{Name: "farewell"})))
	return wd
}

func echo(ctx *wf.TaskContext, arg string) (string, error) {
	ctx.Printf("echo(%v, %q)", ctx, arg)
	return arg, nil
}

func checkTaskApproved(ctx *wf.TaskContext, p db.PGDBTX) (bool, error) {
	q := db.New(p)
	t, err := q.Task(ctx, db.TaskParams{
		Name:       ctx.TaskName,
		WorkflowID: ctx.WorkflowID,
	})
	if !t.ReadyForApproval {
		_, err := q.UpdateTaskReadyForApproval(ctx, db.UpdateTaskReadyForApprovalParams{
			ReadyForApproval: true,
			Name:             ctx.TaskName,
			WorkflowID:       ctx.WorkflowID,
		})
		if err != nil {
			return false, err
		}
	}
	return t.ApprovedAt.Valid, err
}

// ApproveActionDep returns a function for defining approval Actions.
//
// ApproveActionDep takes a single *pgxpool.Pool argument, which is
// used to query the database to determine if a task has been marked
// approved.
//
// ApproveActionDep marks the task as requiring approval in the
// database once the task is started. This can be used to show an
// "approve" control in the UI.
//
//	waitAction := wf.ActionN(wd, "Wait for Approval", ApproveActionDep(db), wf.After(someDependency))
func ApproveActionDep(p db.PGDBTX) func(*wf.TaskContext) error {
	return func(ctx *wf.TaskContext) error {
		_, err := task.AwaitCondition(ctx, 5*time.Second, func() (int, bool, error) {
			done, err := checkTaskApproved(ctx, p)
			return 0, done, err
		})
		return err
	}
}

// RegisterReleaseWorkflows registers workflows for issuing Go releases.
func RegisterReleaseWorkflows(ctx context.Context, h *DefinitionHolder, build *BuildReleaseTasks, milestone *task.MilestoneTasks, version *task.VersionTasks, cycle task.ReleaseCycleTasks, comm task.CommunicationTasks) error {
	// Register prod release workflows.
	if err := registerProdReleaseWorkflows(ctx, h, build, milestone, version, comm); err != nil {
		return err
	}

	// Register pre-announcement workflows.
	currentMajor, _, err := version.GetCurrentMajor(ctx)
	if err != nil {
		return err
	}
	releases := []struct {
		majors []int
	}{
		{[]int{currentMajor, currentMajor - 1}}, // Both minors.
		{[]int{currentMajor}},                   // Current minor only.
		{[]int{currentMajor - 1}},               // Previous minor only.
	}
	for _, r := range releases {
		wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})

		versions := wf.Task1(wd, "Get next versions", version.GetNextMinorVersions, wf.Const(r.majors))
		targetDate := wf.Param(wd, targetDateParam)
		securityContent := wf.Param(wd, securityPreAnnParam)
		cves := wf.Param(wd, securityPreAnnCVEsParam)
		coordinators := wf.Param(wd, releaseCoordinators)

		sentMail := wf.Task5(wd, "mail-pre-announcement", comm.PreAnnounceRelease, versions, targetDate, securityContent, cves, coordinators)
		wf.Output(wd, "Pre-announcement URL", wf.Task1(wd, "await-pre-announcement", comm.AwaitAnnounceMail, sentMail))

		var names []string
		for _, m := range r.majors {
			names = append(names, fmt.Sprintf("1.%d", m))
		}
		h.RegisterDefinition("pre-announce next minor release for Go "+strings.Join(names, " and "), wd)
	}

	// Register pre-announcement workflow for golang.org/x/ fixes.
	{
		wd := wf.New(wf.ACL{Groups: []string{groups.SecurityTeam, groups.ReleaseTeam}})

		module := wf.Param(wd, wf.ParamDef[string]{Name: "Module path", ParamType: wf.BasicString})
		pkgs := wf.Param(wd, wf.ParamDef[[]string]{Name: "Packages affected", ParamType: wf.SliceShort})
		targetDate := wf.Param(wd, targetDateParam)
		cves := wf.Param(wd, securityPreAnnCVEsParam)
		coordinators := wf.Param(wd, releaseCoordinators)

		sentMail := wf.Task5(wd, "mail-pre-announcement", comm.PreAnnounceXFix, module, pkgs, targetDate, cves, coordinators)
		wf.Output(wd, "Pre-announcement URL", wf.Task1(wd, "await-pre-announcement", comm.AwaitAnnounceMail, sentMail))

		h.RegisterDefinition("pre-announce golang.org/x security fix", wd)
	}

	// Register workflows for miscellaneous tasks that happen as part of the Go release cycle (go.dev/s/release).
	{
		// Register an "apply wait-release to CLs" workflow.
		wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})
		waited := wf.Task0(wd, "Apply wait-release to CLs", cycle.ApplyWaitReleaseCLs)
		wf.Output(wd, "waited", waited)
		h.RegisterDefinition("apply wait-release to CLs", wd)
	}
	{
		// Register a "between freeze start and RC 1" workflow.
		wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})
		devVer := wf.Const(currentMajor + 1)
		coordinators := wf.Param(wd, releaseCoordinators)

		tracking := wf.Task1(wd, "Pick release note milestone and issue", milestone.FetchRelnoteMilestoneAndIssue, devVer)

		// APIs.
		nextAPI := wf.Task2(wd, "Promote next API", cycle.PromoteNextAPI, devVer, coordinators)
		auditIssue := wf.Task3(wd, "Open API audit issue", cycle.OpenAPIAuditIssue, devVer, tracking, nextAPI)
		wf.Output(wd, "API audit issue", auditIssue)

		// Release notes.
		relnoteCLsChecked := wf.Action0(wd, "Check for open release note fragment CLs", cycle.CheckRelnoteCLs)
		nextRelnote := wf.Task3(wd, "Merge release note fragments and add to x/website", cycle.MergeNextRelnoteAndAddToWebsite, devVer, tracking, coordinators, wf.After(relnoteCLsChecked))
		relnoteTest := wf.After(nextAPI) // cmd/relnote.TestCheckAPIFragments needs API promotion to be completed.
		wf.Action4(wd, "Remove release note fragments from main repo", cycle.RemoveNextRelnoteFromMainRepo, devVer, tracking, nextRelnote, coordinators, relnoteTest)

		h.RegisterDefinition(fmt.Sprintf("between freeze start and RC 1 for Go 1.%d", currentMajor+1), wd)
	}
	{
		// Register a "ping early-in-cycle issues" workflow.
		wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})
		openTreeURL := wf.Param(wd, wf.ParamDef[string]{
			Name:    "Open Tree URL",
			Doc:     `Open Tree URL is the URL of an announcement that the tree is open for general Go 1.x development.`,
			Example: "https://groups.google.com/g/golang-dev/c/09IwUs7cxXA/m/c2jyIhECBQAJ",
			Check: func(openTreeURL string) error {
				if !strings.HasPrefix(openTreeURL, "https://groups.google.com/g/golang-dev/c/") {
					return fmt.Errorf("openTreeURL value %q doesn't begin with the usual prefix, so please double-check that the URL is correct", openTreeURL)
				}
				return nil
			},
		})
		devVer := wf.Task0(wd, "Get development version", version.GetDevelVersion)
		pinged := wf.Task2(wd, "Ping early-in-cycle issues", milestone.PingEarlyIssues, devVer, openTreeURL)
		wf.Output(wd, "pinged", pinged)
		h.RegisterDefinition("ping early-in-cycle issues in development milestone", wd)
	}
	{
		// Register an "unwait wait-release CLs" workflow.
		wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})
		unwaited := wf.Task0(wd, "Unwait wait-release CLs", cycle.UnwaitWaitReleaseCLs)
		wf.Output(wd, "unwaited", unwaited)
		h.RegisterDefinition("unwait wait-release CLs", wd)
	}

	// Register dry-run release workflows.
	registerBuildTestSignOnlyWorkflow(h, version, build, currentMajor+1, task.KindBeta)

	return nil
}

func NewAnnounceBlogPostWorkflow(comm task.SocialMediaTasks) *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})

	url := wf.Param(wd, wf.ParamDef[string]{
		Name: "Blog Post URL",
		Doc:  `URL of the blog post to announce.`,
		Check: func(url string) error {
			if !strings.HasPrefix(url, "https://go.dev/blog/") {
				return fmt.Errorf("URL must be a go.dev blog post")
			}
			return nil
		},
	})

	// allow for overriding the URL in tests
	atomURL := "https://go.dev/blog/feed.atom"
	if comm.OverrideGoBlogPostAtomURL != "" {
		atomURL = comm.OverrideGoBlogPostAtomURL
	}

	blogPost := wf.Task2(wd, "retrieve-blog-post", task.GetBlogPostMetadata, wf.Const(atomURL), url)
	tweetURL := wf.Task1(wd, "post-tweet", comm.TweetBlogPost, blogPost)
	mastodonURL := wf.Task1(wd, "post-mastodon", comm.TrumpetBlogPost, blogPost)
	blueskyURL := wf.Task1(wd, "post-bluesky", comm.SkeetBlogPost, blogPost)

	wf.Output(wd, "Blog Post", blogPost)
	wf.Output(wd, "Tweet URL", tweetURL)
	wf.Output(wd, "Mastodon URL", mastodonURL)
	wf.Output(wd, "Bluesky URL", blueskyURL)
	return wd
}

func registerProdReleaseWorkflows(ctx context.Context, h *DefinitionHolder, build *BuildReleaseTasks, milestone *task.MilestoneTasks, version *task.VersionTasks, comm task.CommunicationTasks) error {
	currentMajor, majorReleaseTime, err := version.GetCurrentMajor(ctx)
	if err != nil {
		return err
	}
	type release struct {
		major  int
		kind   task.ReleaseKind
		suffix string
	}
	releases := []release{
		{currentMajor + 1, task.KindMajor, "final"},
		{currentMajor + 1, task.KindRC, "next RC"},
		{currentMajor + 1, task.KindBeta, "next beta"},
		{currentMajor, task.KindMinor, "next minor"},     // Current minor only.
		{currentMajor - 1, task.KindMinor, "next minor"}, // Previous minor only.
	}
	if time.Since(majorReleaseTime) < 7*24*time.Hour {
		releases = append(releases, release{currentMajor, task.KindMajor, "final"})
	}
	for _, r := range releases {
		wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})

		coordinators := wf.Param(wd, releaseCoordinators)

		published := addSingleReleaseWorkflow(build, milestone, version, wd, r.major, r.kind, coordinators)

		securitySummary := wf.Const("")
		securityFixes := wf.Slice[string]()
		if r.kind == task.KindMinor || r.kind == task.KindRC {
			securitySummary = wf.Param(wd, securitySummaryParameter)
			securityFixes = wf.Param(wd, securityFixesParameter)
		}
		addCommTasks(wd, build, comm, r.kind, wf.Slice(published), securitySummary, securityFixes, coordinators)
		if r.major >= currentMajor {
			// Add a task for updating the module proxy test repo that makes sure modules containing go directives
			// of the latest published version are fetchable.
			wf.Task1(wd, "update-proxy-test", version.UpdateProxyTestRepo, published)
		}

		h.RegisterDefinition(fmt.Sprintf("Go 1.%d %s", r.major, r.suffix), wd)
	}

	wd, err := createMinorReleaseWorkflow(build, milestone, version, comm, currentMajor-1, currentMajor)
	if err != nil {
		return err
	}
	h.RegisterDefinition(fmt.Sprintf("Minor releases for Go 1.%d and 1.%d", currentMajor-1, currentMajor), wd)

	return nil
}

func registerBuildTestSignOnlyWorkflow(h *DefinitionHolder, version *task.VersionTasks, build *BuildReleaseTasks, major int, kind task.ReleaseKind) {
	wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})

	nextVersion := wf.Task2(wd, "Get next version", version.GetNextVersion, wf.Const(major), wf.Const(kind))
	branch := fmt.Sprintf("release-branch.go1.%d", major)
	if kind == task.KindBeta {
		branch = "master"
	}
	branchVal := wf.Const(branch)
	timestamp := wf.Task0(wd, "Timestamp release", now)
	versionFile := wf.Task2(wd, "Generate VERSION file", version.GenerateVersionFile, nextVersion, timestamp)
	wf.Output(wd, "VERSION file", versionFile)
	head := wf.Task1(wd, "Read branch head", version.ReadBranchHead, branchVal)
	srcSpec := wf.Task4(wd, "Select source spec", build.getGitSource, branchVal, head, wf.Const(""), versionFile)
	source, artifacts, mods := build.addBuildTasks(wd, major, kind, nextVersion, timestamp, srcSpec)
	wf.Output(wd, "Source", source)
	wf.Output(wd, "Artifacts", artifacts)
	wf.Output(wd, "Modules", mods)

	h.RegisterDefinition(fmt.Sprintf("dry-run (build, test, and sign only): Go 1.%d next beta", major), wd)
}

func createMinorReleaseWorkflow(build *BuildReleaseTasks, milestone *task.MilestoneTasks, version *task.VersionTasks, comm task.CommunicationTasks, prevMajor, currentMajor int) (*wf.Definition, error) {
	wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam}})

	coordinators := wf.Param(wd, releaseCoordinators)
	currPublished := addSingleReleaseWorkflow(build, milestone, version, wd.Sub(fmt.Sprintf("Go 1.%d", currentMajor)), currentMajor, task.KindMinor, coordinators)
	prevPublished := addSingleReleaseWorkflow(build, milestone, version, wd.Sub(fmt.Sprintf("Go 1.%d", prevMajor)), prevMajor, task.KindMinor, coordinators)

	securitySummary := wf.Param(wd, securitySummaryParameter)
	securityFixes := wf.Param(wd, securityFixesParameter)
	addCommTasks(wd, build, comm, task.KindMinor, wf.Slice(currPublished, prevPublished), securitySummary, securityFixes, coordinators)
	wf.Task1(wd, "update-proxy-test", version.UpdateProxyTestRepo, currPublished)

	return wd, nil
}

func addCommTasks(
	wd *wf.Definition, build *BuildReleaseTasks, comm task.CommunicationTasks,
	kind task.ReleaseKind, published wf.Value[[]task.Published], securitySummary wf.Value[string], securityFixes, coordinators wf.Value[[]string],
) {
	okayToAnnounce := wf.Action0(wd, "Wait to Announce", build.ApproveAction, wf.After(published))

	// Announce that a new Go release has been published.
	sentMail := wf.Task4(wd, "mail-announcement", comm.AnnounceRelease, wf.Const(kind), published, securityFixes, coordinators, wf.After(okayToAnnounce))
	announcementURL := wf.Task1(wd, "await-announcement", comm.AwaitAnnounceMail, sentMail)
	tweetURL := wf.Task4(wd, "post-tweet", comm.TweetRelease, wf.Const(kind), published, securitySummary, announcementURL, wf.After(okayToAnnounce))
	mastodonURL := wf.Task4(wd, "post-mastodon", comm.TrumpetRelease, wf.Const(kind), published, securitySummary, announcementURL, wf.After(okayToAnnounce))
	blueskyURL := wf.Task4(wd, "post-bluesky", comm.SkeetRelease, wf.Const(kind), published, securitySummary, announcementURL, wf.After(okayToAnnounce))

	wf.Output(wd, "Announcement URL", announcementURL)
	wf.Output(wd, "Tweet URL", tweetURL)
	wf.Output(wd, "Mastodon URL", mastodonURL)
	wf.Output(wd, "Bluesky URL", blueskyURL)
}

func now(_ context.Context) (time.Time, error) {
	return time.Now().UTC().Round(time.Second), nil
}

func addSingleReleaseWorkflow(
	build *BuildReleaseTasks, milestone *task.MilestoneTasks, version *task.VersionTasks,
	wd *wf.Definition, major int, kind task.ReleaseKind, coordinators wf.Value[[]string],
) wf.Value[task.Published] {
	kindVal := wf.Const(kind)
	branch := fmt.Sprintf("release-branch.go1.%d", major)
	if kind == task.KindBeta {
		branch = "master"
	}
	branchVal := wf.Const(branch)
	startingHead := wf.Task1(wd, "Read starting branch head", version.ReadBranchHead, branchVal)

	// Select version, check milestones.
	nextVersion := wf.Task2(wd, "Get next version", version.GetNextVersion, wf.Const(major), kindVal)
	timestamp := wf.Task0(wd, "Timestamp release", now)
	versionFile := wf.Task2(wd, "Generate VERSION file", version.GenerateVersionFile, nextVersion, timestamp)
	wf.Output(wd, "VERSION file", versionFile)
	milestones := wf.Task2(wd, "Pick milestones", milestone.FetchMilestones, nextVersion, kindVal)
	checked := wf.Action3(wd, "Check blocking issues", milestone.CheckBlockers, milestones, nextVersion, kindVal)

	securityRef := wf.Param(wd, wf.ParamDef[string]{
		Name: "Ref from the private repository to build from (optional)",
		Doc: `This optional parameter controls where to build from.

The default workflow behavior, if this value is the empty string,
is to build from the head of the corresponding release branch
in the public Go repository (go.googlesource.com/go).
This is intended for releases with no PRIVATE-track security fixes.

If a non-empty string is entered, it must correspond to a ref
in the private repository (go-internal.googlesource.com/go).
The ref can be a branch name (e.g., "private-release-branch.go1.23.4")
or a commit hash (e.g., "8890e8372e12d3b595e0e8fec29f8d7783ab2daf").
This is intended for releases with 1+ PRIVATE-track security fixes.`,
	})
	securityCommit := wf.Task1(wd, "Read security ref", build.readSecurityRef, securityRef)
	srcSpec := wf.Task4(wd, "Select source spec", build.getGitSource, branchVal, startingHead, securityCommit, versionFile, wf.After(checked))

	// Build, test, and sign release.
	source, signedAndTestedArtifacts, modules := build.addBuildTasks(wd, major, kind, nextVersion, timestamp, srcSpec)
	waitReleaseApproval := wf.Action0(wd, "Wait for Release Coordinator Approval", build.ApproveAction, wf.After(signedAndTestedArtifacts))
	okayToTagAndPublish := wf.Action3(wd, "Re-check blocking issues", milestone.CheckBlockers, milestones, nextVersion, kindVal, wf.After(waitReleaseApproval))

	dlcl := wf.Task5(wd, "Mail DL CL", version.MailDLCL, wf.Const(major), kindVal, nextVersion, coordinators, wf.Const(false), wf.After(okayToTagAndPublish))
	dlclCommit := wf.Task2(wd, "Wait for DL CL submission", version.AwaitCL, dlcl, wf.Const(""))
	wf.Output(wd, "Download CL submitted", dlclCommit)

	// Tag version and upload to CDN/website.
	// If we're releasing a beta from master, tagging is easy; we just tag the
	// commit we started from. Otherwise, we're going to submit a VERSION CL,
	// and we need to make sure that that CL is submitted on top of the same
	// state we built from. For security releases that state may not have
	// been public when we started, but it should be now.
	tagCommit := startingHead
	if branch != "master" {
		publishingHead := wf.Task3(wd, "Check branch state matches source archive", build.checkSourceMatch, branchVal, versionFile, source, wf.After(okayToTagAndPublish))
		versionCL := wf.Task4(wd, "Mail version CL", version.CreateAutoSubmitVersionCL, branchVal, nextVersion, coordinators, versionFile, wf.After(publishingHead))
		tagCommit = wf.Task2(wd, "Wait for version CL submission", version.AwaitCL, versionCL, publishingHead)
	}
	tagged := wf.Action2(wd, "Tag version", version.TagRelease, nextVersion, tagCommit, wf.After(okayToTagAndPublish))
	uploaded := wf.Action1(wd, "Upload artifacts to CDN", build.uploadArtifacts, signedAndTestedArtifacts, wf.After(tagged))
	uploadedMods := wf.Action2(wd, "Upload modules to CDN", build.uploadModules, nextVersion, modules, wf.After(tagged))
	availableOnProxy := wf.Action2(wd, "Wait for modules on proxy.golang.org", build.awaitProxy, nextVersion, modules, wf.After(uploadedMods))
	pushed := wf.Action3(wd, "Push issues", milestone.PushIssues, milestones, nextVersion, kindVal, wf.After(tagged))
	published := wf.Task2(wd, "Publish to website", build.publishArtifacts, nextVersion, signedAndTestedArtifacts, wf.After(uploaded, availableOnProxy, pushed))

	if kind == task.KindRC || kind == task.KindMajor {
		xToolsStdlibCL := wf.Task2(wd, fmt.Sprintf("Mail x/tools stdlib CL for 1.%d", major), version.CreateUpdateStdlibIndexCL, coordinators, nextVersion, wf.After(published))
		xToolsStdlibCommit := wf.Task2(wd, "Wait for x/tools stdlib CL submission", version.AwaitCL, xToolsStdlibCL, wf.Const(""))
		wf.Output(wd, "x/tools stdlib CL submitted", xToolsStdlibCommit)

		if kind == task.KindMajor {
			repos := wf.Task0(wd, "Select repositories", version.GoDirectiveXReposTasks.SelectRepos, wf.After(published),
				wf.After(xToolsStdlibCommit) /* To start off, wait for x/tools CL to land before generating the second CL. */)
			urls := wf.Expand3(wd, "Create plan", version.GoDirectiveXReposTasks.BuildPlan, repos, wf.Const(major), coordinators)
			wf.Output(wd, "Maintained golang.org/x repos", urls)
		}
	}

	dockerResult := wf.Task1(wd, "Run and Await Google Docker build", build.runAndAwaitGoogleDockerBuild, nextVersion, wf.After(uploaded))
	wf.Output(wd, "Google Docker image status", dockerResult)

	wf.Output(wd, "Published to website", published)
	return published
}

// sourceSpec encapsulates all the information that describes a source archive.
type sourceSpec struct {
	GitilesURL, Project, Branch, Revision string
	VersionFile                           string
}

func (s *sourceSpec) ArchiveURL() string {
	return fmt.Sprintf("%s/%s/+archive/%s.tar.gz", s.GitilesURL, s.Project, s.Revision)
}

type moduleArtifact struct {
	// The target for this module.
	Target *releasetargets.Target
	// The contents of the mod and info files.
	Mod, Info string
	// The scratch path of the zip within the scratch directory.
	ZipScratch string // scratch path
}

// addBuildTasks registers tasks to build, test, and sign the release onto wd.
// It returns the resulting artifacts of various kinds.
func (tasks *BuildReleaseTasks) addBuildTasks(wd *wf.Definition, major int, kind task.ReleaseKind, version wf.Value[string], timestamp wf.Value[time.Time], sourceSpec wf.Value[sourceSpec]) (wf.Value[artifact], wf.Value[[]artifact], wf.Value[[]moduleArtifact]) {
	targets := releasetargets.TargetsForGo1Point(major)
	skipTests := wf.Param(wd, wf.ParamDef[[]string]{Name: "Targets to skip testing (or 'all') (optional)", ParamType: wf.SliceShort})

	source := wf.Task1(wd, "Build source archive", tasks.buildSource, sourceSpec)
	artifacts := []wf.Value[artifact]{source}
	var mods []wf.Value[moduleArtifact]
	var blockers []wf.Dependency

	// Build and sign binary artifacts for all targets.
	for _, target := range targets {
		wd := wd.Sub(target.Name)

		// Build release artifacts for the platform using make.bash -distpack.
		// For windows, produce both a tgz and zip -- we need tgzs to run
		// tests, even though we'll eventually publish the zips.
		var tar, zip wf.Value[artifact]
		var mod wf.Value[moduleArtifact]
		{ // Block to improve diff readability. Can be unnested later.
			distpack := wf.Task2(wd, "Build distpack", tasks.buildDistpack, wf.Const(target), source)
			reproducer := wf.Task2(wd, "Reproduce distpack on Windows", tasks.reproduceDistpack, wf.Const(target), source)
			match := wf.Action2(wd, "Check distpacks match", tasks.checkDistpacksMatch, distpack, reproducer)
			blockers = append(blockers, match)
			if target.GOOS == "windows" {
				zip = wf.Task1(wd, "Get binary from distpack", tasks.binaryArchiveFromDistpack, distpack)
				tar = wf.Task1(wd, "Convert zip to .tgz", tasks.convertZipToTGZ, zip)
			} else {
				tar = wf.Task1(wd, "Get binary from distpack", tasks.binaryArchiveFromDistpack, distpack)
			}
			mod = wf.Task1(wd, "Get module files from distpack", tasks.modFilesFromDistpack, distpack)
		}

		// Create installers and perform platform-specific signing where
		// applicable. For macOS, produce updated tgz and module zips that
		// include the signed binaries.
		switch target.GOOS {
		case "darwin":
			pkg := wf.Task1(wd, "Build PKG installer", tasks.buildDarwinPKG, tar)
			signedPKG := wf.Task2(wd, "Sign PKG installer", tasks.signArtifact, pkg, wf.Const(sign.BuildMacOS))
			signedTGZ := wf.Task2(wd, "Merge signed files into .tgz", tasks.mergeSignedToTGZ, tar, signedPKG)
			mod = wf.Task4(wd, "Merge signed files into module zip", tasks.mergeSignedToModule, version, timestamp, mod, signedPKG)
			artifacts = append(artifacts, signedPKG, signedTGZ)
		case "windows":
			msi := wf.Task1(wd, "Build MSI installer", tasks.buildWindowsMSI, tar)
			signedMSI := wf.Task2(wd, "Sign MSI installer", tasks.signArtifact, msi, wf.Const(sign.BuildWindows))
			artifacts = append(artifacts, signedMSI, zip)
		default:
			artifacts = append(artifacts, tar)
		}
		mods = append(mods, mod)
	}
	signedArtifacts := wf.Task1(wd, "Compute GPG signature for artifacts", tasks.computeGPG, wf.Slice(artifacts...))

	// Test all targets.
	builders := wf.Task2(wd, "Read builders", tasks.readRelevantBuilders, wf.Const(major), wf.Const(kind))
	builderResults := wf.Expand1(wd, "Plan builders", func(wd *wf.Definition, builders []string) (wf.Value[[]testResult], error) {
		var results []wf.Value[testResult]
		for _, b := range builders {
			// Note: We can consider adding an "is_first_class" property into builder config
			// and using it to display whether the builder is for a first class port or not.
			// Until then, it's up to the release coordinator to make this distinction when
			// approving any failures.
			res := wf.Task3(wd, "Run advisory builder "+b, tasks.runAdvisoryBuildBucket, wf.Const(b), skipTests, sourceSpec)
			results = append(results, res)
		}
		return wf.Slice(results...), nil
	}, builders)
	buildersApproved := wf.Action1(wd, "Wait for advisory builders", tasks.checkTestResults, builderResults)
	blockers = append(blockers, buildersApproved)

	signedAndTested := wf.Task2(wd, "Wait for signing and tests", func(ctx *wf.TaskContext, artifacts []artifact, version string) ([]artifact, error) {
		// Note: This needs to happen somewhere, doesn't matter where. Maybe move it to a nicer place later.
		for i, a := range artifacts {
			if a.Target != nil {
				artifacts[i].Filename = version + "." + a.Target.Name + "." + a.Suffix
			} else {
				artifacts[i].Filename = version + "." + a.Suffix
			}
		}

		return artifacts, nil
	}, signedArtifacts, version, wf.After(blockers...), wf.After(wf.Slice(mods...)))
	return source, signedAndTested, wf.Slice(mods...)
}

// BuildReleaseTasks serves as an adapter to the various build tasks in the task package.
type BuildReleaseTasks struct {
	GerritClient             task.GerritClient
	GerritProject            string
	GerritHTTPClient         *http.Client // GerritHTTPClient is an HTTP client that authenticates to Gerrit instances. (Both public and private.)
	PrivateGerritClient      task.GerritClient
	PrivateGerritProject     string
	GCSClient                *storage.Client
	ScratchFS                *task.ScratchFS
	SignedURL                string // SignedURL is a gs:// or file:// URL, no trailing slash.
	ServingURL               string // ServingURL is a gs:// or file:// URL, no trailing slash.
	DownloadURL              string
	ProxyPrefix              string // ProxyPrefix is the prefix at which module files are published, e.g. https://proxy.golang.org/golang.org/toolchain/@v
	PublishFile              func(task.WebsiteFile) error
	SignService              sign.Service
	GoogleDockerBuildProject string
	GoogleDockerBuildTrigger string
	CloudBuildClient         task.CloudBuildClient
	BuildBucketClient        task.BuildBucketClient
	SwarmingClient           task.SwarmingClient
	ApproveAction            func(*wf.TaskContext) error
}

var commitRE = regexp.MustCompile(`[a-f0-9]{40}`)

func (b *BuildReleaseTasks) readSecurityRef(ctx *wf.TaskContext, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if commitRE.MatchString(ref) {
		return ref, nil
	}
	commit, err := b.PrivateGerritClient.ReadBranchHead(ctx, b.PrivateGerritProject, ref)
	if err != nil {
		return "", fmt.Errorf("%q doesn't appear to be a commit hash, but resolving it as a branch failed: %v", ref, err)
	}
	return commit, nil
}

func (b *BuildReleaseTasks) getGitSource(ctx *wf.TaskContext, branch, commit, securityCommit, versionFile string) (sourceSpec, error) {
	client, project, rev := b.GerritClient, b.GerritProject, commit
	if securityCommit != "" {
		client, project, rev = b.PrivateGerritClient, b.PrivateGerritProject, securityCommit
	}
	return sourceSpec{
		GitilesURL:  client.GitilesURL(),
		Project:     project,
		Branch:      branch,
		Revision:    rev,
		VersionFile: versionFile,
	}, nil
}

func (b *BuildReleaseTasks) buildSource(ctx *wf.TaskContext, source sourceSpec) (artifact, error) {
	resp, err := b.GerritHTTPClient.Get(source.ArchiveURL())
	if err != nil {
		return artifact{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return artifact{}, fmt.Errorf("failed to fetch %q: %v", source.ArchiveURL(), resp.Status)
	}
	defer resp.Body.Close()
	return b.runBuildStep(ctx, nil, artifact{}, "src.tar.gz", func(_ io.Reader, w io.Writer) error {
		return b.buildSourceGCB(ctx, resp.Body, source.VersionFile, w)
	})
}

func (b *BuildReleaseTasks) buildSourceGCB(ctx *wf.TaskContext, r io.Reader, versionFile string, w io.Writer) error {
	filename, f, err := b.ScratchFS.OpenWrite(ctx, "source.tgz")
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	script := fmt.Sprintf(`
gsutil cp %q source.tgz
mkdir go
tar -xf source.tgz -C go
echo -ne %q > go/VERSION
(cd go/src && GOOS=linux GOARCH=amd64 ./make.bash -distpack)
mv go/pkg/distpack/*.src.tar.gz src.tar.gz
`, b.ScratchFS.URL(ctx, filename), versionFile)

	build, err := b.CloudBuildClient.RunScript(ctx, script, "", []string{"src.tar.gz"})
	if err != nil {
		return err
	}
	if _, err := task.AwaitCondition(ctx, 30*time.Second, func() (string, bool, error) {
		return b.CloudBuildClient.Completed(ctx, build)
	}); err != nil {
		return err
	}
	resultFS, err := b.CloudBuildClient.ResultFS(ctx, build)
	if err != nil {
		return err
	}
	distpack, err := resultFS.Open("src.tar.gz")
	if err != nil {
		return err
	}
	_, err = io.Copy(w, distpack)
	return err
}

func (b *BuildReleaseTasks) checkSourceMatch(ctx *wf.TaskContext, branch, versionFile string, source artifact) (head string, _ error) {
	head, err := b.GerritClient.ReadBranchHead(ctx, b.GerritProject, branch)
	if err != nil {
		return "", err
	}
	spec, err := b.getGitSource(ctx, branch, head, "", versionFile)
	if err != nil {
		return "", err
	}
	branchArchive, err := b.buildSource(ctx, spec)
	if err != nil {
		return "", err
	}
	diff, err := b.diffArtifacts(ctx, branchArchive, source)
	if err != nil {
		return "", err
	}
	if diff != "" {
		return "", fmt.Errorf("branch state doesn't match source archive (-branch, +archive):\n%v", diff)
	}
	return head, nil
}

func (b *BuildReleaseTasks) diffArtifacts(ctx *wf.TaskContext, a1, a2 artifact) (string, error) {
	h1, err := b.hashArtifact(ctx, a1)
	if err != nil {
		return "", fmt.Errorf("hashing first tarball: %v", err)
	}
	h2, err := b.hashArtifact(ctx, a2)
	if err != nil {
		return "", fmt.Errorf("hashing second tarball: %v", err)
	}
	return cmp.Diff(h1, h2), nil
}

func (b *BuildReleaseTasks) hashArtifact(ctx *wf.TaskContext, a artifact) (map[string]string, error) {
	hashes := map[string]string{}
	_, err := b.runBuildStep(ctx, nil, a, "", func(r io.Reader, _ io.Writer) error {
		return tarballHashes(r, "", hashes, false)
	})
	return hashes, err
}

func tarballHashes(r io.Reader, prefix string, hashes map[string]string, includeHeaders bool) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("reading tar header: %v", err)
		}
		if strings.HasSuffix(header.Name, ".tar.gz") {
			if err := tarballHashes(tr, header.Name+":", hashes, true); err != nil {
				return fmt.Errorf("reading inner tarball %v: %v", header.Name, err)
			}
		} else {
			h := sha256.New()
			if _, err := io.CopyN(h, tr, header.Size); err != nil {
				return fmt.Errorf("reading file %q: %v", header.Name, err)
			}
			// At the top level, we don't care about headers, only contents.
			// But in inner archives, headers are contents and we care a lot.
			if includeHeaders {
				hashes[prefix+header.Name] = fmt.Sprintf("%v %X", header, h.Sum(nil))
			} else {
				hashes[prefix+header.Name] = fmt.Sprintf("%X", h.Sum(nil))
			}
		}
	}
	return nil
}

func (b *BuildReleaseTasks) buildDistpack(ctx *wf.TaskContext, target *releasetargets.Target, source artifact) (artifact, error) {
	return b.runBuildStep(ctx, target, artifact{}, "tar.gz", func(_ io.Reader, w io.Writer) error {
		// We need GOROOT_FINAL both during the binary build and test runs. See go.dev/issue/52236.
		// TODO(go.dev/issue/62047): GOROOT_FINAL is being removed. Remove it from here too.
		makeEnv := []string{"GOROOT_FINAL=" + dashboard.GorootFinal(target.GOOS)}
		// Add extra vars from the target's configuration.
		makeEnv = append(makeEnv, target.ExtraEnv...)
		makeEnv = append(makeEnv, "GOOS="+target.GOOS, "GOARCH="+target.GOARCH)

		script := fmt.Sprintf(`
gsutil cp %q src.tar.gz
tar -xf src.tar.gz
(cd go/src && %v ./make.bash -distpack)
(cd go/pkg/distpack && tar -czf ../../../distpacks.tar.gz *)
`, b.ScratchFS.URL(ctx, source.Scratch), strings.Join(makeEnv, " "))
		build, err := b.CloudBuildClient.RunScript(ctx, script, "", []string{"distpacks.tar.gz"})
		if err != nil {
			return err
		}
		if _, err := task.AwaitCondition(ctx, 30*time.Second, func() (string, bool, error) {
			return b.CloudBuildClient.Completed(ctx, build)
		}); err != nil {
			return err
		}
		resultFS, err := b.CloudBuildClient.ResultFS(ctx, build)
		if err != nil {
			return err
		}
		distpack, err := resultFS.Open("distpacks.tar.gz")
		if err != nil {
			return err
		}
		_, err = io.Copy(w, distpack)
		return err
	})
}

func (b *BuildReleaseTasks) reproduceDistpack(ctx *wf.TaskContext, target *releasetargets.Target, source artifact) (artifact, error) {
	return b.runBuildStep(ctx, target, artifact{}, "tar.gz", func(_ io.Reader, w io.Writer) error {
		scratchFile := b.ScratchFS.WriteFilename(ctx, fmt.Sprintf("reproduce-distpack-%v.tar.gz", target.Name))
		// This script is carefully crafted to work on both Windows and Unix
		// for testing. In particular, Windows doesn't seem to like ./foo.exe,
		// so we have to run it unadorned with . on PATH.
		script := fmt.Sprintf(
			`gsutil cat %s | tar -xzf - && cd go/src && make.bat -distpack && cd ../pkg/distpack && tar -czf - * | gsutil cp - %s`,
			b.ScratchFS.URL(ctx, source.Scratch), b.ScratchFS.URL(ctx, scratchFile))

		env := map[string]string{
			"GOOS":   target.GOOS,
			"GOARCH": target.GOARCH,
		}
		for _, e := range target.ExtraEnv {
			k, v, ok := strings.Cut(e, "=")
			if !ok {
				return fmt.Errorf("malformed env var %q", e)
			}
			env[k] = v
		}

		id, err := b.SwarmingClient.RunTask(ctx, map[string]string{
			"cipd_platform": "windows-amd64",
			"os":            "Windows-10",
		}, script, env)
		if err != nil {
			return err
		}
		if _, err := task.AwaitCondition(ctx, 30*time.Second, func() (string, bool, error) {
			return b.SwarmingClient.Completed(ctx, id)
		}); err != nil {
			return err
		}

		distpack, err := b.ScratchFS.OpenRead(ctx, scratchFile)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, distpack)
		return err
	})

}

func (b *BuildReleaseTasks) checkDistpacksMatch(ctx *wf.TaskContext, linux, windows artifact) error {
	diff, err := b.diffArtifacts(ctx, linux, windows)
	if err != nil {
		return err
	}
	if diff != "" {
		return fmt.Errorf("distpacks don't match (-linux, +windows): %v", diff)
	}
	return nil
}

func (b *BuildReleaseTasks) binaryArchiveFromDistpack(ctx *wf.TaskContext, distpack artifact) (artifact, error) {
	// This must not match the module files, which currently start with v0.0.1.
	glob := fmt.Sprintf("go*%v-%v.*", distpack.Target.GOOS, distpack.Target.GOARCH)
	suffix := "tar.gz"
	if distpack.Target.GOOS == "windows" {
		suffix = "zip"
	}
	return b.runBuildStep(ctx, distpack.Target, distpack, suffix, func(r io.Reader, w io.Writer) error {
		return task.ExtractFile(r, w, glob)
	})
}

func (b *BuildReleaseTasks) modFilesFromDistpack(ctx *wf.TaskContext, distpack artifact) (moduleArtifact, error) {
	result := moduleArtifact{Target: distpack.Target}
	artifact, err := b.runBuildStep(ctx, nil, distpack, "mod.zip", func(r io.Reader, w io.Writer) error {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		tr := tar.NewReader(zr)
		foundZip := false
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			} else if err != nil {
				return err
			}
			if h.FileInfo().IsDir() || !strings.HasPrefix(h.Name, "v0.0.1") {
				continue
			}

			switch {
			case strings.HasSuffix(h.Name, ".zip"):
				if _, err := io.Copy(w, tr); err != nil {
					return err
				}
				foundZip = true
			case strings.HasSuffix(h.Name, ".info"):
				buf := &bytes.Buffer{}
				if _, err := io.Copy(buf, tr); err != nil {
					return err
				}
				result.Info = buf.String()
			case strings.HasSuffix(h.Name, ".mod"):
				buf := &bytes.Buffer{}
				if _, err := io.Copy(buf, tr); err != nil {
					return err
				}
				result.Mod = buf.String()
			}

			if foundZip && result.Mod != "" && result.Info != "" {
				return nil
			}
		}
	})
	if err != nil {
		return moduleArtifact{}, err
	}
	result.ZipScratch = artifact.Scratch
	return result, nil
}

func (b *BuildReleaseTasks) modFilesFromBinary(ctx *wf.TaskContext, version string, t time.Time, tar artifact) (moduleArtifact, error) {
	result := moduleArtifact{Target: tar.Target}
	a, err := b.runBuildStep(ctx, nil, tar, "mod.zip", func(r io.Reader, w io.Writer) error {
		ctx.DisableWatchdog() // The zipping process can be time consuming and is unlikely to hang.
		var err error
		result.Mod, result.Info, err = task.TarToModFiles(tar.Target, version, t, r, w)
		return err
	})
	if err != nil {
		return moduleArtifact{}, err
	}
	result.ZipScratch = a.Scratch
	return result, nil
}

func (b *BuildReleaseTasks) mergeSignedToTGZ(ctx *wf.TaskContext, unsigned, signed artifact) (artifact, error) {
	return b.runBuildStep(ctx, unsigned.Target, signed, "tar.gz", func(signed io.Reader, w io.Writer) error {
		signedBinaries, err := task.ReadBinariesFromPKG(signed)
		if err != nil {
			return err
		} else if _, ok := signedBinaries["go/bin/go"]; !ok {
			return fmt.Errorf("didn't find go/bin/go among %d signed binaries %+q", len(signedBinaries), maps.Keys(signedBinaries))
		}

		// Copy files from the tgz, overwriting with binaries from the signed tar.
		ur, err := b.ScratchFS.OpenRead(ctx, unsigned.Scratch)
		if err != nil {
			return err
		}
		defer ur.Close()
		uzr, err := gzip.NewReader(ur)
		if err != nil {
			return err
		}
		defer uzr.Close()

		utr := tar.NewReader(uzr)

		zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
		if err != nil {
			return err
		}
		tw := tar.NewWriter(zw)

		for {
			th, err := utr.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				return err
			}

			hdr := *th
			src := io.NopCloser(utr)
			if signed, ok := signedBinaries[th.Name]; ok {
				src = io.NopCloser(bytes.NewReader(signed))
				hdr.Size = int64(len(signed))
			}

			if err := tw.WriteHeader(&hdr); err != nil {
				return err
			}
			if _, err := io.Copy(tw, src); err != nil {
				return err
			}
		}

		if err := tw.Close(); err != nil {
			return err
		}
		return zw.Close()
	})
}

func (b *BuildReleaseTasks) mergeSignedToModule(ctx *wf.TaskContext, version string, timestamp time.Time, mod moduleArtifact, signed artifact) (moduleArtifact, error) {
	a, err := b.runBuildStep(ctx, nil, signed, "signedmod.zip", func(signed io.Reader, w io.Writer) error {
		signedBinaries, err := task.ReadBinariesFromPKG(signed)
		if err != nil {
			return err
		} else if _, ok := signedBinaries["go/bin/go"]; !ok {
			return fmt.Errorf("didn't find go/bin/go among %d signed binaries %+q", len(signedBinaries), maps.Keys(signedBinaries))
		}

		// Copy files from the module zip, overwriting with binaries from the signed tar.
		mr, err := b.ScratchFS.OpenRead(ctx, mod.ZipScratch)
		if err != nil {
			return err
		}
		defer mr.Close()
		mbytes, err := io.ReadAll(mr)
		if err != nil {
			return err
		}
		mzr, err := zip.NewReader(bytes.NewReader(mbytes), int64(len(mbytes)))
		if err != nil {
			return err
		}

		prefix := task.ToolchainZipPrefix(mod.Target, version) + "/"
		mzw := zip.NewWriter(w)
		mzw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
			return flate.NewWriter(out, flate.BestCompression)
		})
		for _, f := range mzr.File {
			var in io.ReadCloser
			suffix, ok := strings.CutPrefix(f.Name, prefix)
			if !ok {
				continue
			}
			if contents, ok := signedBinaries["go/"+suffix]; ok {
				in = io.NopCloser(bytes.NewReader(contents))
			} else {
				in, err = f.Open()
				if err != nil {
					return err
				}
			}

			hdr := f.FileHeader
			out, err := mzw.CreateHeader(&hdr)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				return err
			}
		}
		return mzw.Close()
	})
	if err != nil {
		return moduleArtifact{}, err
	}
	mod.ZipScratch = a.Scratch
	return mod, nil
}

// buildDarwinPKG constructs an installer for the given binary artifact, to be signed.
func (b *BuildReleaseTasks) buildDarwinPKG(ctx *wf.TaskContext, binary artifact) (artifact, error) {
	return b.runBuildStep(ctx, binary.Target, artifact{}, "pkg", func(_ io.Reader, w io.Writer) error {
		metadataFile, err := jsonEncodeScratchFile(ctx, b.ScratchFS, darwinpkg.InstallerOptions{
			GOARCH:          binary.Target.GOARCH,
			MinMacOSVersion: binary.Target.MinMacOSVersion,
		})
		if err != nil {
			return err
		}
		installerPaths, err := b.signArtifacts(ctx, sign.BuildMacOSConstructInstallerOnly, []string{
			b.ScratchFS.URL(ctx, binary.Scratch),
			b.ScratchFS.URL(ctx, metadataFile),
		})
		if err != nil {
			return err
		} else if len(installerPaths) != 1 {
			return fmt.Errorf("got %d outputs, want 1 macOS .pkg installer", len(installerPaths))
		} else if ext := path.Ext(installerPaths[0]); ext != ".pkg" {
			return fmt.Errorf("got output extension %q, want .pkg", ext)
		}
		resultFS, err := gcsfs.FromURL(ctx, b.GCSClient, b.SignedURL)
		if err != nil {
			return err
		}
		r, err := resultFS.Open(installerPaths[0])
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(w, r)
		return err
	})
}

// buildWindowsMSI constructs an installer for the given binary artifact, to be signed.
func (b *BuildReleaseTasks) buildWindowsMSI(ctx *wf.TaskContext, binary artifact) (artifact, error) {
	return b.runBuildStep(ctx, binary.Target, artifact{}, "msi", func(_ io.Reader, w io.Writer) error {
		metadataFile, err := jsonEncodeScratchFile(ctx, b.ScratchFS, windowsmsi.InstallerOptions{
			GOARCH: binary.Target.GOARCH,
		})
		if err != nil {
			return err
		}
		installerPaths, err := b.signArtifacts(ctx, sign.BuildWindowsConstructInstallerOnly, []string{
			b.ScratchFS.URL(ctx, binary.Scratch),
			b.ScratchFS.URL(ctx, metadataFile),
		})
		if err != nil {
			return err
		} else if len(installerPaths) != 1 {
			return fmt.Errorf("got %d outputs, want 1 Windows .msi installer", len(installerPaths))
		} else if ext := path.Ext(installerPaths[0]); ext != ".msi" {
			return fmt.Errorf("got output extension %q, want .msi", ext)
		}
		resultFS, err := gcsfs.FromURL(ctx, b.GCSClient, b.SignedURL)
		if err != nil {
			return err
		}
		r, err := resultFS.Open(installerPaths[0])
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(w, r)
		return err
	})
}

func (b *BuildReleaseTasks) convertZipToTGZ(ctx *wf.TaskContext, binary artifact) (artifact, error) {
	return b.runBuildStep(ctx, binary.Target, binary, "tar.gz", func(r io.Reader, w io.Writer) error {
		// Reading the whole file isn't ideal, but we need a ReaderAt, and
		// don't have access to the lower-level file (which would support
		// seeking) here.
		content, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		return task.ConvertZIPToTGZ(bytes.NewReader(content), int64(len(content)), w)
	})
}

// computeGPG performs GPG signing on artifacts, and sets their GPGSignature field.
func (b *BuildReleaseTasks) computeGPG(ctx *wf.TaskContext, artifacts []artifact) ([]artifact, error) {
	// doGPG reports whether to do GPG signature computation for artifact a.
	doGPG := func(a artifact) bool {
		return a.Suffix == "src.tar.gz" || a.Suffix == "tar.gz"
	}

	// Start a signing job on all artifacts that want to do GPG signing and await its results.
	var in []string
	for _, a := range artifacts {
		if !doGPG(a) {
			continue
		}

		in = append(in, b.ScratchFS.URL(ctx, a.Scratch))
	}
	out, err := b.signArtifacts(ctx, sign.BuildGPG, in)
	if err != nil {
		return nil, err
	} else if len(out) != len(in) {
		return nil, fmt.Errorf("got %d outputs, want %d .asc signatures", len(out), len(in))
	}
	// All done, we have our GPG signatures.
	// Put them in a base name → scratch path map.
	var signatures = make(map[string]string)
	for _, o := range out {
		signatures[path.Base(o)] = o
	}

	// Set the artifacts' GPGSignature field.
	signedFS, err := gcsfs.FromURL(ctx, b.GCSClient, b.SignedURL)
	if err != nil {
		return nil, err
	}
	for i, a := range artifacts {
		if !doGPG(a) {
			continue
		}

		sigPath, ok := signatures[path.Base(a.Scratch)+".asc"]
		if !ok {
			return nil, fmt.Errorf("no GPG signature for %q", path.Base(a.Scratch))
		}
		sig, err := fs.ReadFile(signedFS, sigPath)
		if err != nil {
			return nil, err
		}
		artifacts[i].GPGSignature = string(sig)
	}

	return artifacts, nil
}

// signArtifact signs a single artifact of specified type.
func (b *BuildReleaseTasks) signArtifact(ctx *wf.TaskContext, a artifact, bt sign.BuildType) (signed artifact, _ error) {
	return b.runBuildStep(ctx, a.Target, artifact{}, a.Suffix, func(_ io.Reader, w io.Writer) error {
		signedPaths, err := b.signArtifacts(ctx, bt, []string{b.ScratchFS.URL(ctx, a.Scratch)})
		if err != nil {
			return err
		} else if len(signedPaths) != 1 {
			return fmt.Errorf("got %d outputs, want 1 signed artifact", len(signedPaths))
		}

		signedFS, err := gcsfs.FromURL(ctx, b.GCSClient, b.SignedURL)
		if err != nil {
			return err
		}
		r, err := signedFS.Open(signedPaths[0])
		if err != nil {
			return err
		}
		_, err = io.Copy(w, r)
		return err
	})
}

// signArtifacts starts signing on the artifacts provided via the gs:// URL inputs,
// waits for signing to complete, and returns the output paths relative to SignedURL.
func (b *BuildReleaseTasks) signArtifacts(ctx *wf.TaskContext, bt sign.BuildType, inURLs []string) (outFiles []string, _ error) {
	jobID, err := b.SignService.SignArtifact(ctx, bt, inURLs)
	if err != nil {
		return nil, err
	}
	outURLs, jobError := task.AwaitCondition(ctx, time.Minute, func() (out []string, done bool, _ error) {
		statusContext, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		t := time.Now()
		status, desc, out, err := b.SignService.ArtifactSigningStatus(statusContext, jobID)
		if err != nil {
			ctx.Printf("ArtifactSigningStatus ran into a retryable communication error after %v: %v\n", time.Since(t), err)
			return nil, false, nil
		}
		switch status {
		case sign.StatusCompleted:
			return out, true, nil // All done.
		case sign.StatusFailed:
			if desc != "" {
				return nil, true, fmt.Errorf("signing attempt failed: %s", desc)
			}
			return nil, true, fmt.Errorf("signing attempt failed")
		default:
			if desc != "" {
				ctx.Printf("still waiting: %s\n", desc)
			}
			return nil, false, nil // Still waiting.
		}
	})
	if jobError != nil {
		// If ctx is canceled, also cancel the signing request.
		if ctx.Err() != nil {
			cancelContext, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			t := time.Now()
			err := b.SignService.CancelSigning(cancelContext, jobID)
			if err != nil {
				ctx.Printf("CancelSigning error after %v: %v\n", time.Since(t), err)
			}
		}

		return nil, jobError
	}

	for _, url := range outURLs {
		f, ok := strings.CutPrefix(url, b.SignedURL+"/")
		if !ok {
			return nil, fmt.Errorf("got signed URL %q outside of signing result dir %q, which is unsupported", url, b.SignedURL+"/")
		}
		outFiles = append(outFiles, f)
	}
	return outFiles, nil
}

func (b *BuildReleaseTasks) readRelevantBuilders(ctx *wf.TaskContext, major int, kind task.ReleaseKind) ([]string, error) {
	prefix := fmt.Sprintf("go1.%v-", major)
	if kind == task.KindBeta {
		prefix = "gotip-"
	}
	builders, err := b.BuildBucketClient.ListBuilders(ctx, "security-try")
	if err != nil {
		return nil, err
	}
	var relevant []string
	for name, b := range builders {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		var props struct {
			BuilderMode int `json:"mode"`
			KnownIssue  int `json:"known_issue"`
		}
		if err := json.Unmarshal([]byte(b.Properties), &props); err != nil {
			return nil, fmt.Errorf("error unmarshaling properties for %v: %v", name, err)
		}
		var skip []string // Log-worthy causes of skip, if any.
		// golangbuildModePerf is golangbuild's MODE_PERF mode that
		// runs benchmarks. It's the first custom mode not relevant
		// to building and testing, and the expectation is that any
		// modes after it will be fine to skip for release purposes.
		//
		// See https://source.chromium.org/chromium/infra/infra/+/main:go/src/infra/experimental/golangbuild/golangbuildpb/params.proto;l=174-177;drc=fdea4abccf8447808d4e702c8d09fdd20fd81acb.
		const golangbuildModePerf = 4
		if props.BuilderMode >= golangbuildModePerf {
			skip = append(skip, fmt.Sprintf("custom mode %d", props.BuilderMode))
		}
		if props.KnownIssue != 0 {
			skip = append(skip, fmt.Sprintf("known issue %d", props.KnownIssue))
		}
		if len(skip) != 0 {
			ctx.Printf("skipping %s because of %s", name, strings.Join(skip, ", "))
			continue
		}
		relevant = append(relevant, name)
	}
	slices.Sort(relevant)
	return relevant, nil
}

type testResult struct {
	Name   string
	Passed bool
}

func (b *BuildReleaseTasks) runAdvisoryBuildBucket(ctx *wf.TaskContext, name string, skipTests []string, source sourceSpec) (testResult, error) {
	return b.runAdvisoryTest(ctx, name, skipTests, func() error {
		u, err := url.Parse(source.GitilesURL)
		if err != nil {
			return err
		}
		commit := &pb.GitilesCommit{
			Host:    u.Host,
			Project: source.Project,
			Id:      source.Revision,
			Ref:     "refs/heads/" + source.Branch,
		}
		id, err := b.BuildBucketClient.RunBuild(ctx, "security-try", name, commit, map[string]*structpb.Value{
			"version_file": structpb.NewStringValue(source.VersionFile),
		})
		if err != nil {
			return err
		}
		_, err = task.AwaitCondition(ctx, 30*time.Second, func() (string, bool, error) {
			return b.BuildBucketClient.Completed(ctx, id)
		})
		return err
	})
}

func (b *BuildReleaseTasks) runAdvisoryTest(ctx *wf.TaskContext, name string, skipTests []string, run func() error) (testResult, error) {
	for _, skip := range skipTests {
		if skip == "all" || name == skip {
			ctx.Printf("Skipping test")
			return testResult{name, true}, nil
		}
	}
	err := errors.New("untested") // prime the loop
	for attempt := 1; attempt <= wf.MaxRetries && err != nil; attempt++ {
		ctx.Printf("======== Attempt %d of %d ========\n", attempt, wf.MaxRetries)
		err = run()
		if err != nil {
			ctx.Printf("Attempt failed: %v\n", err)
		}
	}
	if err != nil {
		ctx.Printf("Advisory test failed. Check the logs and approve this task if it's okay:\n")
		return testResult{name, false}, b.ApproveAction(ctx)
	}
	return testResult{name, true}, nil

}

func (b *BuildReleaseTasks) checkTestResults(ctx *wf.TaskContext, results []testResult) error {
	var fails []string
	for _, r := range results {
		if !r.Passed {
			fails = append(fails, r.Name)
		}
	}
	if len(fails) != 0 {
		sort.Strings(fails)
		ctx.Printf("Some advisory tests failed and their failures have been approved:\n%v", strings.Join(fails, "\n"))
		return nil
	}
	return nil
}

// runBuildStep is a convenience function that manages resources a build step might need.
// If input with a scratch file is specified, its content will be opened and passed as a Reader to f.
// If outputSuffix is specified, a unique filename will be generated based off
// it (and the target name, if any), the file will be opened and passed as a
// Writer to f, and an artifact representing it will be returned as the result.
func (b *BuildReleaseTasks) runBuildStep(
	ctx *wf.TaskContext,
	target *releasetargets.Target,
	input artifact,
	outputSuffix string,
	f func(io.Reader, io.Writer) error,
) (artifact, error) {
	var err error
	var in io.ReadCloser
	if input.Scratch != "" {
		in, err = b.ScratchFS.OpenRead(ctx, input.Scratch)
		if err != nil {
			return artifact{}, err
		}
		defer in.Close()
	}
	var out io.WriteCloser
	var scratch string
	hash := sha256.New()
	size := &sizeWriter{}
	var multiOut io.Writer
	if outputSuffix != "" {
		name := outputSuffix
		if target != nil {
			name = target.Name + "." + outputSuffix
		}
		scratch, out, err = b.ScratchFS.OpenWrite(ctx, name)
		if err != nil {
			return artifact{}, err
		}
		defer out.Close()
		multiOut = io.MultiWriter(out, hash, size)
	}
	// Hide in's Close method from the task, which may assert it to Closer.
	nopIn := io.NopCloser(in)
	if err := f(nopIn, multiOut); err != nil {
		return artifact{}, err
	}
	if in != nil {
		if err := in.Close(); err != nil {
			return artifact{}, err
		}
	}
	if out != nil {
		if err := out.Close(); err != nil {
			return artifact{}, err
		}
	}
	return artifact{
		Target:  target,
		Scratch: scratch,
		Suffix:  outputSuffix,
		SHA256:  fmt.Sprintf("%x", string(hash.Sum([]byte(nil)))),
		Size:    size.size,
	}, nil
}

// An artifact represents a file as it moves through the release process. Most
// files will appear on go.dev/dl eventually.
type artifact struct {
	// The target platform of this artifact, or nil for source.
	Target *releasetargets.Target
	// The filename of this artifact, as used with the tasks' ScratchFS.
	Scratch string
	// The contents of the GPG signature for this artifact (.asc file).
	GPGSignature string
	// The filename suffix of the artifact, e.g. "tar.gz" or "src.tar.gz",
	// combined with the version and Target name to produce Filename.
	Suffix string
	// The final Filename of this artifact as it will be downloaded.
	Filename string
	SHA256   string
	Size     int
}

type sizeWriter struct {
	size int
}

func (w *sizeWriter) Write(p []byte) (n int, err error) {
	w.size += len(p)
	return len(p), nil
}

func (tasks *BuildReleaseTasks) uploadArtifacts(ctx *wf.TaskContext, artifacts []artifact) error {
	servingFS, err := gcsfs.FromURL(ctx, tasks.GCSClient, tasks.ServingURL)
	if err != nil {
		return err
	}

	want := map[string]bool{} // URLs we're waiting on becoming available.
	for _, a := range artifacts {
		if err := tasks.uploadFile(ctx, servingFS, a.Scratch, a.Filename); err != nil {
			return err
		}
		want[tasks.DownloadURL+"/"+a.Filename] = true

		if err := gcsfs.WriteFile(servingFS, a.Filename+".sha256", []byte(a.SHA256)); err != nil {
			return err
		}
		want[tasks.DownloadURL+"/"+a.Filename+".sha256"] = true

		if a.GPGSignature != "" {
			if err := gcsfs.WriteFile(servingFS, a.Filename+".asc", []byte(a.GPGSignature)); err != nil {
				return err
			}
			want[tasks.DownloadURL+"/"+a.Filename+".asc"] = true
		}
	}
	_, err = task.AwaitCondition(ctx, 30*time.Second, checkFiles(ctx, want))
	return err
}

func (tasks *BuildReleaseTasks) uploadModules(ctx *wf.TaskContext, version string, modules []moduleArtifact) error {
	servingFS, err := gcsfs.FromURL(ctx, tasks.GCSClient, tasks.ServingURL)
	if err != nil {
		return err
	}
	want := map[string]bool{} // URLs we're waiting on becoming available.
	for _, mod := range modules {
		base := task.ToolchainModuleVersion(mod.Target, version)
		if err := tasks.uploadFile(ctx, servingFS, mod.ZipScratch, base+".zip"); err != nil {
			return err
		}
		if err := gcsfs.WriteFile(servingFS, base+".info", []byte(mod.Info)); err != nil {
			return err
		}
		if err := gcsfs.WriteFile(servingFS, base+".mod", []byte(mod.Mod)); err != nil {
			return err
		}
		for _, ext := range []string{".zip", ".info", ".mod"} {
			want[tasks.DownloadURL+"/"+base+ext] = true
		}
	}
	_, err = task.AwaitCondition(ctx, 30*time.Second, checkFiles(ctx, want))
	return err
}

func (tasks *BuildReleaseTasks) awaitProxy(ctx *wf.TaskContext, version string, modules []moduleArtifact) error {
	want := map[string]bool{}
	for _, mod := range modules {
		url := fmt.Sprintf("%v/%v.info", tasks.ProxyPrefix, task.ToolchainModuleVersion(mod.Target, version))
		want[url] = true
	}
	_, err := task.AwaitCondition(ctx, 30*time.Second, checkFiles(ctx, want))
	return err
}

func checkFiles(ctx context.Context, want map[string]bool) func() (int, bool, error) {
	found := map[string]bool{}
	return func() (int, bool, error) {
		for url := range want {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			resp, err := ctxhttp.Head(ctx, http.DefaultClient, url)
			if err == context.DeadlineExceeded {
				cancel()
				continue
			}
			if err != nil {
				return 0, false, err
			}
			resp.Body.Close()
			cancel()
			if resp.StatusCode == http.StatusOK {
				found[url] = true
			}
		}
		return 0, len(want) == len(found), nil
	}
}

// uploadFile copies a file from tasks.ScratchFS to servingFS.
func (tasks *BuildReleaseTasks) uploadFile(ctx *wf.TaskContext, servingFS fs.FS, scratch, filename string) error {
	in, err := tasks.ScratchFS.OpenRead(ctx, scratch)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := gcsfs.Create(servingFS, filename)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return nil
}

// publishArtifacts publishes artifacts for version (typically so they appear at https://go.dev/dl/).
// It returns the Go version and files that have been successfully published.
func (tasks *BuildReleaseTasks) publishArtifacts(ctx *wf.TaskContext, version string, artifacts []artifact) (task.Published, error) {
	// Each release artifact corresponds to a single website file.
	var files = make([]task.WebsiteFile, len(artifacts))
	for i, a := range artifacts {
		// Define website file metadata.
		f := task.WebsiteFile{
			Filename:       a.Filename,
			Version:        version,
			ChecksumSHA256: a.SHA256,
			Size:           int64(a.Size),
		}
		if a.Target != nil {
			f.OS = a.Target.GOOS
			f.Arch = a.Target.GOARCH
			if a.Target.GOOS == "linux" && a.Target.GOARCH == "arm" && slices.Contains(a.Target.ExtraEnv, "GOARM=6") {
				f.Arch = "armv6l"
			}
			if goversion.Compare(version, "go1.23") == -1 { // TODO: Delete this after Go 1.24.0 is out and this becomes dead code.
				// Due to an oversight, we've been inadvertently setting the "arch" field
				// of published download metadata to "armv6l" for all arm ports, not just
				// linux/arm port as intended. Keep doing it for the rest of Go 1.22/1.21
				// minor releases only.
				if a.Target.GOARCH == "arm" {
					f.Arch = "armv6l"
				}
			}
		}
		switch a.Suffix {
		case "src.tar.gz":
			f.Kind = "source"
		case "tar.gz", "zip":
			f.Kind = "archive"
		case "msi", "pkg":
			f.Kind = "installer"
		}

		// Publish it.
		if err := tasks.PublishFile(f); err != nil {
			return task.Published{}, err
		}
		ctx.Printf("Published %q.", f.Filename)
		files[i] = f
	}
	ctx.Printf("Published all %d files for %s.", len(files), version)
	return task.Published{Version: version, Files: files}, nil
}

func (b *BuildReleaseTasks) runAndAwaitGoogleDockerBuild(ctx *wf.TaskContext, version string) (detail string, _ error) {
	// Because we want to publish versions without the leading "go", it's easiest to strip it here.
	v := strings.TrimPrefix(version, "go")
	// We're about to trigger a Cloud Build run, and then await its result.
	// Allow only manual retries from this point to remove the possibility
	// of multiple Cloud Build runs being started by automated retries.
	ctx.DisableRetries()
	build, err := b.CloudBuildClient.RunBuildTrigger(ctx, b.GoogleDockerBuildProject, b.GoogleDockerBuildTrigger, map[string]string{"_GO_VERSION": v})
	if err != nil {
		return "", err
	}
	return task.AwaitCondition(ctx, 30*time.Second, func() (string, bool, error) {
		return b.CloudBuildClient.Completed(ctx, build)
	})
}

// jsonEncodeScratchFile JSON encodes v into a new scratch file and returns its name.
func jsonEncodeScratchFile(ctx *wf.TaskContext, fs *task.ScratchFS, v any) (name string, _ error) {
	name, f, err := fs.OpenWrite(ctx, "f.json")
	if err != nil {
		return "", err
	}
	e := json.NewEncoder(f)
	e.SetIndent("", "\t")
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return name, nil
}
