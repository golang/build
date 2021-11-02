// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package remote

import (
	"context"
	"fmt"
	"testing"
	"time"

	"golang.org/x/build/buildlet"
)

func TestSessionRenew(t *testing.T) {
	start := time.Now()
	s := Session{
		expires: start,
	}
	s.renew(context.Background())
	if !s.expires.After(start) {
		t.Errorf("Session.expires = %s; want a time > %s", s.expires, start)
	}
}

func TestSessionIsExpired(t *testing.T) {
	testCases := []struct {
		desc    string
		expires time.Time
		want    bool
	}{
		{"expire is zero value", time.Time{}, false},
		{"expired", time.Now().Add(-time.Minute), true},
		{"not expired", time.Now().Add(time.Minute), false},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			s := &Session{
				expires: tc.expires,
			}
			if got := s.isExpired(); got != tc.want {
				t.Errorf("Session.isExpired() = %t; want %t", got, tc.want)
			}
		})
	}
}

func TestSessionPool(t *testing.T) {
	sp := NewSessionPool(context.Background())
	defer sp.Close()

	wantInstances := 4
	for i := 0; i < wantInstances; i++ {
		sp.AddSession("test-user", "builder-type-x", "host-type-x", &buildlet.FakeClient{})
	}
	sp.destroyExpiredSessions(context.Background())
	if sp.Len() != wantInstances {
		t.Errorf("SessionPool.Len() = %d; want %d", sp.Len(), wantInstances)
	}
}

func TestSessionPoolList(t *testing.T) {
	sp := NewSessionPool(context.Background())
	defer sp.Close()

	wantCount := 4
	for i := 0; i < wantCount; i++ {
		sp.AddSession(fmt.Sprintf("user-%d", i), "builder", "host", &buildlet.FakeClient{})
	}
	got := sp.List()
	if len(got) != wantCount {
		t.Errorf("SessionPool.List() = %v; want %d sessions", got, wantCount)
	}
	for it, s := range got[:len(got)-1] {
		if s.name > got[it+1].name {
			t.Fatalf("SessionPool.List(): Session[%d].name=%s > Session[%d].name=%s; want sorted by name",
				it, s.name, it+1, got[it+1].name)
		}
	}
}

func TestSessionPoolDestroySession(t *testing.T) {
	sp := NewSessionPool(context.Background())
	defer sp.Close()

	var sn []string
	for i := 0; i < 4; i++ {
		name := sp.AddSession(fmt.Sprintf("user-%d", i), "builder", "host", &buildlet.FakeClient{})
		sn = append(sn, name)
	}
	for _, name := range sn {
		if err := sp.DestroySession(name); err != nil {
			t.Errorf("SessionPool.DestroySession(%q) = %s; want no error", name, err)
		}
	}
}

func TestSessionPoolUserFromGomoteInstanceName(t *testing.T) {
	testCases := []struct {
		desc         string
		buildletName string
		user         string
	}{
		{"mutable", "user-bradfitz-linux-amd64-0", "bradfitz"},
		{"non-mutable", "mutable-user-bradfitz-darwin-amd64-10_8-0", "bradfitz"},
		{"invalid", "yipeee", ""},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := userFromGomoteInstanceName(tc.buildletName); got != tc.user {
				t.Errorf("userFromGomoteInstanceName(tc.buildletName) = %q; want %q", got, tc.user)
			}
		})
	}
}
