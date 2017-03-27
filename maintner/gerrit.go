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
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/maintner/maintpb"
)

// Gerrit holds information about a number of Gerrit projects.
type Gerrit struct {
	c       *Corpus
	dataDir string // the root Corpus data directory
	// keys are like "https://go.googlesource.com/build"
	projects map[string]*GerritProject
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
		gitDir: filepath.Join(g.dataDir, url.PathEscape(gerritProj)),
		cls:    map[int32]*gerritCL{},
		remote: map[gerritCLVersion]gitHash{},
	}
	g.projects[gerritProj] = proj
	return proj
}

// GerritProject represents a single Gerrit project.
type GerritProject struct {
	gerrit *Gerrit
	proj   string // "go.googlesource.com/net"
	// TODO: Many different Git remotes can share the same Gerrit instance, e.g.
	// the Go Gerrit instance supports build, gddo, go. For the moment these are
	// all treated separately, since the remotes are separate.
	gitDir string
	cls    map[int32]*gerritCL
	remote map[gerritCLVersion]gitHash
	need   map[gitHash]bool
}

func (gp *GerritProject) logf(format string, args ...interface{}) {
	log.Printf("gerrit "+gp.proj+": "+format, args...)
}

type gerritCLVersion struct {
	CLNumber int32
	Version  int32 // version 0 is used for the "meta" ref.
}

type gerritCL struct {
	// Number is the CL number on the Gerrit
	// server. (e.g. 1, 2, 3)
	Number int32

	// Version is the number of versions of the patchset for this
	// CL seen so far. It starts at 1.
	Version int32

	// Commit is the git commit of the latest version of this CL.
	// Previous versions are available via GerritProject.remote.
	Commit *gitCommit

	// Meta is the head of the most recent Gerrit "meta" commit
	// for this CL. This is guaranteed to be a linear history
	// back to a CL-specific root commit for this meta branch.
	Meta *gitCommit

	// Status is TODO.
	// Will be something like "merged", "abandoned", "new"
	// Or maybe bools.
	// Status string
}

// c.mu must be held
func (c *Corpus) initGerrit() {
	if c.gerrit != nil {
		return
	}
	c.gerrit = &Gerrit{
		c:        c,
		dataDir:  c.dataDir,
		projects: map[string]*GerritProject{},
	}
}

type watchedGerritRepo struct {
	project *GerritProject
}

// AddGerrit adds the Gerrit project with the given project to the corpus.
// The provided string should be of the form "hostname/project", without a scheme
// or trailing slash.
func (c *Corpus) AddGerrit(gerritProj string) {
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
		hash := gitHashFromHexStr(refp.Sha1)
		gc, ok := c.gitCommit[hash]
		if !ok {
			gp.logf("ERROR: ref %v references unknown hash %v; ignoring", refp, hash)
			continue
		}
		clv := gerritCLVersion{int32(clNum64), version}
		gp.remote[clv] = hash
		cl := gp.getOrCreateCL(clv.CLNumber)
		if clv.Version == 0 {
			cl.Meta = gc
		} else {
			cl.Commit = gc
			cl.Version = clv.Version
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
			delete(gp.need, gc.hash)
		}
		for _, p := range gc.parents {
			gp.markNeededCommit(p)
		}
	}
}

// c.mu must be held
func (gp *GerritProject) markNeededCommit(hash gitHash) {
	c := gp.gerrit.c
	if _, ok := c.gitCommit[hash]; ok {
		// Already have it.
		return
	}
	if gp.need == nil {
		gp.need = map[gitHash]bool{}
	}
	gp.need[hash] = true
}

// c.mu must be held
func (gp *GerritProject) getOrCreateCL(num int32) *gerritCL {
	cl, ok := gp.cls[num]
	if ok {
		return cl
	}
	cl = &gerritCL{
		Number: num,
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
	for {
		if err := gp.syncOnce(ctx); err != nil {
			gp.logf("sync: %v", err)
			return err
		}
		if !loop {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Minute):
		}
	}
}

func (gp *GerritProject) syncOnce(ctx context.Context) error {
	c := gp.gerrit.c

	fetchCtx, cancel := context.WithTimeout(ctx, time.Minute)
	cmd := exec.CommandContext(fetchCtx, "git", "fetch", "origin")
	cmd.Dir = gp.gitDir
	out, err := cmd.CombinedOutput()
	cancel()
	if err != nil {
		return fmt.Errorf("git fetch origin: %v, %s", err, out)
	}

	cmd = exec.CommandContext(ctx, "git", "ls-remote")
	cmd.Dir = gp.gitDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git ls-remote: %v, %s", err, out)
	}

	var changedRefs []*maintpb.GitRef
	var toFetch []gitHash

	bs := bufio.NewScanner(bytes.NewReader(out))
	c.mu.RLock() // to access gp.remote; okay because ls-remote output all in out already
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
		hash := gitHashFromHex(sha1)

		curHash := gp.remote[gerritCLVersion{int32(clNum), version}]

		if curHash != hash {
			toFetch = append(toFetch, hash)
			changedRefs = append(changedRefs, &maintpb.GitRef{
				Ref:  strings.TrimSpace(bs.Text()[len(sha1):]),
				Sha1: string(sha1),
			})
		}
	}
	c.mu.RUnlock()
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
		if hash == nil {
			return n, nil
		}
		now := time.Now()
		if lastLog.Before(now.Add(-1 * time.Second)) {
			lastLog = now
			gp.logf("parsing commits (%v done)", n)
		}
		commit, err := parseCommitFromGit(gp.gitDir, hash)
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

func (gp *GerritProject) commitToIndex() gitHash {
	c := gp.gerrit.c

	c.mu.RLock()
	defer c.mu.RUnlock()
	for hash := range gp.need {
		return hash
	}
	return nil
}

var (
	statusSpace = []byte("Status: ")
)

func (gp *GerritProject) fetchHashes(ctx context.Context, hashes []gitHash) error {
	args := []string{"fetch", "--quiet", "origin"}
	for _, hash := range hashes {
		args = append(args, hash.String())
	}
	gp.logf("fetching %v hashes...", len(hashes))
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = gp.gitDir
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("error fetching %d hashes from gerrit project %s: %s", len(hashes), gp.proj, out)
		return err
	}
	gp.logf("fetched %v hashes.", len(hashes))
	return nil
}

func (gp *GerritProject) init(ctx context.Context) error {
	if err := os.MkdirAll(gp.gitDir, 0755); err != nil {
		return err
	}
	// try to short circuit a git init error, since the init error matching is
	// brittle
	if _, err := exec.LookPath("git"); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(gp.gitDir, ".git", "config")); err == nil {
		remoteBytes, err := exec.CommandContext(ctx, "git", "remote", "-v").Output()
		if err != nil {
			return err
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
	cmd.Dir = gp.gitDir
	if err := cmd.Run(); err != nil {
		log.Printf(`Error running "git init": %s`, buf.String())
		return err
	}
	buf.Reset()
	cmd = exec.CommandContext(ctx, "git", "remote", "add", "origin", "https://"+gp.proj)
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.Dir = gp.gitDir
	if err := cmd.Run(); err != nil {
		log.Printf(`Error running "git remote add origin": %s`, buf.String())
		return err
	}

	return nil
}
