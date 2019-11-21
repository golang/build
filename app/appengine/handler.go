// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/build/app/cache"
	"golang.org/x/build/app/key"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
)

const (
	commitsPerPage = 30
	watcherVersion = 3 // must match dashboard/watcher/watcher.go
	builderVersion = 1 // must match dashboard/builder/http.go
)

// commitHandler retrieves commit data or records a new commit.
//
// For GET requests it returns a Commit value for the specified
// packagePath and hash.
//
// For POST requests it reads a JSON-encoded Commit value from the request
// body and creates a new Commit entity. It also updates the "tip" Tag for
// each new commit at tip.
//
// This handler is used by a gobuilder process in -commit mode.
func commitHandler(r *http.Request) (interface{}, error) {
	c := contextForRequest(r)
	com := new(Commit)

	if r.Method == "GET" {
		com.PackagePath = r.FormValue("packagePath")
		com.Hash = r.FormValue("hash")
		err := datastore.Get(c, com.Key(c), com)
		if com.Num == 0 && com.Desc == "" {
			// Perf builder might have written an incomplete Commit.
			// Pretend it doesn't exist, so that we can get complete details.
			err = datastore.ErrNoSuchEntity
		}
		if err != nil {
			if err == datastore.ErrNoSuchEntity {
				// This error string is special.
				// The commit watcher expects it.
				// Do not change it.
				return nil, errors.New("Commit not found")
			}
			return nil, fmt.Errorf("getting Commit: %v", err)
		}
		if com.Num == 0 {
			// Corrupt state which shouldn't happen but does.
			// Return an error so builders' commit loops will
			// be willing to retry submitting this commit.
			return nil, errors.New("in datastore with zero Num")
		}
		if com.Desc == "" || com.User == "" {
			// Also shouldn't happen, but at least happened
			// once on a single commit when trying to fix data
			// in the datastore viewer UI?
			return nil, errors.New("missing field")
		}
		// Strip potentially large and unnecessary fields.
		com.ResultData = nil
		return com, nil
	}
	if r.Method != "POST" {
		return nil, errBadMethod(r.Method)
	}
	if !isMasterKey(c, r.FormValue("key")) {
		return nil, errors.New("can only POST commits with master key")
	}

	v, _ := strconv.Atoi(r.FormValue("version"))
	if v != watcherVersion {
		return nil, fmt.Errorf("rejecting POST from commit watcher; need version %v instead of %v",
			watcherVersion, v)
	}

	// POST request
	body, err := ioutil.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("reading Body: %v", err)
	}
	if err := json.Unmarshal(body, com); err != nil {
		return nil, fmt.Errorf("unmarshaling body %q: %v", body, err)
	}
	com.Desc = limitStringLength(com.Desc, maxDatastoreStringLen)
	if err := com.Valid(); err != nil {
		return nil, fmt.Errorf("validating Commit: %v", err)
	}
	defer cache.Tick(c)
	tx := func(c context.Context) error {
		return addCommit(c, com)
	}
	return nil, datastore.RunInTransaction(c, tx, nil)
}

// addCommit adds the Commit entity to the datastore and updates the tip Tag.
// It must be run inside a datastore transaction.
func addCommit(c context.Context, com *Commit) error {
	var ec Commit // existing commit
	isUpdate := false
	err := datastore.Get(c, com.Key(c), &ec)
	if err != nil && err != datastore.ErrNoSuchEntity {
		return fmt.Errorf("getting Commit: %v", err)
	}
	if err == nil {
		// Commit already in the datastore. Any fields different?
		// If not, don't do anything.
		changes := (com.Num != 0 && com.Num != ec.Num) ||
			com.ParentHash != ec.ParentHash ||
			com.Desc != ec.Desc ||
			com.User != ec.User ||
			!com.Time.Equal(ec.Time)
		if !changes {
			return nil
		}
		ec.ParentHash = com.ParentHash
		ec.Desc = com.Desc
		ec.User = com.User
		if !com.Time.IsZero() {
			ec.Time = com.Time
		}
		if com.Num != 0 {
			ec.Num = com.Num
		}
		isUpdate = true
		com = &ec
	}
	p, err := GetPackage(c, com.PackagePath)
	if err != nil {
		return fmt.Errorf("GetPackage: %v", err)
	}
	if com.Num == 0 {
		// get the next commit number
		com.Num = p.NextNum
		p.NextNum++
		if _, err := datastore.Put(c, p.Key(c), p); err != nil {
			return fmt.Errorf("putting Package: %v", err)
		}
	} else if com.Num >= p.NextNum {
		p.NextNum = com.Num + 1
		if _, err := datastore.Put(c, p.Key(c), p); err != nil {
			return fmt.Errorf("putting Package: %v", err)
		}
	}
	// if this isn't the first Commit test the parent commit exists.
	// The all zeros are returned by hg's p1node template for parentless commits.
	if com.ParentHash != "" && com.ParentHash != "0000000000000000000000000000000000000000" && com.ParentHash != "0000" {
		n, err := datastore.NewQuery("Commit").
			Filter("Hash =", com.ParentHash).
			Ancestor(p.Key(c)).
			Count(c)
		if err != nil {
			return fmt.Errorf("testing for parent Commit: %v", err)
		}
		if n == 0 {
			return errors.New("parent commit not found")
		}
	} else if com.Num != 1 {
		// This is the first commit; fail if it is not number 1.
		// (This will happen if we try to upload a new/different repo
		// where there is already commit data. A bad thing to do.)
		return errors.New("this package already has a first commit; aborting")
	}
	// Update the relevant Tag entity, if applicable.
	if !isUpdate && p.Path == "" {
		var t *Tag
		if com.Branch == "master" {
			t = &Tag{Kind: "tip", Hash: com.Hash}
		}
		if strings.HasPrefix(com.Branch, "release-branch.") {
			t = &Tag{Kind: "release", Name: com.Branch, Hash: com.Hash}
		}
		if t != nil {
			if _, err = datastore.Put(c, t.Key(c), t); err != nil {
				return fmt.Errorf("putting Tag: %v", err)
			}
		}
	}
	// put the Commit
	if err = putCommit(c, com); err != nil {
		return err
	}
	return nil
}

// tagHandler records a new tag. It reads a JSON-encoded Tag value from the
// request body and updates the Tag entity for the Kind of tag provided.
//
// This handler is used by a gobuilder process in -commit mode.
func tagHandler(r *http.Request) (interface{}, error) {
	if r.Method != "POST" {
		return nil, errBadMethod(r.Method)
	}

	t := new(Tag)
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(t); err != nil {
		return nil, err
	}
	if err := t.Valid(); err != nil {
		return nil, err
	}
	c := contextForRequest(r)
	defer cache.Tick(c)
	_, err := datastore.Put(c, t.Key(c), t)
	return nil, err
}

// packagesHandler returns a list of the non-Go Packages monitored
// by the dashboard.
func packagesHandler(r *http.Request) (interface{}, error) {
	kind := r.FormValue("kind")
	c := contextForRequest(r)
	now := cache.Now(c)
	key := "build-packages-" + kind
	var p []*Package
	if cache.Get(c, r, now, key, &p) {
		return p, nil
	}
	p, err := Packages(c, kind)
	if err != nil {
		return nil, err
	}
	cache.Set(c, r, now, key, p)
	return p, nil
}

// buildingHandler records that a build is in progress.
// The data is only stored in memcache and with a timeout. It's assumed
// that the build system will periodically refresh this if the build
// is slow.
func buildingHandler(r *http.Request) (interface{}, error) {
	if r.Method != "POST" {
		return nil, errBadMethod(r.Method)
	}
	c := contextForRequest(r)
	key := buildingKey(r.FormValue("hash"), r.FormValue("gohash"), r.FormValue("builder"))
	err := memcache.Set(c, &memcache.Item{
		Key:        key,
		Value:      []byte(r.FormValue("url")),
		Expiration: 15 * time.Minute,
	})
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"key": key,
	}, nil
}

// resultHandler records a build result.
// It reads a JSON-encoded Result value from the request body,
// creates a new Result entity, and updates the relevant Commit entity.
// If the Log field is not empty, resultHandler creates a new Log entity
// and updates the LogHash field before putting the Commit entity.
func resultHandler(r *http.Request) (interface{}, error) {
	if r.Method != "POST" {
		return nil, errBadMethod(r.Method)
	}

	v, _ := strconv.Atoi(r.FormValue("version"))
	if v != builderVersion {
		return nil, fmt.Errorf("rejecting POST from builder; need version %v instead of %v",
			builderVersion, v)
	}

	c := contextForRequest(r)
	res := new(Result)
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(res); err != nil {
		return nil, fmt.Errorf("decoding Body: %v", err)
	}
	if err := res.Valid(); err != nil {
		return nil, fmt.Errorf("validating Result: %v", err)
	}
	defer cache.Tick(c)
	// store the Log text if supplied
	if len(res.Log) > 0 {
		hash, err := PutLog(c, res.Log)
		if err != nil {
			return nil, fmt.Errorf("putting Log: %v", err)
		}
		res.LogHash = hash
	}
	tx := func(c context.Context) error {
		// check Package exists
		if _, err := GetPackage(c, res.PackagePath); err != nil {
			return fmt.Errorf("GetPackage: %v", err)
		}
		// put Result
		if _, err := datastore.Put(c, res.Key(c), res); err != nil {
			return fmt.Errorf("putting Result: %v", err)
		}
		// add Result to Commit
		com := &Commit{PackagePath: res.PackagePath, Hash: res.Hash}
		if err := com.AddResult(c, res); err != nil {
			return fmt.Errorf("AddResult: %v", err)
		}
		return nil
	}
	return nil, datastore.RunInTransaction(c, tx, nil)
}

// logHandler displays log text for a given hash.
// It handles paths like "/log/hash".
func logHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-type", "text/plain; charset=utf-8")
	c := contextForRequest(r)
	hash := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	key := datastore.NewKey(c, "Log", hash, 0, nil)
	l := new(Log)
	if err := datastore.Get(c, key, l); err != nil {
		if err == datastore.ErrNoSuchEntity {
			// Fall back to default namespace;
			// maybe this was on the old dashboard.
			c := appengine.NewContext(r)
			key := datastore.NewKey(c, "Log", hash, 0, nil)
			err = datastore.Get(c, key, l)
		}
		if err != nil {
			logErr(w, r, err)
			return
		}
	}
	b, err := l.Text()
	if err != nil {
		logErr(w, r, err)
		return
	}
	w.Write(b)
}

// clearResultsHandler purges the last commitsPerPage results for the given builder.
// It optionally takes a comma-separated list of specific hashes to clear.
func clearResultsHandler(r *http.Request) (interface{}, error) {
	if r.Method != "POST" {
		return nil, errBadMethod(r.Method)
	}
	builder := r.FormValue("builder")
	if builder == "" {
		return nil, errors.New("must specify a builder")
	}
	clearAll := r.FormValue("hash") == ""
	hash := strings.Split(r.FormValue("hash"), ",")

	c := contextForRequest(r)
	defer cache.Tick(c)
	pkg := (&Package{}).Key(c) // TODO(adg): support clearing sub-repos
	err := datastore.RunInTransaction(c, func(c context.Context) error {
		var coms []*Commit
		keys, err := datastore.NewQuery("Commit").
			Ancestor(pkg).
			Order("-Num").
			Limit(commitsPerPage).
			GetAll(c, &coms)
		if err != nil {
			return err
		}
		var rKeys []*datastore.Key
		for _, com := range coms {
			if !(clearAll || contains(hash, com.Hash)) {
				continue
			}
			r := com.Result(builder, "")
			if r == nil {
				continue
			}
			com.RemoveResult(r)
			rKeys = append(rKeys, r.Key(c))
		}
		_, err = datastore.PutMulti(c, keys, coms)
		if err != nil {
			return err
		}
		return datastore.DeleteMulti(c, rKeys)
	}, nil)
	return nil, err
}

type dashHandler func(*http.Request) (interface{}, error)

type dashResponse struct {
	Response interface{}
	Error    string
}

// errBadMethod is returned by a dashHandler when
// the request has an unsuitable method.
type errBadMethod string

func (e errBadMethod) Error() string {
	return "bad method: " + string(e)
}

func builderKeyRevoked(builder string) bool {
	switch builder {
	case "plan9-amd64-mischief":
		// Broken and unmaintained for months.
		// It's polluting the dashboard.
		return true
	case "linux-arm-onlinenet":
		// Requested to be revoked by Dave Cheney.
		// The machine is in a fail+report loop
		// and can't be accessed. Revoke it for now.
		return true
	}
	return false
}

// AuthHandler wraps a http.HandlerFunc with a handler that validates the
// supplied key and builder query parameters.
func AuthHandler(h dashHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := contextForRequest(r)

		// Put the URL Query values into r.Form to avoid parsing the
		// request body when calling r.FormValue.
		r.Form = r.URL.Query()

		var err error
		var resp interface{}

		// Validate key query parameter for POST requests only.
		key := r.FormValue("key")
		builder := r.FormValue("builder")
		if r.Method == "POST" && !validKey(c, key, builder) {
			err = fmt.Errorf("invalid key %q for builder %q", key, builder)
		}

		// Call the original HandlerFunc and return the response.
		if err == nil {
			resp, err = h(r)
		}

		// Write JSON response.
		dashResp := &dashResponse{Response: resp}
		if err != nil {
			log.Errorf(c, "%v", err)
			dashResp.Error = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		if err = json.NewEncoder(w).Encode(dashResp); err != nil {
			log.Criticalf(c, "encoding response: %v", err)
		}
	}
}

// validHash reports whether hash looks like a valid git commit hash.
func validHash(hash string) bool {
	// TODO: correctly validate a hash: check that it's exactly 40
	// lowercase hex digits. But this is what we historically did:
	return hash != ""
}

func validKey(c context.Context, key, builder string) bool {
	if isMasterKey(c, key) {
		return true
	}
	if builderKeyRevoked(builder) {
		return false
	}
	return key == builderKey(c, builder)
}

func isMasterKey(c context.Context, k string) bool {
	return appengine.IsDevAppServer() || k == key.Secret(c)
}

func builderKey(c context.Context, builder string) string {
	h := hmac.New(md5.New, []byte(key.Secret(c)))
	h.Write([]byte(builder))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func logErr(w http.ResponseWriter, r *http.Request, err error) {
	c := contextForRequest(r)
	log.Errorf(c, "Error: %v", err)
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprint(w, "Error: ", html.EscapeString(err.Error()))
}

func contextForRequest(r *http.Request) context.Context {
	return goDash.Context(appengine.NewContext(r))
}

// limitStringLength essentially does return s[:max],
// but it ensures that we dot not split UTF-8 rune in half.
// Otherwise appengine python scripts will break badly.
func limitStringLength(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for {
		s = s[:max]
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			return s
		}
		max--
	}
}
