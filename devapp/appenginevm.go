// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file gets built on App Engine Flex.

// +build appenginevm

package devapp

import (
	"errors"
	"fmt"
	"net/http"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/datastore"
	"cloud.google.com/go/logging"
	"golang.org/x/net/context"
)

var lg *logging.Logger
var dsClient *datastore.Client

func init() {
	logger = appengineLogger{}
	id, err := metadata.ProjectID()
	if err != nil {
		id = "devapp"
	}
	ctx := context.Background()
	client, err := logging.NewClient(ctx, id)
	if err == nil {
		lg = client.Logger("log")
	}
	dsClient, _ = datastore.NewClient(ctx, id)

	http.Handle("/static/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/favicon.ico", faviconHandler)
	http.HandleFunc("/setToken", setTokenHandler)
}

type appengineLogger struct{}

func (a appengineLogger) Infof(_ context.Context, format string, args ...interface{}) {
	if lg == nil {
		return
	}
	lg.Log(logging.Entry{
		Severity: logging.Info,
		Payload:  fmt.Sprintf(format, args...),
	})
}

func (a appengineLogger) Errorf(_ context.Context, format string, args ...interface{}) {
	if lg == nil {
		return
	}
	lg.Log(logging.Entry{
		Severity: logging.Error,
		Payload:  fmt.Sprintf(format, args...),
	})
}

func (a appengineLogger) Criticalf(ctx context.Context, format string, args ...interface{}) {
	if lg == nil {
		return
	}
	lg.LogSync(ctx, logging.Entry{
		Severity: logging.Critical,
		Payload:  fmt.Sprintf(format, args...),
	})
}

func newTransport(ctx context.Context) http.RoundTripper {
	// This doesn't have a context, but we should be setting it on the request
	// when it comes through.
	return &http.Transport{}
}

func currentUserEmail(ctx context.Context) string {
	return ""
}

// loginURL returns a URL that, when visited, prompts the user to sign in,
// then redirects the user to the URL specified by dest.
func loginURL(ctx context.Context, path string) (string, error) {
	return "", errors.New("unimplemented")
}

func logoutURL(ctx context.Context, path string) (string, error) {
	return "", errors.New("unimplemented")
}

func getCache(ctx context.Context, name string) (*Cache, error) {
	cache := new(Cache)
	key := datastore.NameKey(entityPrefix+"Cache", name, nil)
	if err := dsClient.Get(ctx, key, cache); err != nil {
		return cache, err
	}
	return cache, nil
}

func getCaches(ctx context.Context, names ...string) map[string]*Cache {
	out := make(map[string]*Cache)
	var keys []*datastore.Key
	var ptrs []*Cache
	for _, name := range names {
		keys = append(keys, datastore.NameKey(entityPrefix+"Cache", name, nil))
		out[name] = new(Cache)
		ptrs = append(ptrs, out[name])
	}
	dsClient.GetMulti(ctx, keys, ptrs) // Ignore errors since they might not exist.
	return out
}

func getPage(ctx context.Context, page string) (*Page, error) {
	entity := new(Page)
	key := datastore.NameKey(entityPrefix+"Page", page, nil)
	err := dsClient.Get(ctx, key, entity)
	return entity, err
}

func writePage(ctx context.Context, page string, content []byte) error {
	entity := &Page{
		Content: content,
	}
	key := datastore.NameKey(entityPrefix+"Page", page, nil)
	_, err := dsClient.Put(ctx, key, entity)
	return err
}

func putCache(ctx context.Context, name string, c *Cache) error {
	key := datastore.NameKey(entityPrefix+"Cache", name, nil)
	_, err := dsClient.Put(ctx, key, c)
	return err
}

func getToken(ctx context.Context) (string, error) {
	cache, err := getCache(ctx, "github-token")
	if err != nil {
		return "", err
	}
	return string(cache.Value), nil
}

func getContext(r *http.Request) context.Context {
	return r.Context()
}
