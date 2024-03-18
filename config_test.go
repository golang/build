// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package luciconfig_test

import (
	"os/exec"
	"testing"
)

func TestValidate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode because network access is needed to validate")
	}
	if _, err := exec.LookPath("lucicfg"); err != nil {
		t.Fatalf("lucicfg is not available: %v\n\nSee the README for how to install it.", err)
	}

	// N.B. Disable lint checks for now. We haven't had them enabled and we're failing quite a few of them.
	result, err := exec.Command("lucicfg", "validate", "-lint-checks=none", "main.star").CombinedOutput()
	t.Logf("validation output:\n%s", result)
	if err != nil {
		t.Fatalf("failed to validate configuration: %v\n\nTry running `go generate` or see the README for more information.", err)
	}
}

func TestFormatted(t *testing.T) {
	if _, err := exec.LookPath("lucicfg"); err != nil {
		t.Fatalf("lucicfg is not available: %v\n\nSee the README for how to install it.", err)
	}

	result, err := exec.Command("lucicfg", "fmt", "-dry-run").CombinedOutput()
	t.Logf("formatter output:\n%s", result)
	if err != nil {
		t.Fatalf("failed to run formatter: %v\n\nTry running `lucicfg fmt` or see the README for more information.", err)
	}
}
