// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"testing"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
)

func TestNoOpCL(t *testing.T) {
	if !*flagRunVersionTest {
		t.Skip("Not enabled by flags")
	}
	cl := gerrit.NewClient("https://go-review.googlesource.com", gerrit.GitCookiesAuth())
	gcl := &RealGerritClient{Client: cl}

	ctx := &wf.TaskContext{Context: context.Background()}
	changeID, err := gcl.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "scratch",
		Branch:  "master",
		Subject: "no-op CL test",
	}, nil, map[string]string{"NONEXISTANT_FILE": ""})
	if err != nil {
		t.Fatal(err)
	}
	if changeID != "" {
		t.Fatalf("creating no-op change resulted in a CL %v (%q), wanted none", ChangeLink(changeID), changeID)
	}
}
