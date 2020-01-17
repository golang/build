// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"io/ioutil"
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

func TestReleaseletIsUpToDate(t *testing.T) {
	want, err := ioutil.ReadFile("releaselet.go")
	if err != nil {
		t.Fatalf("error while reading releaselet.go: %v", err)
	}
	got := []byte(releaselet)
	if !bytes.Equal(got, want) {
		t.Error(`The releaselet constant in static.go is stale. To see the difference, run:
	$ go generate golang.org/x/build/cmd/release
	$ git diff`)
	}
}
