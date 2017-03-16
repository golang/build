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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/maintner/maintpb"
)

type gitHash interface {
	String() string
	Less(gitHash) bool
}

// gitSHA1 (the value type) is the current (only) implementation of
// the gitHash interface.
type gitSHA1 [20]byte

func (h gitSHA1) String() string { return fmt.Sprintf("%x", h[:]) }
func (h gitSHA1) Less(h2 gitHash) bool {
	switch h2 := h2.(type) {
	case gitSHA1:
		return bytes.Compare(h[:], h2[:]) < 0
	default:
		panic("unsupported type")
	}
}

func gitHashFromHexStr(s string) gitHash {
	if len(s) != 40 {
		panic(fmt.Sprintf("bogus git hash %q", s))
	}
	var hash gitSHA1
	n, err := hex.Decode(hash[:], []byte(s)) // TODO: garbage
	if n != 20 || err != nil {
		panic(fmt.Sprintf("bogus git hash %q", s))
	}
	return hash
}

func gitHashFromHex(s []byte) gitHash {
	if len(s) != 40 {
		panic(fmt.Sprintf("bogus git hash %q", s))
	}
	var hash gitSHA1
	n, err := hex.Decode(hash[:], s)
	if n != 20 || err != nil {
		panic(fmt.Sprintf("bogus git hash %q", s))
	}
	return hash
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

// PollGitCommits polls for git commits in a directory.
func (c *Corpus) PollGitCommits(ctx context.Context, conf polledGitCommits) error {
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
	refHash := gitHashFromHexStr(ref)
	c.mu.Lock()
	c.enqueueCommitLocked(refHash)
	c.mu.Unlock()

	idle := false
	for {
		hash := c.gitCommitToIndex()
		if hash == nil {
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
	log.Printf("TODO: poll %v from %v", conf.repo, conf.dir)
	select {} // TODO(bradfitz): actuall poll
	return nil
}

// returns nil if no work.
func (c *Corpus) gitCommitToIndex() gitHash {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for hash := range c.gitCommitTodo {
		return hash
	}
	return nil
}

var (
	nlnl           = []byte("\n\n")
	parentSpace    = []byte("parent ")
	authorSpace    = []byte("author ")
	committerSpace = []byte("committer ")
	treeSpace      = []byte("tree ")
	golangHgSpace  = []byte("golang-hg ")
)

func (c *Corpus) indexCommit(conf polledGitCommits, hash gitHash) error {
	if conf.repo == nil {
		panic("bogus config; nil repo")
	}
	cmd := exec.Command("git", "cat-file", "commit", hash.String())
	cmd.Dir = conf.dir
	catFile, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git cat-file -p %v: %v", hash, err)
	}
	cmd = exec.Command("git", "diff-tree", "--numstat", hash.String())
	cmd.Dir = conf.dir
	diffTreeOut, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git diff-tree --numstat %v: %v", hash, err)
	}

	c.mu.Lock()
	if _, ok := c.gitCommit[hash]; ok {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
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
		return err
	}
	commit := &maintpb.GitCommit{
		Raw:      catFile,
		DiffTree: diffTree,
	}
	switch hash.(type) {
	case gitSHA1:
		commit.Sha1 = hash.String()
	default:
		return fmt.Errorf("unsupported git hash type %T", hash)
	}
	m := &maintpb.Mutation{
		Git: &maintpb.GitMutation{
			Repo:   conf.repo,
			Commit: commit,
		},
	}
	c.processMutation(m)
	return nil
}

// Note: c.mu is held for writing.
func (c *Corpus) processGitMutation(m *maintpb.GitMutation) {
	commit := m.Commit
	if commit == nil {
		return
	}
	if len(commit.Sha1) != 40 {
		return
	}
	hash := gitHashFromHexStr(commit.Sha1)

	catFile := commit.Raw
	i := bytes.Index(catFile, nlnl)
	if i == 0 {
		log.Printf("Unparseable commit %q", hash)
		return
	}
	hdr, msg := catFile[:i], catFile[i+2:]
	gc := &gitCommit{
		hash:    hash,
		parents: make([]gitHash, 0, bytes.Count(hdr, parentSpace)),
		msg:     string(msg),
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
			parentHash := gitHashFromHex(ln[len(parentSpace):])
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
			gc.tree = gitHashFromHex(ln[len(treeSpace):])
			return nil
		}
		if bytes.HasPrefix(ln, golangHgSpace) {
			if c.gitOfHg == nil {
				c.gitOfHg = map[string]gitHash{}
			}
			c.gitOfHg[string(ln[len(golangHgSpace):])] = hash
			return nil
		}
		log.Printf("in commit %s, unrecognized line %q", hash, ln)
		return nil
	})
	if err != nil {
		log.Printf("Unparseable commit %q: %v", hash, err)
		return
	}
	if c.gitCommit == nil {
		c.gitCommit = map[gitHash]*gitCommit{}
	}
	c.gitCommit[hash] = gc
	if c.gitCommitTodo != nil {
		delete(c.gitCommitTodo, hash)
	}
	if n := len(c.gitCommit); n%100 == 0 {
		log.Printf("Num git commits = %v", n)
	}
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

var personRx = regexp.MustCompile(`^(.+) (\d+) ([\+\-]\d\d\d\d)\s*$`)

//
// parsePerson parses an "author" or "committer" value from "git cat-file -p COMMIT"
// The values are like:
//    Foo Bar <foobar@gmail.com> 1488624439 +0900
// c.mu must be held for writing.
func (c *Corpus) parsePerson(v []byte) (*gitPerson, time.Time, error) {
	m := personRx.FindSubmatch(v) // TODO(bradfitz): for speed, don't use regexp :(
	if m == nil {
		return nil, time.Time{}, errors.New("failed to match person")
	}

	ut, err := strconv.ParseInt(string(m[2]), 10, 64)
	if err != nil {
		return nil, time.Time{}, err
	}
	t := time.Unix(ut, 0).In(c.gitLocation(string(m[3])))

	p, ok := c.gitPeople[string(m[1])]
	if !ok {
		p = &gitPerson{str: string(m[1])}
		if c.gitPeople == nil {
			c.gitPeople = map[string]*gitPerson{}
		}
		c.gitPeople[p.str] = p
	}
	return p, t, nil

}

// v is like '[+-]hhmm'
// c.mu must be held for writing.
func (c *Corpus) gitLocation(v string) *time.Location {
	if loc, ok := c.zoneCache[v]; ok {
		return loc
	}
	h, _ := strconv.Atoi(v[1:3])
	m, _ := strconv.Atoi(v[3:5])
	east := 1
	if v[0] == '-' {
		east = -1
	}
	loc := time.FixedZone(v, east*(h*3600+m*60))
	if c.zoneCache == nil {
		c.zoneCache = map[string]*time.Location{}
	}
	c.zoneCache[v] = loc
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
