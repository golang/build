// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"strings"
	"time"

	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/loghash"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
)

const (
	maxDatastoreStringLen = 500
)

// A Package describes a package that is listed on the dashboard.
type Package struct {
	Kind    string // "subrepo", "external", or empty for the main Go tree
	Name    string // "Go", "arch", "net", ...
	Path    string // empty for the main Go tree, else "golang.org/x/foo"
	NextNum int    // Num of the next head Commit
}

func (p *Package) String() string {
	return fmt.Sprintf("%s: %q", p.Path, p.Name)
}

func (p *Package) Key(c context.Context) *datastore.Key {
	key := p.Path
	if key == "" {
		key = "go"
	}
	return datastore.NewKey(c, "Package", key, 0, nil)
}

// filterDatastoreError returns err, unless it's just about datastore
// not being able to load an entity with old legacy struct fields into
// the Commit type that has since removed those fields.
func filterDatastoreError(err error) error {
	if em, ok := err.(*datastore.ErrFieldMismatch); ok {
		switch em.FieldName {
		case "NeedsBenchmarking", "TryPatch", "FailNotificationSent":
			// Removed in CLs 208397 and 208324.
			return nil
		}
	}
	if me, ok := err.(appengine.MultiError); ok {
		any := false
		for i, err := range me {
			me[i] = filterDatastoreError(err)
			if me[i] != nil {
				any = true
			}
		}
		if !any {
			return nil
		}
	}
	return err
}

// LastCommit returns the most recent Commit for this Package.
func (p *Package) LastCommit(c context.Context) (*Commit, error) {
	var commits []*Commit
	_, err := datastore.NewQuery("Commit").
		Ancestor(p.Key(c)).
		Order("-Time").
		Limit(1).
		GetAll(c, &commits)
	err = filterDatastoreError(err)
	if err != nil {
		return nil, err
	}
	if len(commits) != 1 {
		return nil, datastore.ErrNoSuchEntity
	}
	return commits[0], nil
}

// GetPackage fetches a Package by path from the datastore.
func GetPackage(c context.Context, path string) (*Package, error) {
	p := &Package{Path: path}
	err := datastore.Get(c, p.Key(c), p)
	if err == datastore.ErrNoSuchEntity {
		return nil, fmt.Errorf("package %q not found", path)
	}
	return p, err
}

type builderAndGoHash struct {
	builder, goHash string
}

// A Commit describes an individual commit in a package.
//
// Each Commit entity is a descendant of its associated Package entity.
// In other words, all Commits with the same PackagePath belong to the same
// datastore entity group.
type Commit struct {
	PackagePath string // (empty for main repo commits)
	Hash        string
	ParentHash  string
	Num         int // Internal monotonic counter unique to this package.

	User   string
	Desc   string `datastore:",noindex"`
	Time   time.Time
	Branch string

	// ResultData is the Data string of each build Result for this Commit.
	// For non-Go commits, only the Results for the current Go tip, weekly,
	// and release Tags are stored here. This is purely de-normalized data.
	// The complete data set is stored in Result entities.
	ResultData []string `datastore:",noindex"`

	buildingURLs map[builderAndGoHash]string
}

func (com *Commit) Key(c context.Context) *datastore.Key {
	if com.Hash == "" {
		panic("tried Key on Commit with empty Hash")
	}
	p := Package{Path: com.PackagePath}
	key := com.PackagePath + "|" + com.Hash
	return datastore.NewKey(c, "Commit", key, 0, p.Key(c))
}

func (c *Commit) Valid() error {
	if !validHash(c.Hash) {
		return errors.New("invalid Hash")
	}
	if c.ParentHash != "" && !validHash(c.ParentHash) { // empty is OK
		return errors.New("invalid ParentHash")
	}
	return nil
}

func putCommit(c context.Context, com *Commit) error {
	if err := com.Valid(); err != nil {
		return fmt.Errorf("putting Commit: %v", err)
	}
	if com.Num == 0 && com.ParentHash != "0000" { // 0000 is used in tests
		return fmt.Errorf("putting Commit: invalid Num (must be > 0)")
	}
	if _, err := datastore.Put(c, com.Key(c), com); err != nil {
		return fmt.Errorf("putting Commit: %v", err)
	}
	return nil
}

// each result line is approx 105 bytes. This constant is a tradeoff between
// build history and the AppEngine datastore limit of 1mb.
const maxResults = 1000

// AddResult adds the denormalized Result data to the Commit's ResultData field.
// It must be called from inside a datastore transaction.
func (com *Commit) AddResult(c context.Context, r *Result) error {
	err := datastore.Get(c, com.Key(c), com)
	err = filterDatastoreError(err)
	if err != nil {
		return fmt.Errorf("getting Commit: %v", err)
	}

	var resultExists bool
	for i, s := range com.ResultData {
		// if there already exists result data for this builder at com, overwrite it.
		if strings.HasPrefix(s, r.Builder+"|") && strings.HasSuffix(s, "|"+r.GoHash) {
			resultExists = true
			com.ResultData[i] = r.Data()
		}
	}
	if !resultExists {
		// otherwise, add the new result data for this builder.
		com.ResultData = trim(append(com.ResultData, r.Data()), maxResults)
	}
	return putCommit(c, com)
}

// removeResult removes the denormalized Result data from the ResultData field
// for the given builder and go hash.
// It must be called from within the datastore transaction that gets and puts
// the Commit. Note this is slightly different to AddResult, above.
func (com *Commit) RemoveResult(r *Result) {
	var rd []string
	for _, s := range com.ResultData {
		if strings.HasPrefix(s, r.Builder+"|") && strings.HasSuffix(s, "|"+r.GoHash) {
			continue
		}
		rd = append(rd, s)
	}
	com.ResultData = rd
}

func trim(s []string, n int) []string {
	l := min(len(s), n)
	return s[len(s)-l:]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Result returns the build Result for this Commit for the given builder/goHash.
func (c *Commit) Result(builder, goHash string) *Result {
	for _, r := range c.ResultData {
		if !strings.HasPrefix(r, builder) {
			// Avoid strings.SplitN alloc in the common case.
			continue
		}
		p := strings.SplitN(r, "|", 4)
		if len(p) != 4 || p[0] != builder || p[3] != goHash {
			continue
		}
		return partsToResult(c, p)
	}
	if u, ok := c.buildingURLs[builderAndGoHash{builder, goHash}]; ok {
		return &Result{
			Builder:     builder,
			BuildingURL: u,
			Hash:        c.Hash,
			GoHash:      goHash,
		}
	}
	return nil
}

// isUntested reports whether a cell in the build.golang.org grid is
// an untested configuration.
//
// repo is "go", "net", etc.
// branch is the branch of repo "master" or "release-branch.go1.12"
// goBranch applies only if repo != "go" and is of form "master" or "release-branch.go1.N"
//
// As a special case, "tip" is an alias for "master", since this app
// still uses a bunch of hg terms from when we used hg.
func isUntested(builder, repo, branch, goBranch string) bool {
	if branch == "tip" {
		branch = "master"
	}
	if goBranch == "tip" {
		goBranch = "master"
	}
	bc, ok := dashboard.Builders[builder]
	if !ok {
		// Not managed by coordinator. Might be an old-style builder.
		// TODO: remove this once the old-style builders are all dead.
		return false
	}
	return !bc.BuildsRepoPostSubmit(repo, branch, goBranch)
}

// Results returns the build Results for this Commit.
func (c *Commit) Results() (results []*Result) {
	for _, r := range c.ResultData {
		p := strings.SplitN(r, "|", 4)
		if len(p) != 4 {
			continue
		}
		results = append(results, partsToResult(c, p))
	}
	return
}

func (c *Commit) ResultGoHashes() []string {
	// For the main repo, just return the empty string
	// (there's no corresponding main repo hash for a main repo Commit).
	// This function is only really useful for sub-repos.
	if c.PackagePath == "" {
		return []string{""}
	}
	var hashes []string
	for _, r := range c.ResultData {
		p := strings.SplitN(r, "|", 4)
		if len(p) != 4 {
			continue
		}
		// Append only new results (use linear scan to preserve order).
		if !contains(hashes, p[3]) {
			hashes = append(hashes, p[3])
		}
	}
	// Return results in reverse order (newest first).
	reverse(hashes)
	return hashes
}

func contains(t []string, s string) bool {
	for _, s2 := range t {
		if s2 == s {
			return true
		}
	}
	return false
}

func reverse(s []string) {
	for i := 0; i < len(s)/2; i++ {
		j := len(s) - i - 1
		s[i], s[j] = s[j], s[i]
	}
}

// partsToResult converts a Commit and ResultData substrings to a Result.
func partsToResult(c *Commit, p []string) *Result {
	return &Result{
		Builder:     p[0],
		Hash:        c.Hash,
		PackagePath: c.PackagePath,
		GoHash:      p[3],
		OK:          p[1] == "true",
		LogHash:     p[2],
	}
}

// A Result describes a build result for a Commit on an OS/architecture.
//
// Each Result entity is a descendant of its associated Package entity.
type Result struct {
	PackagePath string // (empty for Go commits)
	Builder     string // "os-arch[-note]"
	Hash        string

	// The Go Commit this was built against (empty for Go commits).
	GoHash string

	BuildingURL string `datastore:"-"` // non-empty if currently building
	OK          bool
	Log         string `datastore:"-"`        // for JSON unmarshaling only
	LogHash     string `datastore:",noindex"` // Key to the Log record.

	RunTime int64 // time to build+test in nanoseconds
}

func (r *Result) Key(c context.Context) *datastore.Key {
	p := Package{Path: r.PackagePath}
	key := r.Builder + "|" + r.PackagePath + "|" + r.Hash + "|" + r.GoHash
	return datastore.NewKey(c, "Result", key, 0, p.Key(c))
}

func (r *Result) Valid() error {
	if !validHash(r.Hash) {
		return errors.New("invalid Hash")
	}
	if r.PackagePath != "" && !validHash(r.GoHash) {
		return errors.New("invalid GoHash")
	}
	return nil
}

// Data returns the Result in string format
// to be stored in Commit's ResultData field.
func (r *Result) Data() string {
	return fmt.Sprintf("%v|%v|%v|%v", r.Builder, r.OK, r.LogHash, r.GoHash)
}

// A Log is a gzip-compressed log file stored under the SHA1 hash of the
// uncompressed log text.
type Log struct {
	CompressedLog []byte
}

func (l *Log) Text() ([]byte, error) {
	d, err := gzip.NewReader(bytes.NewBuffer(l.CompressedLog))
	if err != nil {
		return nil, fmt.Errorf("reading log data: %v", err)
	}
	b, err := ioutil.ReadAll(d)
	if err != nil {
		return nil, fmt.Errorf("reading log data: %v", err)
	}
	return b, nil
}

func PutLog(c context.Context, text string) (hash string, err error) {
	b := new(bytes.Buffer)
	z, _ := gzip.NewWriterLevel(b, gzip.BestCompression)
	io.WriteString(z, text)
	z.Close()
	hash = loghash.New(text)
	key := datastore.NewKey(c, "Log", hash, 0, nil)
	_, err = datastore.Put(c, key, &Log{b.Bytes()})
	return
}

// A Tag is used to keep track of the most recent Go weekly and release tags.
// Typically there will be one Tag entity for each kind of git tag.
type Tag struct {
	Kind string // "release", or "tip"
	Name string // the tag itself (for example: "release.r60")
	Hash string
}

func (t *Tag) String() string {
	if t.Kind == "tip" {
		return "tip"
	}
	return t.Name
}

func (t *Tag) Key(c context.Context) *datastore.Key {
	p := &Package{}
	s := t.Kind
	if t.Kind == "release" {
		s += "-" + t.Name
	}
	return datastore.NewKey(c, "Tag", s, 0, p.Key(c))
}

func (t *Tag) Valid() error {
	if t.Kind != "release" && t.Kind != "tip" {
		return errors.New("invalid Kind")
	}
	if t.Kind == "release" && t.Name == "" {
		return errors.New("release must have Name")
	}
	if !validHash(t.Hash) {
		return errors.New("invalid Hash")
	}
	return nil
}

// Commit returns the Commit that corresponds with this Tag.
func (t *Tag) Commit(c context.Context) (*Commit, error) {
	com := &Commit{Hash: t.Hash}
	err := datastore.Get(c, com.Key(c), com)
	err = filterDatastoreError(err)
	return com, err
}

// GetTag fetches a Tag by name from the datastore.
func GetTag(c context.Context, kind, name string) (*Tag, error) {
	t := &Tag{Kind: kind, Name: name}
	if err := datastore.Get(c, t.Key(c), t); err != nil {
		return nil, err
	}
	if err := t.Valid(); err != nil {
		return nil, err
	}
	return t, nil
}

// Packages returns packages of the specified kind.
// Kind must be one of "external" or "subrepo".
func Packages(c context.Context, kind string) ([]*Package, error) {
	switch kind {
	case "external", "subrepo":
	default:
		return nil, errors.New(`kind must be one of "external" or "subrepo"`)
	}
	var pkgs []*Package
	q := datastore.NewQuery("Package").Filter("Kind=", kind)
	for t := q.Run(c); ; {
		pkg := new(Package)
		_, err := t.Next(pkg)
		if err == datastore.Done {
			break
		} else if err != nil {
			return nil, err
		}
		if pkg.Path != "" {
			pkgs = append(pkgs, pkg)
		}
	}
	return pkgs, nil
}
