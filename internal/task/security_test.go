// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"strings"
	"testing"

	"golang.org/x/build/relmeta"
)

const clBase = "https://go-internal-review.git.corp.google.com/c/"

func manualPatch() *relmeta.SecurityPatch {
	return &relmeta.SecurityPatch{
		ID:          1,
		Changelists: []string{clBase + "go/+/1", clBase + "go/+/2", clBase + "net/+/3"},
		DeploymentMap: map[string]string{
			clBase + "go/+/1":  "go:public",
			clBase + "go/+/2":  "go:internal-release-branch.go1.25.1",
			clBase + "net/+/3": "net:public",
		},
	}
}

func TestCheckDeploymentMap(t *testing.T) {
	branches := []string{"public", "internal-release-branch.go1.25.1"}
	for _, tc := range []struct {
		name    string
		patch   func() *relmeta.SecurityPatch
		project string
		wantErr string
	}{
		{"non_manual", func() *relmeta.SecurityPatch { return &relmeta.SecurityPatch{Changelists: []string{clBase + "go/+/1"}} }, "go", ""},
		{"congruent_go", manualPatch, "go", ""},
		{"congruent_net", manualPatch, "net", ""},
		{"missing_entry", func() *relmeta.SecurityPatch {
			p := manualPatch()
			delete(p.DeploymentMap, clBase+"go/+/2")
			return p
		}, "go", "missing from the deployment map"},
		{"unknown_branch", func() *relmeta.SecurityPatch {
			p := manualPatch()
			p.DeploymentMap[clBase+"go/+/2"] = "go:internal-release-branch.go1.24.9"
			return p
		}, "go", "want one of"},
		{"malformed_deployment", func() *relmeta.SecurityPatch {
			p := manualPatch()
			p.DeploymentMap[clBase+"go/+/2"] = "public"
			return p
		}, "go", "want <project>:<branch>"},
		{"foreign_project_branch_ignored", func() *relmeta.SecurityPatch {
			p := manualPatch()
			p.DeploymentMap[clBase+"go/+/2"] = "go:internal-release-branch.go1.24.9"
			return p
		}, "net", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckDeploymentMap(tc.patch(), tc.project, branches)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckDeploymentMap = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CheckDeploymentMap = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestDeployedChangelists(t *testing.T) {
	p := manualPatch()
	for _, tc := range []struct {
		project, branch string
		want            []string
	}{
		{"go", "public", []string{clBase + "go/+/1"}},
		{"go", "internal-release-branch.go1.25.1", []string{clBase + "go/+/2"}},
		{"net", "public", []string{clBase + "net/+/3"}},
		{"net", "internal-release-branch.go1.25.1", nil},
	} {
		got := DeployedChangelists(p, tc.project, tc.branch)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("DeployedChangelists(%s, %s) = %v, want %v", tc.project, tc.branch, got, tc.want)
		}
	}
	p.DeploymentMap = nil
	if got := DeployedChangelists(p, "net", "anything"); len(got) != len(p.Changelists) {
		t.Errorf("non-manual DeployedChangelists = %v, want all changelists", got)
	}
}
