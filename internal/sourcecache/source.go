// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package sourcecache provides a cache of code found in Git repositories.
// It takes directly to the Gerrit instance at go.googlesource.com.
// If RegisterGitMirrorDial is called, it will first try to get code from gitmirror before falling back on Gerrit.
package sourcecache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"golang.org/x/build/internal/lru"
	"golang.org/x/build/internal/singleflight"
	"golang.org/x/build/internal/spanlog"
)

var processStartTime = time.Now()

var sourceGroup singleflight.Group

var sourceCache = lru.New(40) // repo-rev -> source

// source is the cache entry type for sourceCache.
type source struct {
	Tgz    []byte // Source tarball bytes.
	TooBig bool
}

// GetSourceTgz returns a Reader that provides a tgz of the requested source revision.
// repo is go.googlesource.com repo ("go", "net", and so on).
// rev is git revision.
//
// An error of type TooBigError is returned if the compressed tarball exceeds a size that
// on 2021-11-22 was deemed to be enough to meet expected legitimate future needs for a while.
// See golang.org/issue/46379.
func GetSourceTgz(sl spanlog.Logger, repo, rev string) (tgz io.Reader, err error) {
	sp := sl.CreateSpan("get_source", repo+"@"+rev)
	defer func() { sp.Done(err) }()

	key := fmt.Sprintf("%v-%v", repo, rev)
	v, err, _ := sourceGroup.Do(key, func() (interface{}, error) {
		if src, ok := sourceCache.Get(key); ok {
			return src, nil
		}

		if gitMirrorClient != nil {
			sp := sl.CreateSpan("get_source_from_gitmirror")
			src, err := getSourceTgzFromGitMirror(repo, rev)
			if err == nil {
				sourceCache.Add(key, src)
				sp.Done(nil)
				return src, nil
			}
			log.Printf("Error fetching source %s/%s from gitmirror (after %v uptime): %v",
				repo, rev, time.Since(processStartTime), err)
			sp.Done(errors.New("timeout"))
		}

		sp := sl.CreateSpan("get_source_from_gerrit", fmt.Sprintf("%v from gerrit", key))
		src, err := getSourceTgzFromGerrit(repo, rev)
		sp.Done(err)
		if err == nil {
			sourceCache.Add(key, src)
		}
		return src, err
	})
	if err != nil {
		return nil, err
	}
	if v.(source).TooBig {
		return nil, TooBigError{Repo: repo, Rev: rev, Limit: maxSize(repo)}
	}
	return bytes.NewReader(v.(source).Tgz), nil
}

// TooBigError is the error returned when the source revision is considered too big.
type TooBigError struct {
	Repo  string
	Rev   string
	Limit int64 // Max size in bytes.
}

func (e TooBigError) Error() string {
	return fmt.Sprintf("rejected because compressed tarball of repository go.googlesource.com/%s at revision %s exceeded a limit of %d MB; see golang.org/issue/46379",
		e.Repo, e.Rev, e.Limit/1024/1024)
}

var gitMirrorClient *http.Client

// RegisterGitMirrorDial registers a dial function which will be used to reach gitmirror.
// If used, this function must be called before GetSourceTgz.
func RegisterGitMirrorDial(dial func(context.Context) (net.Conn, error)) {
	gitMirrorClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			IdleConnTimeout: 30 * time.Second,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dial(ctx)
			},
		},
	}
}

var gerritHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

func getSourceTgzFromGerrit(repo, rev string) (source, error) {
	return getSourceTgzFromURL(gerritHTTPClient, "gerrit", repo, rev, "https://go.googlesource.com/"+repo+"/+archive/"+rev+".tar.gz")
}

func getSourceTgzFromGitMirror(repo, rev string) (src source, err error) {
	for i := 0; i < 2; i++ { // two tries; different pods maybe?
		if i > 0 {
			time.Sleep(1 * time.Second)
		}
		// The "gitmirror" hostname is unused:
		src, err = getSourceTgzFromURL(gitMirrorClient, "gitmirror", repo, rev, "http://gitmirror/"+repo+".tar.gz?rev="+rev)
		if err == nil {
			return src, nil
		}
		if tr, ok := http.DefaultTransport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	}
	return source{}, err
}

// getSourceTgzFromURL fetches a source tarball from url.
// If url serves more than maxSize bytes, it stops short.
func getSourceTgzFromURL(hc *http.Client, service, repo, rev, url string) (source, error) {
	res, err := hc.Get(url)
	if err != nil {
		return source{}, fmt.Errorf("fetching %s/%s from %s: %v", repo, rev, service, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		slurp, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return source{}, fmt.Errorf("fetching %s/%s from %s: %v; body: %s", repo, rev, service, res.Status, slurp)
	}
	// See golang.org/issue/11224 for a discussion on tree filtering.
	b, err := io.ReadAll(io.LimitReader(res.Body, maxSize(repo)+1))
	if int64(len(b)) > maxSize(repo) && err == nil {
		return source{TooBig: true}, nil
	}
	if err != nil {
		return source{}, fmt.Errorf("reading %s/%s from %s: %v", repo, rev, service, err)
	}
	return source{Tgz: b}, nil
}

// maxSize controls artificial limits on how big of a compressed source tarball
// this package is willing to accept. It's expected humans may need to manage
// these limits every couple of years for the evolving needs of the Go project,
// and ideally not much more often.
//
// repo is a go.googlesource.com repo ("go", "net", and so on).
func maxSize(repo string) int64 {
	switch repo {
	default:
		// As of 2021-11-22, a compressed tarball of Go source is 23 MB,
		// x/net is 1.2 MB,
		// x/build is 1.1 MB,
		// x/tools is 2.9 MB.
		return 100 << 20
	case "website":
		// In 2021, all content in x/blog (52 MB) and x/talks (74 MB) moved
		// to x/website. This makes x/website an outlier, with a compressed
		// tarball size of 135 MB. Give it some room to grow from there.
		return 200 << 20
	}
}
