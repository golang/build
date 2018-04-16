// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReleaselet(t *testing.T) {
	cmd := exec.Command("go", "run", "releaselet.go")
	cmd.Env = append(os.Environ(), "RUN_RELEASELET_TESTS=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("error running releaselet.go tests: %v, %s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "ok" {
		t.Errorf("got output %q; want ok", out)
	}
}
