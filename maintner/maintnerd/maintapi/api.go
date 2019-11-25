// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package maintapi exposes a gRPC maintner service for a given corpus.
package maintapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/maintner/maintnerd/maintapi/version"
	"golang.org/x/build/repos"
	"grpc.go4.org"
	"grpc.go4.org/codes"
)

// NewAPIService creates a gRPC Server that serves the Maintner API for the given corpus.
func NewAPIService(corpus *maintner.Corpus) apipb.MaintnerServiceServer {
	return apiService{corpus}
}

// apiService implements apipb.MaintnerServiceServer using the Corpus c.
type apiService struct {
	c *maintner.Corpus
	// There really shouldn't be any more fields here.
	// All state should be in c.
	// A bool like "in staging" should just be a global flag.
}

func (s apiService) HasAncestor(ctx context.Context, req *apipb.HasAncestorRequest) (*apipb.HasAncestorResponse, error) {
	if len(req.Commit) != 40 {
		return nil, errors.New("invalid Commit")
	}
	if len(req.Ancestor) != 40 {
		return nil, errors.New("invalid Ancestor")
	}
	s.c.RLock()
	defer s.c.RUnlock()

	commit := s.c.GitCommit(req.Commit)
	res := new(apipb.HasAncestorResponse)
	if commit == nil {
		// TODO: wait for it? kick off a fetch of it and then answer?
		// optional?
		res.UnknownCommit = true
		return res, nil
	}
	if a := s.c.GitCommit(req.Ancestor); a != nil {
		res.HasAncestor = commit.HasAncestor(a)
	}
	return res, nil
}

func isStagingCommit(cl *maintner.GerritCL) bool {
	return cl.Commit != nil &&
		strings.Contains(cl.Commit.Msg, "DO NOT SUBMIT") &&
		strings.Contains(cl.Commit.Msg, "STAGING")
}

func tryBotStatus(cl *maintner.GerritCL, forStaging bool) (try, done bool) {
	if cl.Commit == nil {
		return // shouldn't happen
	}
	if forStaging != isStagingCommit(cl) {
		return
	}
	for _, msg := range cl.Messages {
		if msg.Version != cl.Version {
			continue
		}
		firstLine := msg.Message
		if nl := strings.IndexByte(firstLine, '\n'); nl != -1 {
			firstLine = firstLine[:nl]
		}
		if !strings.Contains(firstLine, "TryBot") {
			continue
		}
		if strings.Contains(firstLine, "Run-TryBot+1") {
			try = true
		}
		if strings.Contains(firstLine, "-Run-TryBot") {
			try = false
		}
		if strings.Contains(firstLine, "TryBot-Result") {
			done = true
		}
	}
	return
}

var (
	tryCommentRx = regexp.MustCompile(`(?m)^TRY=(.*)$`)
	patchSetRx   = regexp.MustCompile(`^Patch Set (\d{1,4}):`)
)

func tryWorkItem(cl *maintner.GerritCL, ci *gerrit.ChangeInfo) *apipb.GerritTryWorkItem {
	work := &apipb.GerritTryWorkItem{
		Project:  cl.Project.Project(),
		Branch:   strings.TrimPrefix(cl.Branch(), "refs/heads/"),
		ChangeId: cl.ChangeID(),
		Commit:   cl.Commit.Hash.String(),
	}
	if ci != nil {
		if ci.CurrentRevision != "" {
			// In case maintner is behind.
			work.Commit = ci.CurrentRevision
			work.Version = int32(ci.Revisions[ci.CurrentRevision].PatchSetNumber)
		}
		// Also include any "TRY=foo" comments (just the "foo"
		// aprt) from messages that accompany Run-TryBot+1
		// votes.
		for _, m := range ci.Messages {
			// msg is like:
			//   "Patch Set 2: Run-TryBot+1\n\nTRY=foo2"
			//   "Patch Set 2: Run-TryBot+1 Code-Review-2"
			//   "Uploaded patch set 2."
			//   "Removed Run-TryBot+1 by Brad Fitzpatrick <bradfitz@golang.org>\n"
			//   "Patch Set 1: Run-TryBot+1\n\nTRY=baz"
			msg := m.Message
			if !strings.Contains(msg, "\n\nTRY=") ||
				!strings.HasPrefix(msg, "Patch Set ") ||
				!strings.Contains(firstLine(msg), "Run-TryBot+1") {
				continue
			}
			pm := patchSetRx.FindStringSubmatch(msg)
			var patchSet int
			if pm != nil {
				patchSet, _ = strconv.Atoi(pm[1])
			}
			if tm := tryCommentRx.FindStringSubmatch(msg); tm != nil && patchSet > 0 {
				work.TryMessage = append(work.TryMessage, &apipb.TryVoteMessage{
					Message:  tm[1],
					AuthorId: m.Author.NumericID,
					Version:  int32(patchSet),
				})
			}
		}
	}
	return work
}

func firstLine(s string) string {
	if nl := strings.Index(s, "\n"); nl < 0 {
		return s
	} else {
		return s[:nl]
	}
}

func (s apiService) GetRef(ctx context.Context, req *apipb.GetRefRequest) (*apipb.GetRefResponse, error) {
	s.c.RLock()
	defer s.c.RUnlock()
	gp := s.c.Gerrit().Project(req.GerritServer, req.GerritProject)
	if gp == nil {
		return nil, errors.New("unknown gerrit project")
	}
	res := new(apipb.GetRefResponse)
	hash := gp.Ref(req.Ref)
	if hash != "" {
		res.Value = hash.String()
	}
	return res, nil
}

var tryCache struct {
	sync.Mutex
	forNumChanges int       // number of label changes in project val is valid for
	lastPoll      time.Time // of gerrit
	val           *apipb.GoFindTryWorkResponse
}

var tryBotGerrit = gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)

func (s apiService) GoFindTryWork(ctx context.Context, req *apipb.GoFindTryWorkRequest) (*apipb.GoFindTryWorkResponse, error) {
	tryCache.Lock()
	defer tryCache.Unlock()

	s.c.RLock()
	defer s.c.RUnlock()

	// Count the number of vote label changes over time. If it's
	// the same as the last query, return a cached result without
	// hitting Gerrit.
	var sumChanges int
	s.c.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		sumChanges += gp.NumLabelChanges()
		return nil
	})

	now := time.Now()
	const maxPollInterval = 15 * time.Second

	if tryCache.val != nil &&
		(tryCache.forNumChanges == sumChanges ||
			tryCache.lastPoll.After(now.Add(-maxPollInterval))) {
		return tryCache.val, nil
	}

	tryCache.lastPoll = now

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	res, err := goFindTryWork(ctx, tryBotGerrit, s.c)
	if err != nil {
		log.Printf("maintnerd: goFindTryWork: %v", err)
		return nil, err
	}

	tryCache.val = res
	tryCache.forNumChanges = sumChanges

	log.Printf("maintnerd: GetTryWork: for label changes of %d, cached %d trywork items.",
		sumChanges, len(res.Waiting))

	return res, nil
}

func goFindTryWork(ctx context.Context, gerritc *gerrit.Client, maintc *maintner.Corpus) (*apipb.GoFindTryWorkResponse, error) {
	const query = "label:Run-TryBot=1 label:TryBot-Result=0 status:open"
	cis, err := gerritc.QueryChanges(ctx, query, gerrit.QueryChangesOpt{
		Fields: []string{"CURRENT_REVISION", "CURRENT_COMMIT", "MESSAGES"},
	})
	if err != nil {
		return nil, err
	}

	goProj := maintc.Gerrit().Project("go.googlesource.com", "go")
	supportedReleases, err := supportedGoReleases(goProj)
	if err != nil {
		return nil, err
	}

	res := new(apipb.GoFindTryWorkResponse)
	for _, ci := range cis {
		cl := maintc.Gerrit().Project("go.googlesource.com", ci.Project).CL(int32(ci.ChangeNumber))
		if cl == nil {
			log.Printf("nil Gerrit CL %v", ci.ChangeNumber)
			continue
		}
		work := tryWorkItem(cl, ci)
		if work.Project == "go" {
			// Trybot on Go repo. Set the GoVersion field based on branch name.
			if work.Branch == "master" {
				latest := supportedReleases[0]
				work.GoVersion = []*apipb.MajorMinor{{latest.Major, latest.Minor}}
			} else if major, minor, ok := parseReleaseBranchVersion(work.Branch); ok {
				// A release branch like release-branch.goX.Y.
				// Use the major-minor Go version determined from the branch name.
				work.GoVersion = []*apipb.MajorMinor{{major, minor}}
			} else {
				// A branch that is neither master nor release-branch.goX.Y.
				// I don't see a straightforward way to compute its version,
				// so use the latest Go release until we need to do more.
				latest := supportedReleases[0]
				work.GoVersion = []*apipb.MajorMinor{{latest.Major, latest.Minor}}
			}
		} else {
			// Trybot on a subrepo. Set the Go fields to master and the supported releases.
			work.GoCommit = []string{goProj.Ref("refs/heads/master").String()}
			work.GoBranch = []string{"master"}
			latest := supportedReleases[0]
			work.GoVersion = []*apipb.MajorMinor{{latest.Major, latest.Minor}}
			for _, r := range supportedReleases {
				work.GoCommit = append(work.GoCommit, r.BranchCommit)
				work.GoBranch = append(work.GoBranch, r.BranchName)
				work.GoVersion = append(work.GoVersion, &apipb.MajorMinor{r.Major, r.Minor})
			}
		}
		res.Waiting = append(res.Waiting, work)
	}

	// Sort in some stable order. The coordinator's scheduler
	// currently only uses the time the trybot run was requested,
	// and not the commit time yet, but if two trybot runs are
	// requested within the coordinator's poll interval, the
	// earlier commit being first seems fair enough. Plus it's
	// nice for interactive maintq queries to not have random
	// orders.
	sort.Slice(res.Waiting, func(i, j int) bool {
		return res.Waiting[i].Commit < res.Waiting[j].Commit
	})
	return res, nil
}

// parseTagVersion parses the major-minor-patch version triplet
// from goX, goX.Y, or goX.Y.Z tag names,
// and reports whether the tag name is valid.
//
// Tags with suffixes like "go1.2beta3" or "go1.2rc1" are rejected.
//
// For example, "go1" is parsed as version 1.0.0,
// "go1.2" is parsed as version 1.2.0,
// and "go1.2.3" is parsed as version 1.2.3.
func parseTagVersion(tagName string) (major, minor, patch int32, ok bool) {
	maj, min, pat, ok := version.ParseTag(tagName)
	return int32(maj), int32(min), int32(pat), ok
}

// parseReleaseBranchVersion parses the major-minor version pair
// from release-branch.goX or release-branch.goX.Y release branch names,
// and reports whether the release branch name is valid.
//
// For example, "release-branch.go1" is parsed as version 1.0,
// and "release-branch.go1.2" is parsed as version 1.2.
func parseReleaseBranchVersion(branchName string) (major, minor int32, ok bool) {
	maj, min, ok := version.ParseReleaseBranch(branchName)
	return int32(maj), int32(min), ok
}

// ListGoReleases lists Go releases. A release is considered to exist
// if a tag for it exists.
func (s apiService) ListGoReleases(ctx context.Context, req *apipb.ListGoReleasesRequest) (*apipb.ListGoReleasesResponse, error) {
	s.c.RLock()
	defer s.c.RUnlock()
	goProj := s.c.Gerrit().Project("go.googlesource.com", "go")
	releases, err := supportedGoReleases(goProj)
	if err != nil {
		return nil, err
	}
	return &apipb.ListGoReleasesResponse{
		Releases: releases,
	}, nil
}

// nonChangeRefLister is implemented by *maintner.GerritProject,
// or something that acts like it for testing.
type nonChangeRefLister interface {
	// ForeachNonChangeRef calls fn for each git ref on the server that is
	// not a change (code review) ref. In general, these correspond to
	// submitted changes. fn is called serially with sorted ref names.
	// Iteration stops with the first non-nil error returned by fn.
	ForeachNonChangeRef(fn func(ref string, hash maintner.GitHash) error) error
}

// supportedGoReleases returns the latest patches of releases that are
// considered supported per policy. Sorted by version with latest first.
// The returned list will be empty if and only if the error is non-nil.
func supportedGoReleases(goProj nonChangeRefLister) ([]*apipb.GoRelease, error) {
	type majorMinor struct {
		Major, Minor int32
	}
	type tag struct {
		Patch  int32
		Name   string
		Commit maintner.GitHash
	}
	type branch struct {
		Name   string
		Commit maintner.GitHash
	}
	tags := make(map[majorMinor]tag)
	branches := make(map[majorMinor]branch)

	// Iterate over Go tags and release branches. Find the latest patch
	// for each major-minor pair, and fill in the appropriate fields.
	err := goProj.ForeachNonChangeRef(func(ref string, hash maintner.GitHash) error {
		switch {
		case strings.HasPrefix(ref, "refs/tags/go"):
			// Tag.
			tagName := ref[len("refs/tags/"):]
			major, minor, patch, ok := parseTagVersion(tagName)
			if !ok {
				return nil
			}
			if t, ok := tags[majorMinor{major, minor}]; ok && patch <= t.Patch {
				// This patch version is not newer than what we've already seen, skip it.
				return nil
			}
			tags[majorMinor{major, minor}] = tag{
				Patch:  patch,
				Name:   tagName,
				Commit: hash,
			}

		case strings.HasPrefix(ref, "refs/heads/release-branch.go"):
			// Release branch.
			branchName := ref[len("refs/heads/"):]
			major, minor, ok := parseReleaseBranchVersion(branchName)
			if !ok {
				return nil
			}
			branches[majorMinor{major, minor}] = branch{
				Name:   branchName,
				Commit: hash,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// A release is considered to exist for each git tag named "goX", "goX.Y", or "goX.Y.Z",
	// as long as it has a corresponding "release-branch.goX" or "release-branch.goX.Y" release branch.
	var rs []*apipb.GoRelease
	for v, t := range tags {
		b, ok := branches[v]
		if !ok {
			// In the unlikely case a tag exists but there's no release branch for it,
			// don't consider it a release. This way, callers won't have to do this work.
			continue
		}
		rs = append(rs, &apipb.GoRelease{
			Major:        v.Major,
			Minor:        v.Minor,
			Patch:        t.Patch,
			TagName:      t.Name,
			TagCommit:    t.Commit.String(),
			BranchName:   b.Name,
			BranchCommit: b.Commit.String(),
		})
	}

	// Sort by version. Latest first.
	sort.Slice(rs, func(i, j int) bool {
		x1, y1, z1 := rs[i].Major, rs[i].Minor, rs[i].Patch
		x2, y2, z2 := rs[j].Major, rs[j].Minor, rs[j].Patch
		if x1 != x2 {
			return x1 > x2
		}
		if y1 != y2 {
			return y1 > y2
		}
		return z1 > z2
	})

	// Per policy, only the latest two releases are considered supported.
	// Return an error if there aren't at least two releases, so callers
	// don't have to check for empty list.
	if len(rs) < 2 {
		return nil, fmt.Errorf("there was a problem finding supported Go releases")
	}
	return rs[:2], nil
}

func (s apiService) GetDashboard(ctx context.Context, req *apipb.DashboardRequest) (*apipb.DashboardResponse, error) {
	s.c.RLock()
	defer s.c.RUnlock()

	res := new(apipb.DashboardResponse)
	goProj := s.c.Gerrit().Project("go.googlesource.com", "go")
	if goProj == nil {
		// Return a normal error here, without grpc code
		// NotFound, because we expect to find this.
		return nil, errors.New("go gerrit project not found")
	}
	if req.Repo == "" {
		req.Repo = "go"
	}
	projName, err := dashRepoToGerritProj(req.Repo)
	if err != nil {
		return nil, err
	}
	proj := s.c.Gerrit().Project("go.googlesource.com", projName)
	if proj == nil {
		return nil, grpc.Errorf(codes.NotFound, "repo project %q not found", projName)
	}

	// Populate res.Branches.
	const headPrefix = "refs/heads/"
	refHash := map[string]string{} // "master" -> git commit hash
	goProj.ForeachNonChangeRef(func(ref string, hash maintner.GitHash) error {
		if !strings.HasPrefix(ref, headPrefix) {
			return nil
		}
		branch := strings.TrimPrefix(ref, headPrefix)
		refHash[branch] = hash.String()
		res.Branches = append(res.Branches, branch)
		return nil
	})

	if req.Branch == "" {
		req.Branch = "master"
	}
	branch := req.Branch
	mixBranches := branch == "mixed" // mix all branches together, by commit time
	if !mixBranches && refHash[branch] == "" {
		return nil, grpc.Errorf(codes.NotFound, "unknown branch %q", branch)
	}

	commitsPerPage := int(req.MaxCommits)
	if commitsPerPage < 0 {
		return nil, grpc.Errorf(codes.InvalidArgument, "negative max commits")
	}
	if commitsPerPage > 1000 {
		commitsPerPage = 1000
	}
	if commitsPerPage == 0 {
		if mixBranches {
			commitsPerPage = 500
		} else {
			commitsPerPage = 30 // what build.golang.org historically used
		}
	}

	if req.Page < 0 {
		return nil, grpc.Errorf(codes.InvalidArgument, "invalid page")
	}
	if req.Page != 0 && mixBranches {
		return nil, grpc.Errorf(codes.InvalidArgument, "branch=mixed does not support pagination")
	}
	skip := int(req.Page) * commitsPerPage
	if skip >= 10000 {
		return nil, grpc.Errorf(codes.InvalidArgument, "too far back") // arbitrary
	}

	// Find branches to merge together.
	//
	// By default we only have one branch (the one the user
	// specified). But in mixed mode, as used by the coordinator
	// when trying to find work to do, we merge all the branches
	// together into one timeline.
	branches := []string{branch}
	if mixBranches {
		branches = res.Branches
	}
	var oldestSkipped time.Time
	res.Commits, res.CommitsTruncated, oldestSkipped = s.listDashCommits(proj, branches, commitsPerPage, skip)

	// For non-go repos, populate the Go commits that corresponding to each commit.
	if projName != "go" {
		s.addGoCommits(oldestSkipped, res.Commits)
	}

	// Populate res.RepoHeads: each Gerrit repo with what its
	// current master ref is at.
	res.RepoHeads = s.dashRepoHeads()

	// Populate res.Releases (the currently supported releases)
	// with "master" followed by the past two release branches.
	res.Releases = append(res.Releases, &apipb.GoRelease{
		BranchName:   "master",
		BranchCommit: refHash["master"],
	})
	releases, err := supportedGoReleases(goProj)
	if err != nil {
		return nil, err
	}
	res.Releases = append(res.Releases, releases...)

	return res, nil
}

// listDashCommits merges together the commits in the provided
// branches, sorted by commit time (newest first), skipping skip
// items, and stopping after commitsPerPage items.
// If len(branches) > 1, then skip must be zero.
//
// It returns the commits, whether more would follow on a later page,
// and the oldest skipped commit, if any.
func (s apiService) listDashCommits(proj *maintner.GerritProject, branches []string, commitsPerPage, skip int) (commits []*apipb.DashCommit, truncated bool, oldestSkipped time.Time) {
	mixBranches := len(branches) > 1
	if mixBranches && skip > 0 {
		panic("unsupported skip in mixed mode")
	}
	// oldestItem is the oldest item on the page. It's used to
	// stop iteration early on the 2nd and later branches when
	// len(branches) > 1.
	var oldestItem time.Time
	for _, branch := range branches {
		gh := proj.Ref("refs/heads/" + branch)
		if gh == "" {
			continue
		}
		skipped := 0
		var add []*apipb.DashCommit
		iter := s.gitLogIter(gh)
		for len(add) < commitsPerPage && iter.HasNext() {
			c := iter.Take()
			if c.CommitTime.Before(oldestItem) {
				break
			}
			if skipped >= skip {
				dc := dashCommit(c)
				dc.Branch = branch
				add = append(add, dc)
			} else {
				skipped++
				oldestSkipped = c.CommitTime
			}
		}
		commits = append(commits, add...)
		if !mixBranches {
			truncated = iter.HasNext()
			break
		}

		sort.Slice(commits, func(i, j int) bool {
			return commits[i].CommitTimeSec > commits[j].CommitTimeSec
		})
		if len(commits) > commitsPerPage {
			commits = commits[:commitsPerPage]
			truncated = true
		}
		if len(commits) > 0 {
			oldestItem = time.Unix(commits[len(commits)-1].CommitTimeSec, 0)
		}
	}
	return commits, truncated, oldestSkipped
}

// addGoCommits populates each commit's GoCommitAtTime and
// GoCommitLatest values. for the oldest and newest corresponding "go"
// repo commits, respectively. That way there's at least one
// associated Go commit (even if empty) on the dashboard when viewing
// https://build.golang.org/?repo=golang.org/x/net.
//
// The provided commits must be from most recent to oldest. The
// oldestSkipped should be the oldest commit time that's on the page
// prior to commits, or the zero value for the first (newest) page.
//
// The maintner corpus must be read-locked.
func (s apiService) addGoCommits(oldestSkipped time.Time, commits []*apipb.DashCommit) {
	if len(commits) == 0 {
		return
	}
	goProj := s.c.Gerrit().Project("go.googlesource.com", "go")
	if goProj == nil {
		// Shouldn't happen, except in tests with
		// an empty maintner corpus.
		return
	}
	// Find the oldest (last) commit.
	oldestX := time.Unix(commits[len(commits)-1].CommitTimeSec, 0)

	// Collect enough goCommits going back far enough such that we have one that's older
	// than the oldest repo item on the page.
	var goCommits []*maintner.GitCommit // newest to oldest
	lastGoHash := func() string {
		if len(goCommits) == 0 {
			return ""
		}
		return goCommits[len(goCommits)-1].Hash.String()
	}

	goIter := s.gitLogIter(goProj.Ref("refs/heads/master"))
	for goIter.HasNext() {
		c := goIter.Take()
		goCommits = append(goCommits, c)
		if c.CommitTime.Before(oldestX) {
			break
		}
	}

	for i := len(commits) - 1; i >= 0; i-- { // walk from oldest to newest
		dc := commits[i]
		var maxGoAge time.Time
		if i == 0 {
			maxGoAge = oldestSkipped
		} else {
			maxGoAge = time.Unix(commits[i-1].CommitTimeSec, 0)
		}
		dc.GoCommitAtTime = lastGoHash()
		for len(goCommits) >= 2 && goCommits[len(goCommits)-2].CommitTime.Before(maxGoAge) {
			goCommits = goCommits[:len(goCommits)-1]
		}
		dc.GoCommitLatest = lastGoHash()
	}
}

// dashRepoHeads returns the DashRepoHead for each Gerrit project on
// the go.googlesource.com server.
func (s apiService) dashRepoHeads() (heads []*apipb.DashRepoHead) {
	s.c.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		gh := gp.Ref("refs/heads/master")
		if gh == "" {
			return nil
		}
		c := gp.GitCommit(gh.String())
		if c == nil {
			return nil
		}
		heads = append(heads, &apipb.DashRepoHead{
			GerritProject: gp.Project(),
			Commit:        dashCommit(c),
		})
		return nil
	})
	sort.Slice(heads, func(i, j int) bool {
		return heads[i].GerritProject < heads[j].GerritProject
	})
	return
}

// gitLogIter is a git log iterator.
type gitLogIter struct {
	corpus *maintner.Corpus
	nexth  maintner.GitHash
	nextc  *maintner.GitCommit // lazily looked up
}

// HasNext reports whether there's another commit to be seen.
func (i *gitLogIter) HasNext() bool {
	if i.nextc == nil {
		if i.nexth == "" {
			return false
		}
		i.nextc = i.corpus.GitCommit(i.nexth.String())
	}
	return i.nextc != nil
}

// Take returns the next commit (or nil if none remains) and advances past it.
func (i *gitLogIter) Take() *maintner.GitCommit {
	if !i.HasNext() {
		return nil
	}
	ret := i.nextc
	i.nextc = nil
	if len(ret.Parents) == 0 {
		i.nexth = ""
	} else {
		// TODO: care about returning the history from both
		// sides of merge commits? Go has a linear history for
		// the most part so punting for now. I think the old
		// build.golang.org datastore model got confused by
		// this too. In any case, this is like:
		//    git log --first-parent.
		i.nexth = ret.Parents[0].Hash
	}
	return ret
}

// Peek returns the next commit (or nil if none remains) without advancing past it.
// The next call to Peek or Take will return it again.
func (i *gitLogIter) Peek() *maintner.GitCommit {
	if i.HasNext() {
		// HasNext guarantees that it populates i.nextc.
		return i.nextc
	}
	return nil
}

func (s apiService) gitLogIter(start maintner.GitHash) *gitLogIter {
	return &gitLogIter{
		corpus: s.c,
		nexth:  start,
	}
}

func dashCommit(c *maintner.GitCommit) *apipb.DashCommit {
	return &apipb.DashCommit{
		Commit:        c.Hash.String(),
		CommitTimeSec: c.CommitTime.Unix(),
		AuthorName:    c.Author.Name(),
		AuthorEmail:   c.Author.Email(),
		Title:         c.Summary(),
	}
}

// dashRepoToGerritProj maps a DashboardRequest.repo value to
// a go.googlesource.com Gerrit project name.
func dashRepoToGerritProj(repo string) (proj string, err error) {
	if repo == "go" || repo == "" {
		return "go", nil
	}
	ri, ok := repos.ByImportPath[repo]
	if !ok || ri.GoGerritProject == "" {
		return "", grpc.Errorf(codes.NotFound, `unknown repo %q; must be empty, "go", or "golang.org/*"`, repo)
	}
	return ri.GoGerritProject, nil
}
