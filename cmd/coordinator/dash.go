// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

// Code interacting with build.golang.org ("the dashboard").

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/secret"
)

// dash is copied from the builder binary. It runs the given method and command on the dashboard.
//
// TODO(bradfitz,adg): unify this somewhere?
//
// If args is non-nil it is encoded as the URL query string.
// If req is non-nil it is JSON-encoded and passed as the body of the HTTP POST.
// If resp is non-nil the server's response is decoded into the value pointed
// to by resp (resp must be a pointer).
func dash(meth, cmd string, args url.Values, req, resp interface{}) error {
	const builderVersion = 1 // keep in sync with dashboard/app/build/handler.go
	argsCopy := url.Values{"version": {fmt.Sprint(builderVersion)}}
	for k, v := range args {
		if k == "version" {
			panic(`dash: reserved args key: "version"`)
		}
		argsCopy[k] = v
	}
	var r *http.Response
	var err error
	cmd = pool.NewGCEConfiguration().BuildEnv().DashBase() + cmd + "?" + argsCopy.Encode()
	switch meth {
	case "GET":
		if req != nil {
			log.Panicf("%s to %s with req", meth, cmd)
		}
		r, err = http.Get(cmd)
	case "POST":
		var body io.Reader
		if req != nil {
			b, err := json.Marshal(req)
			if err != nil {
				return err
			}
			body = bytes.NewBuffer(b)
		}
		r, err = http.Post(cmd, "text/json", body)
	default:
		log.Panicf("%s: invalid method %q", cmd, meth)
		panic("invalid method: " + meth)
	}
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return fmt.Errorf("bad http response: %v", r.Status)
	}
	body := new(bytes.Buffer)
	if _, err := body.ReadFrom(r.Body); err != nil {
		return err
	}

	// Read JSON-encoded Response into provided resp
	// and return an error if present.
	if err = json.Unmarshal(body.Bytes(), resp); err != nil {
		log.Printf("json unmarshal %#q: %s\n", body.Bytes(), err)
		return err
	}

	return nil
}

// recordResult sends build results to the dashboard.
// This is not used for trybot runs; only those after commit.
// The URLs end up looking like https://build.golang.org/log/$HEXDIGEST
func recordResult(br buildgo.BuilderRev, ok bool, buildLog string, runTime time.Duration) error {
	req := map[string]interface{}{
		"Builder":     br.Name,
		"PackagePath": "",
		"Hash":        br.Rev,
		"GoHash":      "",
		"OK":          ok,
		"Log":         buildLog,
		"RunTime":     runTime,
	}
	if br.IsSubrepo() {
		req["PackagePath"] = importPathOfRepo(br.SubName)
		req["Hash"] = br.SubRev
		req["GoHash"] = br.Rev
	}
	args := url.Values{"key": {builderKey(br.Name)}, "builder": {br.Name}}
	if *mode == "dev" {
		log.Printf("In dev mode, not recording result: %v", req)
		return nil
	}
	var result struct {
		Response interface{}
		Error    string
	}
	if err := dash("POST", "result", args, req, &result); err != nil {
		return err
	}
	if result.Error != "" {
		return errors.New(result.Error)
	}
	return nil
}

var (
	keyOnce        sync.Once
	masterKeyCache []byte
)

// mustInitMasterKeyCache populates the masterKeyCache
// with the master key. If no master key is found it will
// log an error and exit. If `masterKey()` is called before
// the key is initialzed then it will log an error and exit.
func mustInitMasterKeyCache(sc *secret.Client) {
	keyOnce.Do(func() { loadKey(sc) })
}

func masterKey() []byte {
	if len(masterKeyCache) == 0 {
		log.Fatalf("No builder master key available")
	}
	return masterKeyCache
}

func builderKey(builder string) string {
	master := masterKey()
	if len(master) == 0 {
		return ""
	}
	h := hmac.New(md5.New, master)
	io.WriteString(h, builder)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func loadKey(sc *secret.Client) {
	if *masterKeyFile != "" {
		b, err := ioutil.ReadFile(*masterKeyFile)
		if err != nil {
			log.Fatal(err)
		}
		masterKeyCache = bytes.TrimSpace(b)
		return
	}
	if *mode == "dev" {
		masterKeyCache = []byte("gophers rule")
		return
	}

	if !metadata.OnGCE() {
		log.Fatalf("No builder master key provided")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	masterKey, err := sc.Retrieve(ctx, secret.NameBuilderMasterKey)
	if err != nil {
		log.Fatalf("No builder master key available: %v", err)
	}
	masterKeyCache = []byte(strings.TrimSpace(masterKey))
}
