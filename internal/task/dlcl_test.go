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

	"golang.org/x/build/internal/workflow"
)

func TestMailDLCL(t *testing.T) {
	year := fmt.Sprint(time.Now().UTC().Year())
	tests := [...]struct {
		name    string
		in      []string
		wantLog string
	}{
		{
			name: "minor",
			in:   []string{"go1.17.1", "go1.16.8"},
			wantLog: `file "go1.17.1/main.go" (command "golang.org/dl/go1.17.1"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.17.1 command runs the go command from Go 1.17.1.
//
// To install, run:
//
//     $ go install golang.org/dl/go1.17.1@latest
//     $ go1.17.1 download
//
// And then use the go1.17.1 command as if it were your normal go
// command.
//
// See the release notes at https://go.dev/doc/devel/release#go1.17.minor.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("go1.17.1")
}
file "go1.16.8/main.go" (command "golang.org/dl/go1.16.8"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.16.8 command runs the go command from Go 1.16.8.
//
// To install, run:
//
//     $ go install golang.org/dl/go1.16.8@latest
//     $ go1.16.8 download
//
// And then use the go1.16.8 command as if it were your normal go
// command.
//
// See the release notes at https://go.dev/doc/devel/release#go1.16.minor.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("go1.16.8")
}` + "\n",
		},
		{
			name: "beta",
			in:   []string{"go1.17beta1"},
			wantLog: `file "go1.17beta1/main.go" (command "golang.org/dl/go1.17beta1"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.17beta1 command runs the go command from Go 1.17beta1.
//
// To install, run:
//
//     $ go install golang.org/dl/go1.17beta1@latest
//     $ go1.17beta1 download
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
			name: "rc",
			in:   []string{"go1.17rc2"},
			wantLog: `file "go1.17rc2/main.go" (command "golang.org/dl/go1.17rc2"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.17rc2 command runs the go command from Go 1.17rc2.
//
// To install, run:
//
//     $ go install golang.org/dl/go1.17rc2@latest
//     $ go1.17rc2 download
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
			name: "major",
			in:   []string{"go1.17"},
			wantLog: `file "go1.17/main.go" (command "golang.org/dl/go1.17"):
// Copyright ` + year + ` The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.17 command runs the go command from Go 1.17.
//
// To install, run:
//
//     $ go install golang.org/dl/go1.17@latest
//     $ go1.17 download
//
// And then use the go1.17 command as if it were your normal go
// command.
//
// See the release notes at https://go.dev/doc/go1.17.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("go1.17")
}` + "\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Call the mail a dl CL task function in dry-run mode so it
			// doesn't actually try to mail a dl CL, but capture its log.
			var buf bytes.Buffer
			ctx := &workflow.TaskContext{Context: context.Background(), Logger: fmtWriter{&buf}}
			changeURL, err := MailDLCL(ctx, tc.in, ExternalConfig{DryRun: true})
			if err != nil {
				t.Fatal("got a non-nil error:", err)
			}
			if got, want := changeURL, "(dry-run)"; got != want {
				t.Errorf("unexpected changeURL: got = %q, want %q", got, want)
			}
			if got, want := buf.String(), tc.wantLog; got != want {
				t.Errorf("unexpected log:\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
