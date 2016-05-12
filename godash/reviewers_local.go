// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package godash

import (
	"log"
	"os/exec"
	"strings"
)

// Adapted from git-codereview/mail.go, but uses Author lines
// in addition to Reviewed-By lines. The effect should be the same,
// since the most common reviewers are the most common authors too,
// but admitting authors lets us shorten CL owners too.

func (r *Reviewers) LoadLocal() {
	output, err := exec.Command("go", "env", "GOROOT").CombinedOutput()
	if err != nil {
		log.Fatalf("go env GOROOT: %v\n%s", err, output)
	}
	goroot := strings.TrimSpace(string(output))
	cmd := exec.Command("git", "log", "--format=format:Author: <%aE>%n%B")
	cmd.Dir = goroot
	output, err = cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("git log: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "Reviewed-by:") || strings.HasPrefix(line, "Author:") {
			f := strings.Fields(line)
			addr := f[len(f)-1]
			if strings.HasPrefix(addr, "<") && strings.Contains(addr, "@") && strings.HasSuffix(addr, ">") {
				email := addr[1 : len(addr)-1]
				r.add(email, strings.HasPrefix(line, "Reviewed-by:"))
			}
		}
	}
	r.recalculate()
}
