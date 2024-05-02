// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

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
		Expires: start,
	}
	s.renew()
	if !s.Expires.After(start) {
		t.Errorf("Session.expires = %s; want a time > %s", s.Expires, start)
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
				Expires: tc.expires,
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
		sp.AddSession("accounts.google.com:user-xyz-124", "test-user", "builder-type-x", "host-type-x", &buildlet.FakeClient{})
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
		sp.AddSession("accounts.google.com:user-xyz-124", fmt.Sprintf("user-%d", i), "builder", "host", &buildlet.FakeClient{})
	}
	got := sp.List()
	if len(got) != wantCount {
		t.Errorf("SessionPool.List() = %v; want %d sessions", got, wantCount)
	}
	for it, s := range got[:len(got)-1] {
		if s.ID > got[it+1].ID {
			t.Fatalf("SessionPool.List(): SessionInstance[%d].ID=%s > SessionInstance[%d].ID=%s; want sorted by name",
				it, s.ID, it+1, got[it+1].ID)
		}
	}
}

func TestSessionPoolDestroySession(t *testing.T) {
	sp := NewSessionPool(context.Background())
	defer sp.Close()

	var sn []string
	for i := 0; i < 4; i++ {
		name := sp.AddSession("accounts.google.com:user-xyz-124", fmt.Sprintf("user-%d", i), "builder", "host", &buildlet.FakeClient{})
		sn = append(sn, name)
	}
	for _, name := range sn {
		if err := sp.DestroySession(name); err != nil {
			t.Errorf("SessionPool.DestroySession(%q) = %s; want no error", name, err)
		}
	}
}

func TestRenewTimeout(t *testing.T) {
	sp := NewSessionPool(context.Background())
	defer sp.Close()

	name := sp.AddSession("accounts.google.com:user-xyz-124", "user-x", "builder", "host", &buildlet.FakeClient{})
	if err := sp.RenewTimeout(name); err != nil {
		t.Errorf("SessionPool.RenewTimeout(%q) = %s; want no error", name, err)
	}
}

func TestRenewTimeoutError(t *testing.T) {
	sp := NewSessionPool(context.Background())
	defer sp.Close()

	name := sp.AddSession("accounts.google.com:user-xyz-124", "user-x", "builder", "host", &buildlet.FakeClient{})
	if err := sp.RenewTimeout(name + "-wrong"); err == nil {
		t.Errorf("SessionPool.RenewTimeout(%q) = %s; want error", name, err)
	}
}
