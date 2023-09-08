// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package main

import (
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"path/filepath"
	"strings"
)

var hgwebPy = `#!/usr/bin/env python3
config = b"../hgweb.cfg"

import cgitb; cgitb.enable()

from mercurial import demandimport

demandimport.enable()
from mercurial.hgweb import hgweb, wsgicgi

application = hgweb(config)
wsgicgi.launch(application)
`

var hgwebCfg = `
[paths]
/hg/ = /DIR/hg/*
`

func hgHandler() http.Handler {
	py := filepath.Join(*dir, "hgweb.py")
	if err := os.WriteFile(py, []byte(hgwebPy), 0777); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*dir, "hgweb.cfg"), []byte(strings.Replace(hgwebCfg, "DIR", *dir, -1)), 0777); err != nil {
		log.Fatal(err)
	}
	os.Mkdir(filepath.Join(*dir, "hg"), 0777)

	return &cgi.Handler{
		Path: py,
		Dir:  filepath.Join(*dir, "hg"),
	}
}
