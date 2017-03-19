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
func (g *Gerrit) getOrCreateProject(gerritURL string) *GerritProject {
	proj, ok := g.projects[gerritURL]
	if ok {
		return proj
	}
	proj = &GerritProject{
		gerrit: g,
		url:    gerritURL,
		gitDir: filepath.Join(g.dataDir, url.PathEscape(gerritURL)),
		cls:    map[int32]*gerritCL{},
	}
	g.projects[gerritURL] = proj
	return proj
}

// GerritProject represents a single Gerrit project.
type GerritProject struct {
	gerrit *Gerrit
	url    string // "https://go.googlesource.com"
	// TODO: Many different Git remotes can share the same Gerrit instance, e.g.
	// the Go Gerrit instance supports build, gddo, go. For the moment these are
	// all treated separately, since the remotes are separate.
	gitDir string
	cls    map[int32]*gerritCL
}

type gerritCL struct {
	Hash       gitHash
	Number     int32
	Author     *gitPerson
	AuthorTime time.Time
	Status     string // "merged", "abandoned", "new"
	// TODO...
}

// gerritMetaCommit holds data about the "meta commit" object that Gerrit
// returns for a given CL.
type gerritMetaCommit struct {
	Hash   gitHash
	Number int32
	Raw    []byte
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

// AddGerrit adds the Gerrit project with the given URL to the corpus.
func (c *Corpus) AddGerrit(gerritURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initGerrit()
	project := c.gerrit.getOrCreateProject(gerritURL)
	if project == nil {
		panic("gerrit project not created")
	}
	c.watchedGerritRepos = append(c.watchedGerritRepos, watchedGerritRepo{
		project: project,
	})
}

func (c *Corpus) processGerritMutation(gm *maintpb.GerritMutation) {
	panic("TODO")
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

func (gp *GerritProject) sync(ctx context.Context, loop bool) error {
	if err := gp.init(ctx); err != nil {
		return err
	}
	for {
		if err := gp.syncOnce(ctx); err != nil {
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

	// TODO abstract the cmd running boilerplate
	fetchCtx, cancel := context.WithTimeout(ctx, time.Minute)
	cmd := exec.CommandContext(fetchCtx, "git", "fetch", "origin")
	buf := new(bytes.Buffer)
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.Dir = gp.gitDir
	err := cmd.Run()
	cancel()
	if err != nil {
		os.Stderr.Write(buf.Bytes())
		return err
	}
	cmd = exec.CommandContext(ctx, "git", "ls-remote")
	buf.Reset()
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.Dir = gp.gitDir
	if err := cmd.Run(); err != nil {
		os.Stderr.Write(buf.Bytes())
		return err
	}
	remoteHash := map[int32]gitHash{} // CL -> current server value
	bs := bufio.NewScanner(buf)
	for bs.Scan() {
		m := rxRemoteRef.FindSubmatch(bs.Bytes())
		if m == nil {
			continue
		}
		clNum, err := strconv.ParseInt(string(m[2]), 10, 32)
		if err != nil {
			return fmt.Errorf("maintner: error parsing CL as a number: %v", err)
		}
		remoteHash[int32(clNum)] = gitHashFromHex(m[1])

	}
	if err := bs.Err(); err != nil {
		return err
	}

	var toFetch []gitHash
	c.mu.RLock() // while we read from gp.cls
	for cl, hash := range remoteHash {
		if cl, ok := gp.cls[cl]; !ok || cl.Hash != hash {
			toFetch = append(toFetch, hash)
		}
	}
	c.mu.RUnlock()

	if err := gp.fetchHashes(ctx, toFetch); err != nil {
		return err
	}
	for cl, hash := range remoteHash {
		c.mu.RLock()
		val, ok := gp.cls[cl]
		c.mu.RUnlock()
		if !ok || val.Hash != hash {
			// TODO: parallelize updates if this gets slow, we can probably do
			// lots of filesystem reads without penalty
			if err := gp.updateCL(ctx, cl, hash); err != nil {
				return err
			}
		}
	}
	return nil
}

var (
	statusSpace = []byte("Status: ")
)

// newMutationFromCL generates a GerritCLMutation using the smallest possible
// diff between a (the state we have in memory) and b (the current Gerrit
// state).
//
// If newMutationFromCL returns nil, the provided gerrit CL is no newer than
// the data we have in the corpus. 'a' may be nil.
func (gp *GerritProject) newMutationFromCL(a *gerritCL, b *gerritMetaCommit) *maintpb.Mutation {
	if b == nil {
		panic("newMutationFromCL: provided nil gerritCL")
	}
	if a == nil {
		var sha1 string
		switch b.Hash.(type) {
		case gitSHA1:
			sha1 = b.Hash.String()
		default:
			panic(fmt.Sprintf("unsupported git hash type %T", b.Hash))
		}
		_ = sha1
		panic("TODO")
		return &maintpb.Mutation{
			Gerrit: &maintpb.GerritMutation{
			//Url:      gp.url,
			//MetaSha1: sha1,
			//Number:   b.Number,
			//MetaRaw:  b.Raw,
			},
		}
	}
	// TODO: update the existing proto
	return nil
}

// updateCL updates the local CL.
func (gp *GerritProject) updateCL(ctx context.Context, clNum int32, hash gitHash) error {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-p", hash.String())
	cmd.Dir = gp.gitDir
	buf, errBuf := new(bytes.Buffer), new(bytes.Buffer)
	cmd.Stdout = buf
	cmd.Stderr = errBuf
	if err := cmd.Run(); err != nil {
		return err
	}
	cl := &gerritMetaCommit{
		Number: clNum,
		Hash:   hash,
		Raw:    buf.Bytes(),
	}
	proto := gp.newMutationFromCL(gp.cls[clNum], cl)
	gp.gerrit.c.processMutation(proto)
	return nil
}

func (gp *GerritProject) fetchHashes(ctx context.Context, hashes []gitHash) error {
	for len(hashes) > 0 {
		batch := hashes
		if len(batch) > 500 {
			batch = batch[:500]
		}
		hashes = hashes[len(batch):]

		args := []string{"fetch", "--quiet", "origin"}
		for _, hash := range batch {
			args = append(args, hash.String())
		}
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = gp.gitDir
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("error fetching %d hashes from git remote %s: %s", len(batch), gp.url, out)
			return err
		}
	}
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
		if !strings.Contains(string(remoteBytes), "origin") && !strings.Contains(string(remoteBytes), gp.url) {
			return fmt.Errorf("didn't find origin & gp.url in remote output %s", string(remoteBytes))
		}
	} else {
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
		cmd = exec.CommandContext(ctx, "git", "remote", "add", "origin", gp.url)
		cmd.Stdout = buf
		cmd.Stderr = buf
		cmd.Dir = gp.gitDir
		if err := cmd.Run(); err != nil {
			log.Printf(`Error running "git remote add origin": %s`, buf.String())
			return err
		}
	}
	return nil
}
