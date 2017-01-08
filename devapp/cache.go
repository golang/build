// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package devapp

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"

	"golang.org/x/net/context"
)

// Cache is a datastore entity type that contains serialized data for dashboards.
type Cache struct {
	// Value contains a gzipped gob'd serialization of the object
	// to be cached. It must be []byte to avail ourselves of the
	// datastore's 1 MB size limit.
	Value []byte
}

func unpackCache(cache *Cache, data interface{}) error {
	if len(cache.Value) > 0 {
		gzr, err := gzip.NewReader(bytes.NewReader(cache.Value))
		if err != nil {
			return err
		}
		defer gzr.Close()
		if err := gob.NewDecoder(gzr).Decode(data); err != nil {
			return err
		}
	}
	return nil
}

func loadCache(ctx context.Context, name string, data interface{}) error {
	cache, err := getCache(ctx, name)
	if err != nil {
		return err
	}
	return unpackCache(cache, data)
}

func writeCache(ctx context.Context, name string, data interface{}) error {
	var cache Cache
	var cacheout bytes.Buffer
	cachegz := gzip.NewWriter(&cacheout)
	e := gob.NewEncoder(cachegz)
	if err := e.Encode(data); err != nil {
		return err
	}
	if err := cachegz.Close(); err != nil {
		return err
	}
	cache.Value = cacheout.Bytes()
	log.Infof(ctx, "Cache %q update finished; writing %d bytes", name, cacheout.Len())
	return putCache(ctx, name, &cache)
}
