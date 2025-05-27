// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
)

func TestPromoteNextAPIAndOpenAPIAuditIssue(t *testing.T) {
	goRepo := task.NewFakeRepo(t, "go")
	goRepo.Commit(map[string]string{
		"api/next/54386.txt": "pkg bytes, func ContainsFunc([]uint8, func(int32) bool) bool #54386\n",
		"api/next/53685.txt": "pkg bytes, method (*Buffer) AvailableBuffer() []uint8 #53685\npkg bytes, method (*Buffer) Available() int #53685\n",
		"api/next/59488.txt": "pkg cmp, func Compare[$0 Ordered]($0, $0) int #59488\npkg cmp, func Less[$0 Ordered]($0, $0) bool #59488\npkg cmp, type Ordered interface {} #59488\n",
		"api/next/50489.txt": "pkg math/big, method (*Rat) FloatPrec() (int, bool) #50489\n",
	})
	fakeGerrit := task.NewFakeGerrit(t, goRepo)
	fakeGitHub := &task.FakeGitHub{
		Milestones: map[int]string{322: "Go1.24"}, // https://github.com/golang/go/milestone/322
	}

	const version = 24
	cycleTasks := task.ReleaseCycleTasks{
		Gerrit: fakeGerrit,
		GitHub: fakeGitHub,
	}
	promotedAPI, err := cycleTasks.PromoteNextAPI(
		&workflow.TaskContext{Context: context.Background(), Logger: testLogger{t: t}},
		version,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	apiAuditIssue, err := cycleTasks.OpenAPIAuditIssue(
		&workflow.TaskContext{Context: context.Background(), Logger: testLogger{t: t}},
		version,
		task.RelnoteTracking{Milestone: 322},
		promotedAPI,
	)
	if err != nil {
		t.Fatal(err)
	}

	const wantIssueTitle = "api: audit for Go 1.24"
	const wantIssueBody = `This is a tracking issue for doing an audit of API additions for Go 1.24 as of [CL 1](https://go.dev/cl/1).

## New API changes for Go 1.24

### bytes

- ` + "`" + `func ContainsFunc([]uint8, func(int32) bool) bool` + "`" + ` #54386
- ` + "`" + `method (*Buffer) Available() int` + "`" + ` #53685
- ` + "`" + `method (*Buffer) AvailableBuffer() []uint8` + "`" + ` #53685

### cmp

- ` + "`" + `func Compare[$0 Ordered]($0, $0) int` + "`" + ` #59488
- ` + "`" + `func Less[$0 Ordered]($0, $0) bool` + "`" + ` #59488
- ` + "`" + `type Ordered interface {}` + "`" + ` #59488

### math/big

- ` + "`" + `method (*Rat) FloatPrec() (int, bool)` + "`" + ` #50489

CC @aclements, @ianlancetaylor, @golang/release.`
	if len(fakeGitHub.Issues) != 1 {
		t.Fatalf("created %d issues, want 1", len(fakeGitHub.Issues))
	}
	if got, want := fakeGitHub.Issues[apiAuditIssue].GetTitle(), wantIssueTitle; got != want {
		t.Errorf("issue title mismatch: got %s, want %s", got, want)
	}
	if got, want := fakeGitHub.Issues[apiAuditIssue].GetBody(), wantIssueBody; got != want {
		t.Errorf("issue body mismatch: got %s, want %s", got, want)
	}
}

type testLogger struct {
	t    testing.TB
	task string // Optional.
}

func (l testLogger) Printf(format string, v ...interface{}) {
	l.t.Logf("%v\ttask %-10v: LOG: %s", time.Now(), l.task, fmt.Sprintf(format, v...))
}
