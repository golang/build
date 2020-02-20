// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package dashboard

import (
	"context"
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
	if err := cl.GetMulti(ctx, keys, out); err != nil {
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
