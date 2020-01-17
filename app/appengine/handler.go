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
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"cloud.google.com/go/datastore"
	"golang.org/x/build/app/key"
)

const (
	commitsPerPage = 30
	builderVersion = 1 // must match x/build/cmd/coordinator/dash.go's value
)

// resultHandler records a build result.
// It reads a JSON-encoded Result value from the request body,
// creates a new Result entity, and creates or updates the relevant Commit entity.
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

	ctx := r.Context()
	res := new(Result)
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(res); err != nil {
		return nil, fmt.Errorf("decoding Body: %v", err)
	}
	if err := res.Valid(); err != nil {
		return nil, fmt.Errorf("validating Result: %v", err)
	}
	// store the Log text if supplied
	if len(res.Log) > 0 {
		hash, err := PutLog(ctx, res.Log)
		if err != nil {
			return nil, fmt.Errorf("putting Log: %v", err)
		}
		res.LogHash = hash
	}
	tx := func(tx *datastore.Transaction) error {
		if _, err := getOrMakePackageInTx(ctx, tx, res.PackagePath); err != nil {
			return fmt.Errorf("GetPackage: %v", err)
		}
		// put Result
		if _, err := tx.Put(res.Key(), res); err != nil {
			return fmt.Errorf("putting Result: %v", err)
		}
		// add Result to Commit
		com := &Commit{PackagePath: res.PackagePath, Hash: res.Hash}
		if err := com.AddResult(tx, res); err != nil {
			return fmt.Errorf("AddResult: %v", err)
		}
		return nil
	}
	_, err := datastoreClient.RunInTransaction(ctx, tx)
	return nil, err
}

// logHandler displays log text for a given hash.
// It handles paths like "/log/hash".
func logHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-type", "text/plain; charset=utf-8")
	c := r.Context()
	hash := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	key := dsKey("Log", hash, nil)
	l := new(Log)
	if err := datastoreClient.Get(c, key, l); err != nil {
		if err == datastore.ErrNoSuchEntity {
			// Fall back to default namespace;
			// maybe this was on the old dashboard.
			key.Namespace = ""
			err = datastoreClient.Get(c, key, l)
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

// clearResultsHandler purge a single build failure from the dashboard.
// It currently only supports the main Go repo.
func clearResultsHandler(r *http.Request) (interface{}, error) {
	if r.Method != "POST" {
		return nil, errBadMethod(r.Method)
	}
	builder := r.FormValue("builder")
	hash := r.FormValue("hash")
	if builder == "" {
		return nil, errors.New("missing 'builder'")
	}
	if hash == "" {
		return nil, errors.New("missing 'hash'")
	}

	ctx := r.Context()

	_, err := datastoreClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		c := &Commit{
			PackagePath: "", // TODO(adg): support clearing sub-repos
			Hash:        hash,
		}
		err := tx.Get(c.Key(), c)
		err = filterDatastoreError(err)
		if err == datastore.ErrNoSuchEntity {
			// Doesn't exist, so no build to clear.
			return nil
		}
		if err != nil {
			return err
		}

		r := c.Result(builder, "")
		if r == nil {
			// No result, so nothing to clear.
			return nil
		}
		c.RemoveResult(r)
		_, err = tx.Put(c.Key(), c)
		if err != nil {
			return err
		}
		return tx.Delete(r.Key())
	})
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
		c := r.Context()

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
			log.Printf("%v", err)
			dashResp.Error = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		if err = json.NewEncoder(w).Encode(dashResp); err != nil {
			log.Printf("encoding response: %v", err)
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

var devModeMasterKey string

func masterKey(ctx context.Context) string {
	if *dev {
		return devModeMasterKey
	}
	return key.Secret(ctx, datastoreClient)
}

func isMasterKey(ctx context.Context, k string) bool {
	return k == masterKey(ctx)
}

func builderKey(ctx context.Context, builder string) string {
	h := hmac.New(md5.New, []byte(masterKey(ctx)))
	h.Write([]byte(builder))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func logErr(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("Error: %v", err)
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprint(w, "Error: ", html.EscapeString(err.Error()))
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
