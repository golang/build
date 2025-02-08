// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package criadb

import (
	"context"
	"testing"

	"go.chromium.org/luci/server/auth/authtest"
)

func TestIsMemberOfAny(t *testing.T) {
	db := &AuthDatabase{}

	_, err := db.IsMemberOfAny(context.Background(), "test@golang.org", []string{"test-group"})
	if err == nil {
		t.Error("db.IsMemberOfAny returned nil error with uninitialized database")
	}

	db.db = authtest.NewFakeDB(authtest.MockMembership("user:test@golang.org", "test-group"))

	isMember, err := db.IsMemberOfAny(context.Background(), "user:test@golang.org", []string{"test-group", "random-group"})
	if err != nil {
		t.Errorf("db.IsMemberOfAny('user:test@golang.org', []string{'test-group', 'random-group'}) failed: %s", err)
	}
	if !isMember {
		t.Error("db.IsMemberOfAny('user:test@golang.org', []string{'test-group', 'random-group'}) returned false, expected true")
	}

	isMember, err = db.IsMemberOfAny(context.Background(), "user:nope@golang.org", []string{"test-group", "random-group"})
	if err != nil {
		t.Errorf("db.IsMemberOfAny('user:nope@golang.org', []string{'test-group', 'random-group'}) failed: %s", err)
	}
	if isMember {
		t.Error("db.IsMemberOfAny('user:nope@golang.org', []string{'test-group', 'random-group'}) returned true, expected false")
	}
}
