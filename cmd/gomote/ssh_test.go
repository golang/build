// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"
)

func TestScpArgs(t *testing.T) {
	tests := []struct {
		args          []string
		wantInstances []string
		wantRewritten []string
	}{
		{
			[]string{"-r", "user-linux-amd64-0:foo", "."},
			[]string{"user-linux-amd64-0"},
			[]string{"-r", "user-linux-amd64-0@" + sshServer + ":foo", "."},
		},
		{
			[]string{"local", "user-linux-amd64-0:", "user-linux-amd64-0:x/y"},
			[]string{"user-linux-amd64-0"},
			[]string{"local", "user-linux-amd64-0@" + sshServer + ":", "user-linux-amd64-0@" + sshServer + ":x/y"},
		},
		{
			[]string{"a-1:x", "b-2:y"},
			[]string{"a-1", "b-2"},
			[]string{"a-1@" + sshServer + ":x", "b-2@" + sshServer + ":y"},
		},
		{
			// Local paths, flags, and already-qualified names pass through.
			[]string{"-P", "2222", "./a:b", "/c:d", "u@h:x", ":x", "plain"},
			nil,
			[]string{"-P", "2222", "./a:b", "/c:d", "u@h:x", ":x", "plain"},
		},
	}
	for _, tt := range tests {
		instances, rewritten := scpArgs(tt.args)
		if !reflect.DeepEqual(instances, tt.wantInstances) || !reflect.DeepEqual(rewritten, tt.wantRewritten) {
			t.Errorf("scpArgs(%q) = %q, %q, want %q, %q", tt.args, instances, rewritten, tt.wantInstances, tt.wantRewritten)
		}
	}
}

func TestShellJoin(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"ssh", "-p", "2222", "inst@host"}, "ssh -p 2222 inst@host"},
		{[]string{"-i", "/a/Application Support/key"}, "-i '/a/Application Support/key'"},
		{[]string{"echo", "don't"}, `echo 'don'\''t'`},
		{[]string{""}, "''"},
	}
	for _, tt := range tests {
		if got := shellJoin(tt.args); got != tt.want {
			t.Errorf("shellJoin(%q) = %s, want %s", tt.args, got, tt.want)
		}
	}
}
