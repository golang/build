// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import "testing"

var go119Unsupported = false

var turndownMsg = `
Since Go 1.19 is definitely not longer supported by now,
vcs-test.golang.org is no longer needed for testing any release branch and should have been turned
down, and x/build/vcs-test/... should have been deleted.
(See https://go.dev/issue/61425.)`

func TestTurnDownVCSTest(t *testing.T) {
	if !go119Unsupported {
		return
	}

	if testing.Short() {
		t.Log(turndownMsg)
	} else {
		t.Fatal(turndownMsg)
	}
}
