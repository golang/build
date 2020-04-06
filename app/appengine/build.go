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
	"math/rand"
	pathpkg "path"
	"strings"

	"cloud.google.com/go/datastore"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/loghash"
)

const (
	maxDatastoreStringLen = 500
)

func dsKey(kind, name string, parent *datastore.Key) *datastore.Key {
	dk := datastore.NameKey(kind, name, parent)
	dk.Namespace = "Git"
	return dk
}

// A Package describes a package that is listed on the dashboard.
type Package struct {
	Name string // "Go", "arch", "net", ...
	Path string // empty for the main Go tree, else "golang.org/x/foo"
}

func (p *Package) String() string {
	return fmt.Sprintf("%s: %q", p.Path, p.Name)
}

func (p *Package) Key() *datastore.Key {
	key := p.Path
	if key == "" {
		key = "go"
	}
	return dsKey("Package", key, nil)
}

// filterDatastoreError returns err, unless it's just about datastore
// not being able to load an entity with old legacy struct fields into
// the Commit type that has since removed those fields.
func filterDatastoreError(err error) error {
	return filterAppEngineError(err, func(err error) bool {
		if em, ok := err.(*datastore.ErrFieldMismatch); ok {
			switch em.FieldName {
			case "NeedsBenchmarking", "TryPatch", "FailNotificationSent":
				// Removed in CLs 208397 and 208324.
				return true
			case "PackagePath", "ParentHash", "Num", "User", "Desc", "Time", "Branch", "NextNum", "Kind":
				// Removed in move to maintner in CL 208697.
				return true
			}
		}
		return false
	})
}

// filterNoSuchEntity returns err, unless it's just about datastore
// not being able to load an entity because it doesn't exist.
func filterNoSuchEntity(err error) error {
	return filterAppEngineError(err, func(err error) bool {
		return err == datastore.ErrNoSuchEntity
	})
}

// filterAppEngineError returns err, unless ignore(err) is true,
// in which case it returns nil. If err is an datastore.MultiError,
// it returns either nil (if all errors are ignored) or a deep copy
// with the non-ignored errors.
func filterAppEngineError(err error, ignore func(error) bool) error {
	if err == nil || ignore(err) {
		return nil
	}
	if me, ok := err.(datastore.MultiError); ok {
		me2 := make(datastore.MultiError, 0, len(me))
		for _, err := range me {
			if e2 := filterAppEngineError(err, ignore); e2 != nil {
				me2 = append(me2, e2)
			}
		}
		if len(me2) == 0 {
			return nil
		}
		return me2
	}
	return err
}

// getOrMakePackageInTx fetches a Package by path from the datastore,
// creating it if necessary.
func getOrMakePackageInTx(ctx context.Context, tx *datastore.Transaction, path string) (*Package, error) {
	p := &Package{Path: path}
	if path != "" {
		p.Name = pathpkg.Base(path)
	} else {
		p.Name = "Go"
	}
	err := tx.Get(p.Key(), p)
	err = filterDatastoreError(err)
	if err == datastore.ErrNoSuchEntity {
		if _, err := tx.Put(p.Key(), p); err != nil {
			return nil, err
		}
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
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

	// ResultData is the Data string of each build Result for this Commit.
	// For non-Go commits, only the Results for the current Go tip, weekly,
	// and release Tags are stored here. This is purely de-normalized data.
	// The complete data set is stored in Result entities.
	ResultData []string `datastore:",noindex"`
}

func (com *Commit) Key() *datastore.Key {
	if com.Hash == "" {
		panic("tried Key on Commit with empty Hash")
	}
	p := Package{Path: com.PackagePath}
	key := com.PackagePath + "|" + com.Hash
	return dsKey("Commit", key, p.Key())
}

// Valid reports whether the commit is valid.
func (c *Commit) Valid() bool {
	// Valid really just means the hash is populated.
	return validHash(c.Hash)
}

// each result line is approx 105 bytes. This constant is a tradeoff between
// build history and the AppEngine datastore limit of 1mb.
const maxResults = 1000

// AddResult adds the denormalized Result data to the Commit's
// ResultData field.
func (com *Commit) AddResult(tx *datastore.Transaction, r *Result) error {
	err := tx.Get(com.Key(), com)
	if err == datastore.ErrNoSuchEntity {
		// If it doesn't exist, we create it below.
	} else {
		err = filterDatastoreError(err)
		if err != nil {
			return fmt.Errorf("Commit.AddResult, getting Commit: %v", err)
		}
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
	if !com.Valid() {
		return errors.New("putting Commit: commit is not valid")
	}
	if _, err := tx.Put(com.Key(), com); err != nil {
		return fmt.Errorf("putting Commit: %v", err)
	}
	return nil
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
//
// For the main Go repo, goHash is the empty string.
func (c *Commit) Result(builder, goHash string) *Result {
	return result(c.ResultData, c.Hash, c.PackagePath, builder, goHash)
}

// Result returns the build Result for this commit for the given builder/goHash.
//
// For the main Go repo, goHash is the empty string.
func (c *CommitInfo) Result(builder, goHash string) *Result {
	if r := result(c.ResultData, c.Hash, c.PackagePath, builder, goHash); r != nil {
		return r
	}
	if u, ok := c.BuildingURLs[builderAndGoHash{builder, goHash}]; ok {
		return &Result{
			Builder:     builder,
			BuildingURL: u,
			Hash:        c.Hash,
			GoHash:      goHash,
		}
	}
	if *fakeResults {
		switch rand.Intn(3) {
		default:
			return nil
		case 1:
			return &Result{
				Builder: builder,
				Hash:    c.Hash,
				GoHash:  goHash,
				OK:      true,
			}
		case 2:
			return &Result{
				Builder: builder,
				Hash:    c.Hash,
				GoHash:  goHash,
				LogHash: "fakefailureurl",
			}
		}
	}
	return nil
}

func result(resultData []string, hash, packagePath, builder, goHash string) *Result {
	for _, r := range resultData {
		if !strings.HasPrefix(r, builder) {
			// Avoid strings.SplitN alloc in the common case.
			continue
		}
		p := strings.SplitN(r, "|", 4)
		if len(p) != 4 || p[0] != builder || p[3] != goHash {
			continue
		}
		return partsToResult(hash, packagePath, p)
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
		// Unknown builder, so not tested.
		return true
	}
	return !bc.BuildsRepoPostSubmit(repo, branch, goBranch)
}

// knownIssue returns a known issue for the named builder,
// or zero if there isn't a known issue.
func knownIssue(builder string) int {
	bc, ok := dashboard.Builders[builder]
	if !ok {
		// Unknown builder.
		return 0
	}
	return bc.KnownIssue
}

// Results returns the build Results for this Commit.
func (c *CommitInfo) Results() (results []*Result) {
	for _, r := range c.ResultData {
		p := strings.SplitN(r, "|", 4)
		if len(p) != 4 {
			continue
		}
		results = append(results, partsToResult(c.Hash, c.PackagePath, p))
	}
	return
}

// ResultGoHashes, for non-go repos, returns the list of Go hashes that
// this repo has been (or should be) built at.
//
// For the main Go repo it always returns a slice with 1 element: the
// empty string.
func (c *CommitInfo) ResultGoHashes() []string {
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

// partsToResult creates a Result from ResultData substrings.
func partsToResult(hash, packagePath string, p []string) *Result {
	return &Result{
		Builder:     p[0],
		Hash:        hash,
		PackagePath: packagePath,
		GoHash:      p[3],
		OK:          p[1] == "true",
		LogHash:     p[2],
	}
}

// A Result describes a build result for a Commit on an OS/architecture.
//
// Each Result entity is a descendant of its associated Package entity.
type Result struct {
	Builder     string // "os-arch[-note]"
	PackagePath string // (empty for Go commits, else "golang.org/x/foo")
	Hash        string

	// The Go Commit this was built against (when PackagePath != ""; empty for Go commits).
	GoHash string

	BuildingURL string `datastore:"-"` // non-empty if currently building
	OK          bool
	Log         string `datastore:"-"`        // for JSON unmarshaling only
	LogHash     string `datastore:",noindex"` // Key to the Log record.

	RunTime int64 // time to build+test in nanoseconds
}

func (r *Result) Key() *datastore.Key {
	p := Package{Path: r.PackagePath}
	key := r.Builder + "|" + r.PackagePath + "|" + r.Hash + "|" + r.GoHash
	return dsKey("Result", key, p.Key())
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
	CompressedLog []byte `datastore:",noindex"`
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
	key := dsKey("Log", hash, nil)
	_, err = datastoreClient.Put(c, key, &Log{b.Bytes()})
	return
}
