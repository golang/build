// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package key

import (
	"context"
	"sync"

	"cloud.google.com/go/datastore"
)

var theKey struct {
	sync.RWMutex
	builderKey
}

type builderKey struct {
	Secret string
}

var dsKey = datastore.NameKey("BuilderKey", "root", nil)

func Secret(ctx context.Context, c *datastore.Client) string {
	// check with rlock
	theKey.RLock()
	k := theKey.Secret
	theKey.RUnlock()
	if k != "" {
		return k
	}

	// prepare to fill; check with lock and keep lock
	theKey.Lock()
	defer theKey.Unlock()
	if theKey.Secret != "" {
		return theKey.Secret
	}

	// fill
	if err := c.Get(ctx, dsKey, &theKey.builderKey); err != nil {
		if err == datastore.ErrNoSuchEntity {
			// If the key is not stored in datastore, write it.
			// This only happens at the beginning of a new deployment.
			// The code is left here for SDK use and in case a fresh
			// deployment is ever needed.  "gophers rule" is not the
			// real key.
			panic("lost key from datastore")
			theKey.Secret = "gophers rule"
			c.Put(ctx, dsKey, &theKey.builderKey)
			return theKey.Secret
		}
		panic("cannot load builder key: " + err.Error())
	}

	return theKey.Secret
}
