// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build !appengine

package devapp

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	stdlog "log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/context"
)

var tokenFile = flag.String("token", "", "read GitHub token personal access token from `file` (default $HOME/.github-issue-token)")

func init() {
	log = &stderrLogger{}
	// TODO don't bind to the working directory.
	http.Handle("/static/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/favicon.ico", faviconHandler)
}

type stderrLogger struct{}

func (s *stderrLogger) Infof(_ context.Context, format string, args ...interface{}) {
	stdlog.Printf(format, args...)
}

func (s *stderrLogger) Errorf(_ context.Context, format string, args ...interface{}) {
	stdlog.Printf(format, args...)
}

func (s *stderrLogger) Criticalf(_ context.Context, format string, args ...interface{}) {
	stdlog.Printf(format, args...)
}

func newTransport(ctx context.Context) http.RoundTripper {
	dline, ok := ctx.Deadline()
	t := &http.Transport{}
	if ok {
		t.ResponseHeaderTimeout = time.Until(dline)
	}
	return t
}

func currentUserEmail(ctx context.Context) string {
	// TODO
	return ""
}

// loginURL returns a URL that, when visited, prompts the user to sign in,
// then redirects the user to the URL specified by dest.
func loginURL(ctx context.Context, path string) (string, error) {
	// TODO implement
	return "/login", nil
}

func logoutURL(ctx context.Context, path string) (string, error) {
	// TODO implement
	return "/logout", nil
}

func getCaches(ctx context.Context, names ...string) map[string]*Cache {
	out := make(map[string]*Cache)
	dstoreMu.Lock()
	defer dstoreMu.Unlock()
	for _, name := range names {
		if val, ok := dstore[name]; ok {
			out[name] = val
		} else {
			// Ignore errors since they might not exist.
			out[name] = &Cache{}
		}
	}
	return out
}

var dstore = make(map[string]*Cache)
var dstoreMu sync.Mutex

var pageStore = make(map[string]*Page)
var pageStoreMu sync.Mutex

func getCache(_ context.Context, name string) (*Cache, error) {
	dstoreMu.Lock()
	defer dstoreMu.Unlock()
	cache, ok := dstore[name]
	if ok {
		return cache, nil
	}
	return &Cache{}, fmt.Errorf("cache key %s not found", name)
}

func getPage(ctx context.Context, name string) (*Page, error) {
	pageStoreMu.Lock()
	defer pageStoreMu.Unlock()
	page, ok := pageStore[name]
	if ok {
		return page, nil
	}
	return &Page{}, fmt.Errorf("page key %s not found", name)
}

func writePage(ctx context.Context, page string, content []byte) error {
	pageStoreMu.Lock()
	defer pageStoreMu.Unlock()
	entity := &Page{
		Content: content,
	}
	pageStore[page] = entity
	return nil
}

func putCache(_ context.Context, name string, c *Cache) error {
	dstoreMu.Lock()
	defer dstoreMu.Unlock()
	dstore[name] = c
	return nil
}

var githubToken string
var githubOnceErr error
var githubOnce sync.Once

func getToken(ctx context.Context) (string, error) {
	githubOnce.Do(func() {
		const short = ".github-issue-token"
		filename := filepath.Clean(os.Getenv("HOME") + "/" + short)
		shortFilename := filepath.Clean("$HOME/" + short)
		if *tokenFile != "" {
			filename = *tokenFile
			shortFilename = *tokenFile
		}
		data, err := ioutil.ReadFile(filename)
		if err != nil {
			msg := fmt.Sprintln("reading token: ", err, "\n\n"+
				"Please create a personal access token at https://github.com/settings/tokens/new\n"+
				"and write it to ", shortFilename, " to use this program.\n"+
				"The token only needs the repo scope, or private_repo if you want to\n"+
				"view or edit issues for private repositories.\n"+
				"The benefit of using a personal access token over using your GitHub\n"+
				"password directly is that you can limit its use and revoke it at any time.\n\n")
			githubOnceErr = errors.New(msg)
			return
		}
		fi, err := os.Stat(filename)
		if fi.Mode()&0077 != 0 {
			githubOnceErr = fmt.Errorf("reading token: %s mode is %#o, want %#o", shortFilename, fi.Mode()&0777, fi.Mode()&0700)
			return
		}
		githubToken = strings.TrimSpace(string(data))
	})
	return githubToken, githubOnceErr
}

func getContext(r *http.Request) context.Context {
	return r.Context()
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "./static/favicon.ico")
}
