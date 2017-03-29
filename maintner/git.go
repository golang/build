// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/maintner/maintpb"
)

// gitHash is a git commit in binary form (NOT hex form).
// They are currently always 20 bytes long. (for SHA-1 refs)
// That may change in the future.
type gitHash string

func (h gitHash) String() string { return fmt.Sprintf("%x", string(h)) }

// requires c.mu be held for writing
func (c *Corpus) gitHashFromHexStr(s string) gitHash {
	if len(s) != 40 {
		panic(fmt.Sprintf("bogus git hash %q", s))
	}
	var buf [40]byte
	copy(buf[:], s)
	_, err := hex.Decode(buf[:20], buf[:]) // aliasing is safe
	if err != nil {
		panic(fmt.Sprintf("bogus git hash %q: %v", s, err))
	}
	return gitHash(c.strb(buf[:20]))
}

// requires c.mu be held for writing
func (c *Corpus) gitHashFromHex(s []byte) gitHash {
	if len(s) != 40 {
		panic(fmt.Sprintf("bogus git hash %q", s))
	}
	var buf [20]byte
	_, err := hex.Decode(buf[:], s)
	if err != nil {
		panic(fmt.Sprintf("bogus git hash %q: %v", s, err))
	}
	return gitHash(c.strb(buf[:20]))
}

type gitCommit struct {
	hash       gitHash
	tree       gitHash
	parents    []gitHash
	author     *gitPerson
	authorTime time.Time
	committer  *gitPerson
	commitTime time.Time
	msg        string
	files      []*maintpb.GitDiffTreeFile
}

type gitPerson struct {
	str string // "Foo Bar <foo@bar.com>"
}

// requires c.mu be held for writing.
func (c *Corpus) enqueueCommitLocked(h gitHash) {
	if _, ok := c.gitCommit[h]; ok {
		return
	}
	if c.gitCommitTodo == nil {
		c.gitCommitTodo = map[gitHash]bool{}
	}
	c.gitCommitTodo[h] = true
}

// syncGitCommits polls for git commits in a directory.
func (c *Corpus) syncGitCommits(ctx context.Context, conf polledGitCommits, loop bool) error {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "refs/remotes/origin/master")
	cmd.Dir = conf.dir
	out, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}
	outs := strings.TrimSpace(string(out))
	if outs == "" {
		return fmt.Errorf("no remote found for refs/remotes/origin/master")
	}
	ref := strings.Fields(outs)[0]
	c.mu.Lock()
	refHash := c.gitHashFromHexStr(ref)
	c.enqueueCommitLocked(refHash)
	c.mu.Unlock()

	idle := false
	for {
		hash := c.gitCommitToIndex()
		if hash == "" {
			if !loop {
				return nil
			}
			if !idle {
				log.Printf("All git commits index for %v; idle.", conf.repo)
				idle = true
			}
			time.Sleep(5 * time.Second)
			continue
		}
		if err := c.indexCommit(conf, hash); err != nil {
			log.Printf("Error indexing %v: %v", hash, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			// TODO: temporary vs permanent failure? reschedule? fail hard?
			// For now just loop with a sleep.
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// returns nil if no work.
func (c *Corpus) gitCommitToIndex() gitHash {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for hash := range c.gitCommitTodo {
		if _, ok := c.gitCommit[hash]; !ok {
			return hash
		}
		log.Printf("Warning: git commit %v in todo map, but already known; ignoring", hash)
	}
	return ""
}

var (
	nlnl           = []byte("\n\n")
	parentSpace    = []byte("parent ")
	authorSpace    = []byte("author ")
	committerSpace = []byte("committer ")
	treeSpace      = []byte("tree ")
	golangHgSpace  = []byte("golang-hg ")
	gpgSigSpace    = []byte("gpgsig ")
	encodingSpace  = []byte("encoding ")
	space          = []byte(" ")
)

func parseCommitFromGit(dir string, hash gitHash) (*maintpb.GitCommit, error) {
	cmd := exec.Command("git", "cat-file", "commit", hash.String())
	cmd.Dir = dir
	catFile, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file -p %v: %v", hash, err)
	}
	cmd = exec.Command("git", "diff-tree", "--numstat", hash.String())
	cmd.Dir = dir
	diffTreeOut, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff-tree --numstat %v: %v", hash, err)
	}

	diffTree := &maintpb.GitDiffTree{}
	bs := bufio.NewScanner(bytes.NewReader(diffTreeOut))
	lineNum := 0
	for bs.Scan() {
		line := strings.TrimSpace(bs.Text())
		lineNum++
		if lineNum == 1 && line == hash.String() {
			continue
		}
		f := strings.Fields(line)
		// A line is like: <added> WS+ <deleted> WS+ <filename>
		// Where <added> or <deleted> can be '-' to mean binary.
		// The filename could contain spaces.
		// 49      8       maintner/maintner.go
		// Or:
		// 49      8       some/name with spaces.txt
		if len(f) < 3 {
			continue
		}
		binary := f[0] == "-" || f[1] == "-"
		added, _ := strconv.ParseInt(f[0], 10, 64)
		deleted, _ := strconv.ParseInt(f[1], 10, 64)
		file := strings.TrimPrefix(line, f[0])
		file = strings.TrimSpace(file)
		file = strings.TrimPrefix(file, f[1])
		file = strings.TrimSpace(file)

		diffTree.File = append(diffTree.File, &maintpb.GitDiffTreeFile{
			File:    file,
			Added:   added,
			Deleted: deleted,
			Binary:  binary,
		})
	}
	if err := bs.Err(); err != nil {
		return nil, err
	}
	commit := &maintpb.GitCommit{
		Raw:      catFile,
		DiffTree: diffTree,
	}
	switch len(hash) {
	case 20:
		commit.Sha1 = hash.String()
	default:
		return nil, fmt.Errorf("unsupported git hash %q", hash.String())
	}
	return commit, nil
}

func (c *Corpus) indexCommit(conf polledGitCommits, hash gitHash) error {
	if conf.repo == nil {
		panic("bogus config; nil repo")
	}
	commit, err := parseCommitFromGit(conf.dir, hash)
	if err != nil {
		return err
	}
	m := &maintpb.Mutation{
		Git: &maintpb.GitMutation{
			Repo:   conf.repo,
			Commit: commit,
		},
	}
	c.addMutation(m)
	return nil
}

// c.mu is held for writing.
func (c *Corpus) processGitMutation(m *maintpb.GitMutation) {
	commit := m.Commit
	if commit == nil {
		return
	}
	// TODO: care about m.Repo?
	c.processGitCommit(commit)
}

// c.mu is held for writing.
func (c *Corpus) processGitCommit(commit *maintpb.GitCommit) (*gitCommit, error) {
	if len(commit.Sha1) != 40 {
		return nil, fmt.Errorf("bogus git sha1 %q", commit.Sha1)
	}
	hash := c.gitHashFromHexStr(commit.Sha1)

	catFile := commit.Raw
	i := bytes.Index(catFile, nlnl)
	if i == 0 {
		return nil, fmt.Errorf("commit %v lacks double newline", hash)
	}
	hdr, msg := catFile[:i], catFile[i+2:]
	gc := &gitCommit{
		hash:    hash,
		parents: make([]gitHash, 0, bytes.Count(hdr, parentSpace)),
		msg:     c.strb(msg),
	}
	if commit.DiffTree != nil {
		gc.files = commit.DiffTree.File
	}
	for _, f := range gc.files {
		f.File = c.str(f.File) // intern the string
	}
	parents := 0
	err := foreachLine(hdr, func(ln []byte) error {
		if bytes.HasPrefix(ln, parentSpace) {
			parents++
			parentHash := c.gitHashFromHex(ln[len(parentSpace):])
			gc.parents = append(gc.parents, parentHash)
			c.enqueueCommitLocked(parentHash)
			return nil
		}
		if bytes.HasPrefix(ln, authorSpace) {
			p, t, err := c.parsePerson(ln[len(authorSpace):])
			if err != nil {
				return fmt.Errorf("unrecognized author line %q: %v", ln, err)
			}
			gc.author = p
			gc.authorTime = t
			return nil
		}
		if bytes.HasPrefix(ln, committerSpace) {
			p, t, err := c.parsePerson(ln[len(committerSpace):])
			if err != nil {
				return fmt.Errorf("unrecognized committer line %q: %v", ln, err)
			}
			gc.committer = p
			gc.commitTime = t
			return nil
		}
		if bytes.HasPrefix(ln, treeSpace) {
			gc.tree = c.gitHashFromHex(ln[len(treeSpace):])
			return nil
		}
		if bytes.HasPrefix(ln, golangHgSpace) {
			if c.gitOfHg == nil {
				c.gitOfHg = map[string]gitHash{}
			}
			c.gitOfHg[string(ln[len(golangHgSpace):])] = hash
			return nil
		}
		if bytes.HasPrefix(ln, gpgSigSpace) || bytes.HasPrefix(ln, space) {
			// Jessie Frazelle is a unique butterfly.
			return nil
		}
		if bytes.HasPrefix(ln, encodingSpace) {
			// Also ignore this. In practice this has only
			// been seen to declare that a commit's
			// metadata is utf-8 when the author name has
			// non-ASCII.
			return nil
		}
		log.Printf("in commit %s, unrecognized line %q", hash, ln)
		return nil
	})
	if err != nil {
		log.Printf("Unparseable commit %q: %v", hash, err)
		return nil, fmt.Errorf("Unparseable commit %q: %v", hash, err)
	}
	if c.gitCommit == nil {
		c.gitCommit = map[gitHash]*gitCommit{}
	}
	c.gitCommit[hash] = gc
	if c.gitCommitTodo != nil {
		delete(c.gitCommitTodo, hash)
	}
	if c.Verbose {
		now := time.Now()
		if now.After(c.lastGitCount.Add(time.Second)) {
			c.lastGitCount = now
			log.Printf("Num git commits = %v", len(c.gitCommit))
		}
	}
	return gc, nil
}

// calls f on each non-empty line in v, without the trailing \n. the
// final line need not include a trailing \n. Returns first non-nil
// error returned by f.
func foreachLine(v []byte, f func([]byte) error) error {
	for len(v) > 0 {
		i := bytes.IndexByte(v, '\n')
		if i < 0 {
			return f(v)
		}
		if err := f(v[:i]); err != nil {
			return err
		}
		v = v[i+1:]
	}
	return nil
}

// parsePerson parses an "author" or "committer" value from "git cat-file -p COMMIT"
// The values are like:
//    Foo Bar <foobar@gmail.com> 1488624439 +0900
// c.mu must be held for writing.
func (c *Corpus) parsePerson(v []byte) (*gitPerson, time.Time, error) {
	v = bytes.TrimSpace(v)

	lastSpace := bytes.LastIndexByte(v, ' ')
	if lastSpace < 0 {
		return nil, time.Time{}, errors.New("failed to match person")
	}
	tz := v[lastSpace+1:] // "+0800"
	v = v[:lastSpace]     // now v is "Foo Bar <foobar@gmail.com> 1488624439"

	lastSpace = bytes.LastIndexByte(v, ' ')
	if lastSpace < 0 {
		return nil, time.Time{}, errors.New("failed to match person")
	}
	unixTime := v[lastSpace+1:]
	nameEmail := v[:lastSpace] // now v is "Foo Bar <foobar@gmail.com>"

	ut, err := strconv.ParseInt(string(unixTime), 10, 64)
	if err != nil {
		return nil, time.Time{}, err
	}
	t := time.Unix(ut, 0).In(c.gitLocation(tz))

	p, ok := c.gitPeople[string(nameEmail)]
	if !ok {
		p = &gitPerson{str: string(nameEmail)}
		if c.gitPeople == nil {
			c.gitPeople = map[string]*gitPerson{}
		}
		c.gitPeople[p.str] = p
	}
	return p, t, nil

}

// v is like '[+-]hhmm'
// c.mu must be held for writing.
func (c *Corpus) gitLocation(v []byte) *time.Location {
	if loc, ok := c.zoneCache[string(v)]; ok {
		return loc
	}
	s := string(v)
	h, _ := strconv.Atoi(s[1:3])
	m, _ := strconv.Atoi(s[3:5])
	east := 1
	if v[0] == '-' {
		east = -1
	}
	loc := time.FixedZone(s, east*(h*3600+m*60))
	if c.zoneCache == nil {
		c.zoneCache = map[string]*time.Location{}
	}
	c.zoneCache[s] = loc
	return loc
}

type FileCount struct {
	File  string
	Count int
}

// queryFrequentlyModifiedFiles is an example query just for fun.
// It is not currently used by anything.
func (c *Corpus) QueryFrequentlyModifiedFiles(topN int) []FileCount {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := map[string]int{} // file -> count
	for _, gc := range c.gitCommit {
		for _, f := range gc.files {
			n[modernizeFilename(f.File)]++
		}
	}
	files := make([]FileCount, 0, len(n))
	for file, count := range n {
		files = append(files, FileCount{file, count})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Count > files[j].Count
	})
	if len(files) > topN {
		files = files[:topN]
	}
	return files
}

func modernizeFilename(f string) string {
	if strings.HasPrefix(f, "src/pkg/") {
		f = "src/" + strings.TrimPrefix(f, "src/pkg/")
	}
	if strings.HasPrefix(f, "src/http/") {
		f = "src/net/http/" + strings.TrimPrefix(f, "src/http/")
	}
	return f
}
