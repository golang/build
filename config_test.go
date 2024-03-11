// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package luciconfig

import (
	"os/exec"
	"testing"
)

func TestValidate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode because we need network access on the builders to validate")
	}

	// N.B. Disable lint checks for now. We haven't had them enabled and we're failing quite a few of them.
	result, err := exec.Command("lucicfg", "validate", "-lint-checks", "none", "./main.star").CombinedOutput()
	t.Logf("validation output:\n%s", result)
	if err != nil {
		t.Fatal("failed to validate configuration, did you remember to run `./main.star`?")
	}
}

func TestFormatted(t *testing.T) {
	result, err := exec.Command("lucicfg", "fmt", "-dry-run", ".").CombinedOutput()
	t.Logf("formatter output:\n%s", result)
	if err != nil {
		t.Fatal("failed to run formatter, did you remember to run `lucicfg fmt`?")
	}
}
