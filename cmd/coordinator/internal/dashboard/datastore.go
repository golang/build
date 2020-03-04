// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package dashboard

import (
	"context"
	"errors"
	"log"

	"cloud.google.com/go/datastore"
)

// getDatastoreResults populates result data on commits, fetched from Datastore.
func getDatastoreResults(ctx context.Context, cl *datastore.Client, commits []*commit, pkg string) {
	var keys []*datastore.Key
	for _, c := range commits {
		pkey := datastore.NameKey("Package", pkg, nil)
		pkey.Namespace = "Git"
		key := datastore.NameKey("Commit", "|"+c.Hash, pkey)
		key.Namespace = "Git"
		keys = append(keys, key)
	}
	out := make([]*Commit, len(keys))
	// datastore.ErrNoSuchEntity is returned when we ask for a commit that we do not yet have test data.
	if err := cl.GetMulti(ctx, keys, out); err != nil && filterMultiError(err, ignoreNoSuchEntity) != nil {
		log.Printf("getResults: error fetching %d results: %v", len(keys), err)
		return
	}
	hashOut := make(map[string]*Commit)
	for _, o := range out {
		if o != nil && o.Hash != "" {
			hashOut[o.Hash] = o
		}
	}
	for _, c := range commits {
		if result, ok := hashOut[c.Hash]; ok {
			c.ResultData = result.ResultData
		}
	}
	return
}

type ignoreFunc func(err error) error

// ignoreNoSuchEntity ignores datastore.ErrNoSuchEntity, which is returned when
// we ask for a commit that we do not yet have test data.
func ignoreNoSuchEntity(err error) error {
	if !errors.Is(err, datastore.ErrNoSuchEntity) {
		return err
	}
	return nil
}

// filterMultiError loops over datastore.MultiError, skipping errors ignored by
// the specified ignoreFuncs. Any unfiltered errors will be returned as a
// datastore.MultiError error. If no errors are left, nil will be returned.
// Errors that are not datastore.MultiError will be returned as-is.
func filterMultiError(err error, ignores ...ignoreFunc) error {
	if err == nil {
		return nil
	}
	me := datastore.MultiError{}
	if ok := errors.As(err, &me); !ok {
		return err
	}
	ret := datastore.MultiError{}
	for _, err := range me {
		var skip bool
		for _, ignore := range ignores {
			if err := ignore(err); err == nil {
				skip = true
				break
			}
		}
		if !skip {
			ret = append(ret, err)
		}
	}
	if len(ret) > 0 {
		return ret
	}
	return nil
}
