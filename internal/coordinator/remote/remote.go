// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package remote

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/internal"
)

const (
	remoteBuildletIdleTimeout   = 30 * time.Minute
	remoteBuildletCleanInterval = time.Minute
)

// BuildletClient is used in order to enable tests. The interface should contain all the buildlet.Client
// functions used by the callers.
type BuildletClient interface {
	Close() error
	GCEInstanceName() string
	SetGCEInstanceName(v string)
	SetName(name string)
}

// Session stores the metadata for a remote buildlet session.
type Session struct {
	mu sync.Mutex

	builderType string // default builder config to use if not overwritten
	buildlet    BuildletClient
	created     time.Time
	expires     time.Time
	hostType    string
	name        string // dup of key
	user        string // "user-foo" build key
}

// KeepAlive will renew the remote buildlet session by extending the expiration value. It will
// periodically extend the value until the provided context has been cancelled.
func (s *Session) KeepAlive(ctx context.Context) {
	go internal.PeriodicallyDo(ctx, time.Minute, func(ctx context.Context, _ time.Time) {
		s.renew(ctx)
	})
}

// renew extends the expiration timestamp for a session.
func (s *Session) renew(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.expires = time.Now().Add(remoteBuildletIdleTimeout)
}

// isExpired determines if the remote buildlet session has expired.
func (s *Session) isExpired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// check that the expire timestamp has been set and that it has expired.
	return !s.expires.IsZero() && s.expires.Before(time.Now())
}

// Buildlet returns the buildlet client associated with the Session.
func (s *Session) Buildlet() BuildletClient {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buildlet
}

// Name returns the buildlet's name.
func (s *Session) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.name
}

// SessionPool contains active remote buildlet sessions.
type SessionPool struct {
	mu sync.RWMutex

	once       sync.Once
	pollWait   sync.WaitGroup
	cancelPoll context.CancelFunc
	m          map[string]*Session // keyed by buildletName
}

// NewSessionPool creates a session pool which stores and provides access to active remote buildlet sessions.
// Either cancelling the context or calling close on the session pool will terminate any polling functions.
func NewSessionPool(ctx context.Context) *SessionPool {
	ctx, cancel := context.WithCancel(ctx)
	sp := &SessionPool{
		cancelPoll: cancel,
		m:          map[string]*Session{},
	}
	sp.pollWait.Add(1)
	go func() {
		internal.PeriodicallyDo(ctx, remoteBuildletCleanInterval, func(ctx context.Context, _ time.Time) {
			log.Printf("remote: cleaning up expired remote buildlets")
			sp.destroyExpiredSessions(ctx)
		})
		sp.pollWait.Done()
	}()
	return sp
}

// AddSession adds the provided session to the session pool.
func (sp *SessionPool) AddSession(user, builderType, hostType string, bc BuildletClient) (name string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	for n := 0; ; n++ {
		name = fmt.Sprintf("%s-%s-%d", user, builderType, n)
		if _, ok := sp.m[name]; !ok {
			now := time.Now()
			sp.m[name] = &Session{
				builderType: builderType,
				buildlet:    bc,
				created:     now,
				expires:     now.Add(remoteBuildletIdleTimeout),
				hostType:    hostType,
				name:        name,
				user:        user,
			}
			return name
		}
	}
}

// IsGCESession checks if the session is a GCE instance.
func (sp *SessionPool) IsGCESession(instName string) bool {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	for _, s := range sp.m {
		if s.buildlet.GCEInstanceName() == instName {
			return true
		}
	}
	return false
}

// destroyExpiredSessions destroys all sessions which have expired.
func (sp *SessionPool) destroyExpiredSessions(ctx context.Context) {
	sp.mu.Lock()
	var ss []*Session
	for name, s := range sp.m {
		if s.isExpired() {
			ss = append(ss, s)
			delete(sp.m, name)
		}
	}
	sp.mu.Unlock()
	// the sessions are no longer in the map. They can be mutated.
	for _, s := range ss {
		if err := s.buildlet.Close(); err != nil {
			log.Printf("remote: unable to close buildlet connection %s", err)
		}
	}
}

// DestroySession destroys a session.
func (sp *SessionPool) DestroySession(buildletName string) error {
	sp.mu.Lock()
	s, ok := sp.m[buildletName]
	if ok {
		delete(sp.m, buildletName)
	}
	sp.mu.Unlock()
	if !ok {
		return fmt.Errorf("remote buildlet does not exist=%s", buildletName)
	}
	if err := s.buildlet.Close(); err != nil {
		log.Printf("remote: unable to close buildlet connection %s: %s", buildletName, err)
	}
	return nil
}

// Close cancels the polling performed by the session pool. It waits for polling to conclude
// before returning.
func (sp *SessionPool) Close() {
	sp.once.Do(func() {
		sp.cancelPoll()
		sp.pollWait.Wait()
	})
}

// List returns a list of all active sessions sorted by session name.
func (sp *SessionPool) List() []*Session {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	var ss []*Session
	for _, s := range sp.m {
		ss = append(ss, s)
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].name < ss[j].name })
	return ss
}

// Len gives a count of how many sessions are in the pool.
func (sp *SessionPool) Len() int {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	return len(sp.m)
}

// Session retrieves a session from the pool.
func (sp *SessionPool) Session(buildletName string) (*Session, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	if rb, ok := sp.m[buildletName]; ok {
		rb.expires = time.Now().Add(remoteBuildletIdleTimeout)
		return rb, nil
	}
	return nil, fmt.Errorf("remote buildlet does not exist=%s", buildletName)
}

// userFromGomoteInstanceName returns the username part of a gomote
// remote instance name.
//
// The instance name is of two forms. The normal form is:
//
//     user-bradfitz-linux-amd64-0
//
// The overloaded form to convey that the user accepts responsibility
// for changes to the underlying host is to prefix the same instance
// name with the string "mutable-", such as:
//
//     mutable-user-bradfitz-darwin-amd64-10_8-0
//
// The mutable part is ignored by this function.
func userFromGomoteInstanceName(name string) string {
	name = strings.TrimPrefix(name, "mutable-")
	if !strings.HasPrefix(name, "user-") {
		return ""
	}
	user := name[len("user-"):]
	hyphen := strings.IndexByte(user, '-')
	if hyphen == -1 {
		return ""
	}
	return user[:hyphen]
}
