// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"net/http"

	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"

	"golang.org/x/build/app/cache"
	"golang.org/x/build/app/key"
)

func initHandler(w http.ResponseWriter, r *http.Request) {
	d := goDash
	c := d.Context(appengine.NewContext(r))
	defer cache.Tick(c)
	for _, p := range d.Packages {
		err := datastore.Get(c, p.Key(c), new(Package))
		if _, ok := err.(*datastore.ErrFieldMismatch); ok {
			// Some fields have been removed, so it's okay to ignore this error.
			err = nil
		}
		if err == nil {
			continue
		} else if err != datastore.ErrNoSuchEntity {
			logErr(w, r, err)
			return
		}
		p.NextNum = 1 // So we can add the first commit.
		if _, err := datastore.Put(c, p.Key(c), p); err != nil {
			logErr(w, r, err)
			return
		}
	}

	// Create secret key.
	key.Secret(c)

	fmt.Fprint(w, "OK")
}
