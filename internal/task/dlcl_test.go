// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/internal/workflow"
)

func TestMailDLCL(t *testing.T) {
	year := fmt.Sprint(time.Now().UTC().Year())
	tests := [...]struct {
		name    string
		kind    ReleaseKind
		major   int
		version string
		wantLog string
	}{
		{
			name:    "minor",
			kind:    KindMinor,
			major:   17,
			version: "go1.17.1",
			wantLog: `file "go1.17.1/main.go" (command "golang.org/dl/go1.17.1"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.17.1 command runs the go command from Go 1.17.1.
//
// To install, run:
//
//	$ go install golang.org/dl/go1.17.1@latest
//	$ go1.17.1 download
//
// And then use the go1.17.1 command as if it were your normal go
// command.
//
// See the release notes at https://go.dev/doc/devel/release#go1.17.1.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("go1.17.1")
}` + "\n",
		},
		{
			name:    "beta",
			kind:    KindBeta,
			major:   17,
			version: "go1.17beta1",
			wantLog: `file "go1.17beta1/main.go" (command "golang.org/dl/go1.17beta1"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.17beta1 command runs the go command from Go 1.17beta1.
//
// To install, run:
//
//	$ go install golang.org/dl/go1.17beta1@latest
//	$ go1.17beta1 download
//
// And then use the go1.17beta1 command as if it were your normal go
// command.
//
// See the release notes at https://tip.golang.org/doc/go1.17.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("go1.17beta1")
}` + "\n",
		},
		{
			name:    "rc",
			kind:    KindRC,
			major:   17,
			version: "go1.17rc2",
			wantLog: `file "go1.17rc2/main.go" (command "golang.org/dl/go1.17rc2"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.17rc2 command runs the go command from Go 1.17rc2.
//
// To install, run:
//
//	$ go install golang.org/dl/go1.17rc2@latest
//	$ go1.17rc2 download
//
// And then use the go1.17rc2 command as if it were your normal go
// command.
//
// See the release notes at https://tip.golang.org/doc/go1.17.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("go1.17rc2")
}` + "\n",
		},
		{
			name:    "major",
			kind:    KindMajor,
			major:   21,
			version: "go1.21.0",
			wantLog: `file "go1.21.0/main.go" (command "golang.org/dl/go1.21.0"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.21.0 command runs the go command from Go 1.21.0.
//
// To install, run:
//
//	$ go install golang.org/dl/go1.21.0@latest
//	$ go1.21.0 download
//
// And then use the go1.21.0 command as if it were your normal go
// command.
//
// See the release notes at https://go.dev/doc/go1.21.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("go1.21.0")
}` + "\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Call the mail a dl CL task function in dry-run mode so it
			// doesn't actually try to mail a dl CL, but capture its log.
			var buf bytes.Buffer
			ctx := &workflow.TaskContext{Context: context.Background(), Logger: fmtWriter{&buf}}
			tasks := &VersionTasks{Gerrit: nil}
			changeID, err := tasks.MailDLCL(ctx, tc.major, tc.kind, tc.version, nil, true)
			if err != nil {
				t.Fatal("got a non-nil error:", err)
			}
			if got, want := changeID, "(dry-run)"; got != want {
				t.Errorf("unexpected changeID: got = %q, want %q", got, want)
			}
			if diff := cmp.Diff(tc.wantLog, buf.String()); diff != "" {
				t.Errorf("log mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
