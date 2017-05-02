// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Logic to interact with a Gerrit server. Gerrit has an entire Git-based
// protocol for fetching metadata about CL's, reviewers, patch comments, which
// is used here - we don't use the x/build/gerrit client, which hits the API.
// TODO: write about Gerrit's Git API.

package maintner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/maintner/maintpb"
)

// Gerrit holds information about a number of Gerrit projects.
type Gerrit struct {
	c        *Corpus
	projects map[string]*GerritProject // keyed by "go.googlesource.com/build"

	clsReferencingGithubIssue map[GitHubIssueRef][]*GerritCL
}

// c.mu must be held
func (g *Gerrit) getOrCreateProject(gerritProj string) *GerritProject {
	proj, ok := g.projects[gerritProj]
	if ok {
		return proj
	}
	proj = &GerritProject{
		gerrit: g,
		proj:   gerritProj,
		cls:    map[int32]*GerritCL{},
		remote: map[gerritCLVersion]GitHash{},
	}
	g.projects[gerritProj] = proj
	return proj
}

// ForeachProjectUnsorted calls fn for each known Gerrit project.
// Iteration ends if fn returns a non-nil value.
func (g *Gerrit) ForeachProjectUnsorted(fn func(*GerritProject) error) error {
	for _, p := range g.projects {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

// GerritProject represents a single Gerrit project.
type GerritProject struct {
	gerrit *Gerrit
	proj   string // "go.googlesource.com/net"
	cls    map[int32]*GerritCL
	remote map[gerritCLVersion]GitHash
	need   map[GitHash]bool
}

func (gp *GerritProject) gitDir() string {
	return filepath.Join(gp.gerrit.c.getDataDir(), url.PathEscape(gp.proj))
}

func (gp *GerritProject) ServerSlashProject() string { return gp.proj }

// Server returns the Gerrit server, such as "go.googlesource.com".
func (gp *GerritProject) Server() string {
	if i := strings.IndexByte(gp.proj, '/'); i != -1 {
		return gp.proj[:i]
	}
	return ""
}

// Project returns the Gerrit project on the server, such as "go" or "crypto".
func (gp *GerritProject) Project() string {
	if i := strings.IndexByte(gp.proj, '/'); i != -1 {
		return gp.proj[i+1:]
	}
	return ""
}

// ForeachOpenCL calls fn for each open CL in the repo.
//
// If fn returns an error, iteration ends and ForeachIssue returns
// with that error.
//
// The fn function is called serially, with increasingly numbered
// CLs.
func (gp *GerritProject) ForeachOpenCL(fn func(*GerritCL) error) error {
	var s []*GerritCL
	for _, cl := range gp.cls {
		if cl.Status != "new" {
			continue
		}
		s = append(s, cl)
	}
	sort.Slice(s, func(i, j int) bool { return s[i].Number < s[j].Number })
	for _, cl := range s {
		if err := fn(cl); err != nil {
			return err
		}
	}
	return nil
}

func (gp *GerritProject) ForeachCLUnsorted(fn func(*GerritCL) error) error {
	for _, cl := range gp.cls {
		if err := fn(cl); err != nil {
			return err
		}
	}
	return nil
}

func (gp *GerritProject) logf(format string, args ...interface{}) {
	log.Printf("gerrit "+gp.proj+": "+format, args...)
}

// gerritCLVersion is a value type used as a map key to store a CL
// number and a patchset version. Its Version field is overloaded
// to reference the "meta" metadata commit if the Version is 0.
type gerritCLVersion struct {
	CLNumber int32
	Version  int32 // version 0 is used for the "meta" ref.
}

// A GerritCL represents a single change in Gerrit.
type GerritCL struct {
	// Project is the project this CL is part of.
	Project *GerritProject

	// Number is the CL number on the Gerrit
	// server. (e.g. 1, 2, 3)
	Number int32

	// Version is the number of versions of the patchset for this
	// CL seen so far. It starts at 1.
	Version int32

	// Commit is the git commit of the latest version of this CL.
	// Previous versions are available via GerritProject.remote.
	Commit *GitCommit

	// Meta is the head of the most recent Gerrit "meta" commit
	// for this CL. This is guaranteed to be a linear history
	// back to a CL-specific root commit for this meta branch.
	Meta *GitCommit

	// Status will be "merged", "abandoned", "new", or "draft".
	Status string

	// GitHubIssueRefs are parsed references to GitHub issues.
	GitHubIssueRefs []GitHubIssueRef
}

// References reports whether cl includes a commit message reference
// to the provided Github issue ref.
func (cl *GerritCL) References(ref GitHubIssueRef) bool {
	for _, eref := range cl.GitHubIssueRefs {
		if eref == ref {
			return true
		}
	}
	return false
}

func (cl *GerritCL) updateGithubIssueRefs() {
	gp := cl.Project
	gerrit := gp.gerrit
	gc := cl.Commit

	oldRefs := cl.GitHubIssueRefs
	newRefs := gerrit.c.parseGithubRefs(gp.proj, gc.Msg)
	cl.GitHubIssueRefs = newRefs
	for _, ref := range newRefs {
		if !clSliceContains(gerrit.clsReferencingGithubIssue[ref], cl) {
			// TODO: make this as small as
			// possible? Most will have length
			// 1. Care about default capacity of
			// 2?
			gerrit.clsReferencingGithubIssue[ref] = append(gerrit.clsReferencingGithubIssue[ref], cl)
		}
	}
	for _, ref := range oldRefs {
		if !cl.References(ref) {
			// TODO: remove ref from gerrit.clsReferencingGithubIssue
			// It could be a map of maps I suppose, but not as compact.
			// So uses a slice as the second layer, since there will normally
			// be one item.
		}
	}
}

// c.mu must be held
func (c *Corpus) initGerrit() {
	if c.gerrit != nil {
		return
	}
	c.gerrit = &Gerrit{
		c:                         c,
		projects:                  map[string]*GerritProject{},
		clsReferencingGithubIssue: map[GitHubIssueRef][]*GerritCL{},
	}
}

type watchedGerritRepo struct {
	project *GerritProject
}

// TrackGerrit registers the Gerrit project with the given project as a project
// to watch and append to the mutation log. Only valid in leader mode.
// The provided string should be of the form "hostname/project", without a scheme
// or trailing slash.
func (c *Corpus) TrackGerrit(gerritProj string) {
	if c.mutationLogger == nil {
		panic("can't TrackGerrit in non-leader mode")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if strings.Count(gerritProj, "/") != 1 {
		panic(fmt.Sprintf("gerrit project argument %q expected to contain exactly 1 slash", gerritProj))
	}
	c.initGerrit()
	if _, dup := c.gerrit.projects[gerritProj]; dup {
		panic("duplicated watched gerrit project " + gerritProj)
	}
	project := c.gerrit.getOrCreateProject(gerritProj)
	if project == nil {
		panic("gerrit project not created")
	}
	c.watchedGerritRepos = append(c.watchedGerritRepos, watchedGerritRepo{
		project: project,
	})
}

// called with c.mu Locked
func (c *Corpus) processGerritMutation(gm *maintpb.GerritMutation) {
	if c.gerrit == nil {
		// TODO: option to ignore mutation if user isn't interested.
		c.initGerrit()
	}
	gp, ok := c.gerrit.projects[gm.Project]
	if !ok {
		// TODO: option to ignore mutation if user isn't interested.
		// For now, always process the record.
		gp = c.gerrit.getOrCreateProject(gm.Project)
	}
	gp.processMutation(gm)
}

var statusIndicator = "\nStatus: "

// The Go Gerrit site does not really use the "draft" status much, but if
// you need to test it, create a dummy commit and then run
//
//     git push origin HEAD:refs/drafts/master
var statuses = []string{"merged", "abandoned", "draft", "new"}

// getGerritStatus takes a current and previous commit, and returns a Gerrit
// status, or the empty string to indicate the status did not change between the
// two commits.
//
// getGerritStatus relies on the Gerrit code review convention of amending
// the meta commit to include the current status of the CL. The Gerrit search
// bar allows you to search for changes with the following statuses: "open",
// "reviewed", "closed", "abandoned", "merged", "draft", "pending". The REST API
// returns only "NEW", "DRAFT", "ABANDONED", "MERGED". Gerrit attaches "draft",
// "abandoned", "new", and "merged" statuses to some meta commits; you may have
// to search the current meta commit's parents to find the last good commit.
//
// Corpus.mu must be held.
func (gp *GerritProject) getGerritStatus(currentMeta, oldMeta *GitCommit) string {
	commit := currentMeta
	c := gp.gerrit.c
	for {
		idx := strings.Index(commit.Msg, statusIndicator)
		if idx > -1 {
			off := idx + len(statusIndicator)
			for _, status := range statuses {
				if strings.HasPrefix(commit.Msg[off:], status) {
					return status
				}
			}
		}
		if len(commit.Parents) == 0 {
			return "new"
		}
		parentHash := commit.Parents[0] // meta tree has no merge commits
		commit = c.gitCommit[parentHash]
		if commit == nil {
			gp.logf("getGerritStatus: did not find parent commit %s", parentHash)
			return "new"
		}
		if oldMeta != nil && commit.Hash == oldMeta.Hash {
			return ""
		}
	}
}

// called with c.mu Locked
func (gp *GerritProject) processMutation(gm *maintpb.GerritMutation) {
	c := gp.gerrit.c

	for _, refp := range gm.Refs {
		m := rxChangeRef.FindStringSubmatch(refp.Ref)
		if m == nil {
			continue
		}
		clNum64, err := strconv.ParseInt(m[1], 10, 32)
		version, ok := gerritVersionNumber(m[2])
		if !ok || err != nil {
			continue
		}
		hash := c.gitHashFromHexStr(refp.Sha1)
		gc, ok := c.gitCommit[hash]
		if !ok {
			gp.logf("ERROR: ref %v references unknown hash %v; ignoring", refp, hash)
			continue
		}
		clv := gerritCLVersion{int32(clNum64), version}
		gp.remote[clv] = hash
		cl := gp.getOrCreateCL(clv.CLNumber)
		if clv.Version == 0 {
			oldMeta := cl.Meta
			cl.Meta = gc
			if status := gp.getGerritStatus(cl.Meta, oldMeta); status != "" {
				cl.Status = status
			}
		} else {
			cl.Commit = gc
			cl.Version = clv.Version
			cl.updateGithubIssueRefs()
		}
		if c.didInit {
			gp.logf("Ref %+v => %v", clv, hash)
		}
	}

	for _, commitp := range gm.Commits {
		gc, err := c.processGitCommit(commitp)
		if err != nil {
			continue
		}
		if gp.need != nil {
			delete(gp.need, gc.Hash)
		}
		for _, p := range gc.Parents {
			gp.markNeededCommit(p)
		}
	}
}

// clSliceContains reports whether cls contains cl.
func clSliceContains(cls []*GerritCL, cl *GerritCL) bool {
	for _, v := range cls {
		if v == cl {
			return true
		}
	}
	return false
}

// c.mu must be held
func (gp *GerritProject) markNeededCommit(hash GitHash) {
	c := gp.gerrit.c
	if _, ok := c.gitCommit[hash]; ok {
		// Already have it.
		return
	}
	if gp.need == nil {
		gp.need = map[GitHash]bool{}
	}
	gp.need[hash] = true
}

// c.mu must be held
func (gp *GerritProject) getOrCreateCL(num int32) *GerritCL {
	cl, ok := gp.cls[num]
	if ok {
		return cl
	}
	cl = &GerritCL{
		Project: gp,
		Number:  num,
	}
	gp.cls[num] = cl
	return cl
}

func gerritVersionNumber(s string) (version int32, ok bool) {
	if s == "meta" {
		return 0, true
	}
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(v), true
}

// rxRemoteRef matches "git ls-remote" lines.
//
// sample row:
// fd1e71f1594ce64941a85428ddef2fbb0ad1023e	refs/changes/99/30599/3
//
// Capture values:
//   $0: whole match
//   $1: "fd1e71f1594ce64941a85428ddef2fbb0ad1023e"
//   $2: "30599" (CL number)
//   $3: "1", "2" (patchset number) or "meta" (a/ special commit
//       holding the comments for a commit)
//
// The "99" in the middle covers all CL's that end in "99", so
// refs/changes/99/99/1, refs/changes/99/199/meta.
var rxRemoteRef = regexp.MustCompile(`^([0-9a-f]{40,})\s+refs/changes/[0-9a-f]{2}/([0-9]+)/(.+)$`)

// $1: change num
// $2: version or "meta"
var rxChangeRef = regexp.MustCompile(`^refs/changes/[0-9a-f]{2}/([0-9]+)/(meta|(?:\d+))`)

func (gp *GerritProject) sync(ctx context.Context, loop bool) error {
	if err := gp.init(ctx); err != nil {
		gp.logf("init: %v", err)
		return err
	}
	activityCh := gp.gerrit.c.activityChan("gerrit:" + gp.proj)
	for {
		if err := gp.syncOnce(ctx); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				err = fmt.Errorf("%v; stderr=%q", err, ee.Stderr)
			}
			gp.logf("sync: %v", err)
			return err
		}
		if !loop {
			return nil
		}
		timer := time.NewTimer(15 * time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-activityCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (gp *GerritProject) syncOnce(ctx context.Context) error {
	c := gp.gerrit.c
	gitDir := gp.gitDir()

	fetchCtx, cancel := context.WithTimeout(ctx, time.Minute)
	cmd := exec.CommandContext(fetchCtx, "git", "fetch", "origin")
	cmd.Dir = gitDir
	out, err := cmd.CombinedOutput()
	cancel()
	if err != nil {
		return fmt.Errorf("git fetch origin: %v, %s", err, out)
	}

	cmd = exec.CommandContext(ctx, "git", "ls-remote")
	cmd.Dir = gitDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git ls-remote: %v, %s", err, out)
	}

	var changedRefs []*maintpb.GitRef
	var toFetch []GitHash

	bs := bufio.NewScanner(bytes.NewReader(out))

	// Take the lock here to access gp.remote and call c.gitHashFromHex.
	// It's acceptable to take such a coarse-looking lock because
	// it's not actually around I/O: all the input from ls-remote has
	// already been slurped into memory.
	c.mu.Lock()
	for bs.Scan() {
		m := rxRemoteRef.FindSubmatch(bs.Bytes())
		if m == nil {
			continue
		}
		clNum, err := strconv.ParseInt(string(m[2]), 10, 32)
		version, ok := gerritVersionNumber(string(m[3]))
		if err != nil || !ok {
			continue
		}
		sha1 := m[1]
		hash := c.gitHashFromHex(sha1)

		curHash := gp.remote[gerritCLVersion{int32(clNum), version}]

		if curHash != hash {
			toFetch = append(toFetch, hash)
			changedRefs = append(changedRefs, &maintpb.GitRef{
				Ref:  strings.TrimSpace(bs.Text()[len(sha1):]),
				Sha1: string(sha1),
			})
		}
	}
	c.mu.Unlock()
	if err := bs.Err(); err != nil {
		return err
	}
	if len(changedRefs) == 0 {
		return nil
	}
	gp.logf("%d new refs", len(changedRefs))
	const batchSize = 250
	for len(toFetch) > 0 {
		batch := toFetch
		if len(batch) > batchSize {
			batch = batch[:batchSize]
		}
		if err := gp.fetchHashes(ctx, batch); err != nil {
			return err
		}

		c.mu.Lock()
		for _, hash := range batch {
			gp.markNeededCommit(hash)
		}
		c.mu.Unlock()

		n, err := gp.syncCommits(ctx)
		if err != nil {
			return err
		}
		toFetch = toFetch[len(batch):]
		gp.logf("synced %v commits for %d new hashes, %d hashes remain", n, len(batch), len(toFetch))

		c.addMutation(&maintpb.Mutation{
			Gerrit: &maintpb.GerritMutation{
				Project: gp.proj,
				Refs:    changedRefs[:len(batch)],
			}})
		changedRefs = changedRefs[len(batch):]
	}
	return nil
}

func (gp *GerritProject) syncCommits(ctx context.Context) (n int, err error) {
	c := gp.gerrit.c
	lastLog := time.Now()
	for {
		hash := gp.commitToIndex()
		if hash == "" {
			return n, nil
		}
		now := time.Now()
		if lastLog.Before(now.Add(-1 * time.Second)) {
			lastLog = now
			gp.logf("parsing commits (%v done)", n)
		}
		commit, err := parseCommitFromGit(gp.gitDir(), hash)
		if err != nil {
			return n, err
		}
		c.addMutation(&maintpb.Mutation{
			Gerrit: &maintpb.GerritMutation{
				Project: gp.proj,
				Commits: []*maintpb.GitCommit{commit},
			},
		})
		n++
	}
}

func (gp *GerritProject) commitToIndex() GitHash {
	c := gp.gerrit.c

	c.mu.RLock()
	defer c.mu.RUnlock()
	for hash := range gp.need {
		return hash
	}
	return ""
}

var (
	statusSpace = []byte("Status: ")
)

func (gp *GerritProject) fetchHashes(ctx context.Context, hashes []GitHash) error {
	args := []string{"fetch", "--quiet", "origin"}
	for _, hash := range hashes {
		args = append(args, hash.String())
	}
	gp.logf("fetching %v hashes...", len(hashes))
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = gp.gitDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("error fetching %d hashes from gerrit project %s: %s", len(hashes), gp.proj, out)
		return err
	}
	gp.logf("fetched %v hashes.", len(hashes))
	return nil
}

func formatExecError(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Sprintf("%v; stderr=%q", err, ee.Stderr)
	}
	return fmt.Sprint(err)
}

func (gp *GerritProject) init(ctx context.Context) error {
	gitDir := gp.gitDir()
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		return err
	}
	// try to short circuit a git init error, since the init error matching is
	// brittle
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("looking for git binary: %v", err)
	}

	if _, err := os.Stat(filepath.Join(gitDir, ".git", "config")); err == nil {
		cmd := exec.CommandContext(ctx, "git", "remote", "-v")
		cmd.Dir = gitDir
		remoteBytes, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("running git remote -v in %v: %v", gitDir, formatExecError(err))
		}
		if !strings.Contains(string(remoteBytes), "origin") && !strings.Contains(string(remoteBytes), "https://"+gp.proj) {
			return fmt.Errorf("didn't find origin & gp.url in remote output %s", string(remoteBytes))
		}
		gp.logf("git directory exists.")
		return nil
	}

	cmd := exec.CommandContext(ctx, "git", "init")
	buf := new(bytes.Buffer)
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.Dir = gitDir
	if err := cmd.Run(); err != nil {
		log.Printf(`Error running "git init": %s`, buf.String())
		return err
	}
	buf.Reset()
	cmd = exec.CommandContext(ctx, "git", "remote", "add", "origin", "https://"+gp.proj)
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.Dir = gitDir
	if err := cmd.Run(); err != nil {
		log.Printf(`Error running "git remote add origin": %s`, buf.String())
		return err
	}

	return nil
}
