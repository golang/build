// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package criadb provides a wrapper around the CrIA authorization database. The
// database is replicated from a GCP Cloud Storage bucket, and is updated every
// 30 + rand.Intn(10) seconds. This database is used to check whether a user is
// a member of a specified mdb (or other) group.
package criadb

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"go.chromium.org/luci/auth/identity"
	"go.chromium.org/luci/server/auth"
	"go.chromium.org/luci/server/auth/authdb"
	"go.chromium.org/luci/server/auth/authdb/dump"
	"go.chromium.org/luci/server/auth/authtest"
	"go.chromium.org/luci/server/caching"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type AuthDatabase struct {
	mu sync.RWMutex
	db authdb.DB

	serviceName string
	cache       *caching.ProcessCacheData
	authConfig  *auth.Config
}

func (db *AuthDatabase) updateDB() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ctx = caching.WithProcessCacheData(ctx, db.cache)
	ctx = auth.Initialize(ctx, db.authConfig)

	fetcher := dump.Fetcher{
		StorageDumpPath:    fmt.Sprintf("%s.appspot.com/auth-db", db.serviceName),
		AuthServiceURL:     fmt.Sprintf("https://%s.appspot.com", db.serviceName),
		AuthServiceAccount: fmt.Sprintf("%s@appspot.gserviceaccount.com", db.serviceName),
		OAuthScopes:        auth.CloudOAuthScopes,
	}
	db.mu.RLock()
	curSnap, _ := db.db.(*authdb.SnapshotDB)
	snap, err := fetcher.FetchAuthDB(ctx, curSnap)
	if err != nil {
		db.mu.RUnlock()
		return err
	}
	db.mu.RUnlock()
	db.mu.Lock()
	db.db = snap
	db.mu.Unlock()
	return nil
}

func NewDatabase(serviceName string) (*AuthDatabase, error) {
	creds, err := google.FindDefaultCredentials(context.Background(), auth.CloudOAuthScopes...)
	if err != nil {
		return nil, err
	}
	db := &AuthDatabase{
		serviceName: serviceName,
		cache:       caching.NewProcessCacheData(),
		authConfig: &auth.Config{
			AccessTokenProvider: func(ctx context.Context, scopes []string) (*oauth2.Token, error) {
				return creds.TokenSource.Token()
			},
			IDTokenProvider: func(ctx context.Context, audience string) (*oauth2.Token, error) {
				return creds.TokenSource.Token()
			},
			AnonymousTransport: func(context.Context) http.RoundTripper {
				return http.DefaultTransport
			},
		},
	}
	if err := db.updateDB(); err != nil {
		return nil, err
	}

	go func() {
		// TODO(roland): this will not fail gracefully, especially during shutdown.
		for {
			jitter := time.Duration(rand.Int63n(int64(10 * time.Second)))
			time.Sleep(30*time.Second + jitter)
			if err := db.updateDB(); err != nil {
				log.Printf("db.updateDB failed: %s", err)
			}
		}
	}()

	return db, nil
}

func (db *AuthDatabase) IsMemberOfAny(ctx context.Context, ident string, groups []string) (bool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if db.db == nil {
		return false, errors.New("authdb uninitialized")
	}

	return db.db.IsMember(ctx, identity.Identity(ident), groups)
}

func NewDevDatabase() *AuthDatabase {
	return &AuthDatabase{db: authdb.DevServerDB{}}
}

func NewTestDatabase(memberships [][2]string) *AuthDatabase {
	var datums []authtest.MockedDatum
	for _, membership := range memberships {
		datums = append(datums, authtest.MockMembership(identity.Identity(membership[0]), membership[1]))
	}
	return &AuthDatabase{db: authtest.NewFakeDB(datums...)}
}
