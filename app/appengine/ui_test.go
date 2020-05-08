// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/types"
)

func TestUITemplateDataBuilder(t *testing.T) {
	// Thin the list of builders to make this test's data lighter
	// and require less maintenance keeping it in sync.
	origBuilders := dashboard.Builders
	defer func() { dashboard.Builders = origBuilders }()
	dashboard.Builders = map[string]*dashboard.BuildConfig{
		"linux-amd64": origBuilders["linux-amd64"],
		"linux-386":   origBuilders["linux-386"],
	}

	tests := []struct {
		name           string                   // test subname
		view           dashboardView            // one of htmlView{}, jsonView{}, or failuresView{}
		req            *apipb.DashboardRequest  // what we pretend we sent to maintner
		res            *apipb.DashboardResponse // what we pretend we got back from maintner
		testCommitData map[string]*Commit       // what we pretend we loaded from datastore
		activeBuilds   []types.ActivePostSubmitBuild
		want           *uiTemplateData // what we're hoping we generated for the view/template
	}{
		// Basic test.
		{
			name: "html,zero_value_req,no_commits",
			view: htmlView{},
			req:  &apipb.DashboardRequest{},
			res: &apipb.DashboardResponse{
				Branches: []string{"release.foo", "release.bar", "dev.blah"},
			},
			want: &uiTemplateData{
				Dashboard:  goDash,
				Repo:       "go",
				Package:    &Package{Name: "Go", Path: ""},
				Branches:   []string{"release.foo", "release.bar", "dev.blah"},
				Builders:   []string{"linux-386", "linux-amd64"},
				Pagination: &Pagination{},
			},
		},

		// Basic test + two commits: one that's in datastore and one that's not.
		{
			name: "html,zero_value_req,has_commit",
			view: htmlView{},
			req:  &apipb.DashboardRequest{},
			// Have only one commit load from the datastore:
			testCommitData: map[string]*Commit{
				"26957168c4c0cdcc7ca4f0b19d0eb19474d224ac": {
					PackagePath: "",
					Hash:        "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
					ResultData: []string{
						"openbsd-amd64|true||", // pretend openbsd-amd64 passed (and thus exists)
					},
				},
			},
			activeBuilds: []types.ActivePostSubmitBuild{
				{Builder: "linux-amd64", Commit: "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac", StatusURL: "http://fake-status"},
			},
			res: &apipb.DashboardResponse{
				Branches: []string{"release.foo", "release.bar", "dev.blah"},
				Commits: []*apipb.DashCommit{
					// This is the maintner commit response that is in the datastore:
					{
						Commit:        "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						AuthorName:    "Foo Bar",
						AuthorEmail:   "foo@example.com",
						CommitTimeSec: 1257894001,
						Title:         "runtime: fix all the bugs",
						Branch:        "master",
					},
					// And another commit that's not in the datastore:
					{
						Commit:        "ffffffffffffffffffffffffffffffffffffffff",
						AuthorName:    "Fancy Fred",
						AuthorEmail:   "f@eff.tld",
						CommitTimeSec: 1257894000,
						Title:         "all: add effs",
						Branch:        "master",
					},
				},
				CommitsTruncated: true, // pretend there's a page 2
			},
			want: &uiTemplateData{
				Dashboard: goDash,
				Repo:      "go",
				Package:   &Package{Name: "Go", Path: ""},
				Branches:  []string{"release.foo", "release.bar", "dev.blah"},
				Builders:  []string{"linux-386", "linux-amd64", "openbsd-amd64"},
				Pagination: &Pagination{
					Next: 1,
				},
				Commits: []*CommitInfo{
					{
						Hash: "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						User: "Foo Bar <foo@example.com>",
						Desc: "runtime: fix all the bugs",
						Time: time.Unix(1257894001, 0),
						ResultData: []string{
							"openbsd-amd64|true||",
						},
						Branch:       "master",
						BuildingURLs: map[builderAndGoHash]string{{builder: "linux-amd64"}: "http://fake-status"},
					},
					{
						Hash:   "ffffffffffffffffffffffffffffffffffffffff",
						User:   "Fancy Fred <f@eff.tld>",
						Desc:   "all: add effs",
						Time:   time.Unix(1257894000, 0),
						Branch: "master",
					},
				},
			},
		},

		// Test that we generate the TagState (sections at
		// bottom with the x/foo repo state).
		{
			name:           "html,zero_value_req,has_xrepos",
			view:           htmlView{},
			req:            &apipb.DashboardRequest{},
			testCommitData: map[string]*Commit{},
			res: &apipb.DashboardResponse{
				Branches: []string{"release.foo", "release.bar", "dev.blah"},
				Commits: []*apipb.DashCommit{
					{
						Commit:        "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						AuthorName:    "Foo Bar",
						AuthorEmail:   "foo@example.com",
						CommitTimeSec: 1257894001,
						Title:         "runtime: fix all the bugs",
						Branch:        "master",
					},
				},
				Releases: []*apipb.GoRelease{
					{
						BranchName:   "master",
						BranchCommit: "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
					},
					{
						BranchName:   "release-branch.go1.99",
						BranchCommit: "ffffffffffffffffffffffffffffffffffffffff",
					},
				},
				RepoHeads: []*apipb.DashRepoHead{
					{
						GerritProject: "go",
						Commit: &apipb.DashCommit{
							Commit:        "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
							AuthorName:    "Foo Bar",
							AuthorEmail:   "foo@example.com",
							CommitTimeSec: 1257894001,
							Title:         "runtime: fix all the bugs",
							Branch:        "master",
						},
					},
					{
						GerritProject: "net",
						Commit: &apipb.DashCommit{
							Commit:        "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
							AuthorName:    "Ee Yore",
							AuthorEmail:   "e@e.net",
							CommitTimeSec: 1257894001,
							Title:         "all: fix networking",
							Branch:        "master",
						},
					},
					{
						GerritProject: "sys",
						Commit: &apipb.DashCommit{
							Commit:        "dddddddddddddddddddddddddddddddddddddddd",
							AuthorName:    "Sys Tem",
							AuthorEmail:   "sys@s.net",
							CommitTimeSec: 1257894001,
							Title:         "sys: support more systems",
							Branch:        "master",
						},
					},
				},
			},
			want: &uiTemplateData{
				Dashboard:  goDash,
				Repo:       "go",
				Package:    &Package{Name: "Go", Path: ""},
				Branches:   []string{"release.foo", "release.bar", "dev.blah"},
				Builders:   []string{"linux-386", "linux-amd64"},
				Pagination: &Pagination{},
				Commits: []*CommitInfo{
					{
						Hash:   "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						User:   "Foo Bar <foo@example.com>",
						Desc:   "runtime: fix all the bugs",
						Time:   time.Unix(1257894001, 0),
						Branch: "master",
					},
				},
				TagState: []*TagState{
					{
						Name:     "master",
						Tag:      &CommitInfo{Hash: "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac"},
						Builders: []string{"linux-386", "linux-amd64"},
						Packages: []*PackageState{
							{
								Package: &Package{Name: "net", Path: "golang.org/x/net"},
								Commit: &CommitInfo{
									Hash:        "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
									PackagePath: "golang.org/x/net",
									User:        "Ee Yore <e@e.net>",
									Desc:        "all: fix networking",
									Time:        time.Unix(1257894001, 0),
									Branch:      "master",
								},
							},
							{
								Package: &Package{Name: "sys", Path: "golang.org/x/sys"},
								Commit: &CommitInfo{
									Hash:        "dddddddddddddddddddddddddddddddddddddddd",
									PackagePath: "golang.org/x/sys",
									User:        "Sys Tem <sys@s.net>",
									Desc:        "sys: support more systems",
									Time:        time.Unix(1257894001, 0),
									Branch:      "master",
								},
							},
						},
					},
					{
						Name:     "release-branch.go1.99",
						Tag:      &CommitInfo{Hash: "ffffffffffffffffffffffffffffffffffffffff"},
						Builders: []string{"linux-386", "linux-amd64"},
						Packages: []*PackageState{
							{
								Package: &Package{Name: "net", Path: "golang.org/x/net"},
								Commit: &CommitInfo{
									Hash:        "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
									PackagePath: "golang.org/x/net",
									User:        "Ee Yore <e@e.net>",
									Desc:        "all: fix networking",
									Time:        time.Unix(1257894001, 0),
									Branch:      "master",
								},
							},
							{
								Package: &Package{Name: "sys", Path: "golang.org/x/sys"},
								Commit: &CommitInfo{
									Hash:        "dddddddddddddddddddddddddddddddddddddddd",
									PackagePath: "golang.org/x/sys",
									User:        "Sys Tem <sys@s.net>",
									Desc:        "sys: support more systems",
									Time:        time.Unix(1257894001, 0),
									Branch:      "master",
								},
							},
						},
					},
				},
			},
		},

		// Test viewing a non-go repo.
		{
			name:           "html,other_repo",
			view:           htmlView{},
			req:            &apipb.DashboardRequest{Repo: "golang.org/x/net"},
			testCommitData: map[string]*Commit{},
			res: &apipb.DashboardResponse{
				Branches: []string{"master", "dev.blah"},
				Commits: []*apipb.DashCommit{
					{
						Commit:         "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						AuthorName:     "Foo Bar",
						AuthorEmail:    "foo@example.com",
						CommitTimeSec:  1257894001,
						Title:          "net: fix all the bugs",
						Branch:         "master",
						GoCommitAtTime: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						GoCommitLatest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					},
				},
			},
			want: &uiTemplateData{
				Dashboard:  goDash,
				Repo:       "net",
				Package:    &Package{Name: "net", Path: "golang.org/x/net"},
				Branches:   []string{"master", "dev.blah"},
				Builders:   []string{"linux-386", "linux-amd64"},
				Pagination: &Pagination{},
				Commits: []*CommitInfo{
					{
						PackagePath: "golang.org/x/net",
						Hash:        "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						User:        "Foo Bar <foo@example.com>",
						Desc:        "net: fix all the bugs",
						Time:        time.Unix(1257894001, 0),
						Branch:      "master",
						ResultData: []string{
							"|false||aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
							"|false||bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb := &uiTemplateDataBuilder{
				view:           tt.view,
				req:            tt.req,
				res:            tt.res,
				activeBuilds:   tt.activeBuilds,
				testCommitData: tt.testCommitData,
			}
			data, err := tb.buildTemplateData(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			diff := cmp.Diff(tt.want, data, cmpopts.IgnoreUnexported(CommitInfo{}))
			if diff != "" {
				t.Errorf("mismatch want->got:\n%s", diff)
			}
		})
	}
}

func TestToBuildStatus(t *testing.T) {
	tests := []struct {
		name string
		data *uiTemplateData
		want types.BuildStatus
	}{
		{
			name: "go repo",
			data: &uiTemplateData{
				Dashboard:  goDash,
				Repo:       "go",
				Package:    &Package{Name: "Go", Path: ""},
				Branches:   []string{"release.foo", "release.bar", "dev.blah"},
				Builders:   []string{"linux-386", "linux-amd64"},
				Pagination: &Pagination{},
				Commits: []*CommitInfo{
					{
						Hash:   "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						User:   "Foo Bar <foo@example.com>",
						Desc:   "runtime: fix all the bugs",
						Time:   time.Unix(1257894001, 0).UTC(),
						Branch: "master",
					},
				},
				TagState: []*TagState{
					{
						Name:     "master",
						Tag:      &CommitInfo{Hash: "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac"},
						Builders: []string{"linux-386", "linux-amd64"},
						Packages: []*PackageState{
							{
								Package: &Package{Name: "net", Path: "golang.org/x/net"},
								Commit: &CommitInfo{
									Hash:        "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
									PackagePath: "golang.org/x/net",
									User:        "Ee Yore <e@e.net>",
									Desc:        "all: fix networking",
									Time:        time.Unix(1257894001, 0).UTC(),
									Branch:      "master",
								},
							},
							{
								Package: &Package{Name: "sys", Path: "golang.org/x/sys"},
								Commit: &CommitInfo{
									Hash:        "dddddddddddddddddddddddddddddddddddddddd",
									PackagePath: "golang.org/x/sys",
									User:        "Sys Tem <sys@s.net>",
									Desc:        "sys: support more systems",
									Time:        time.Unix(1257894001, 0).UTC(),
									Branch:      "master",
								},
							},
						},
					},
					{
						Name:     "release-branch.go1.99",
						Tag:      &CommitInfo{Hash: "ffffffffffffffffffffffffffffffffffffffff"},
						Builders: []string{"linux-386", "linux-amd64"},
						Packages: []*PackageState{
							{
								Package: &Package{Name: "net", Path: "golang.org/x/net"},
								Commit: &CommitInfo{
									Hash:        "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
									PackagePath: "golang.org/x/net",
									User:        "Ee Yore <e@e.net>",
									Desc:        "all: fix networking",
									Time:        time.Unix(1257894001, 0).UTC(),
									Branch:      "master",
								},
							},
							{
								Package: &Package{Name: "sys", Path: "golang.org/x/sys"},
								Commit: &CommitInfo{
									Hash:        "dddddddddddddddddddddddddddddddddddddddd",
									PackagePath: "golang.org/x/sys",
									User:        "Sys Tem <sys@s.net>",
									Desc:        "sys: support more systems",
									Time:        time.Unix(1257894001, 0).UTC(),
									Branch:      "master",
								},
							},
						},
					},
				},
			},
			want: types.BuildStatus{
				Builders: []string{"linux-386", "linux-amd64"},
				Revisions: []types.BuildRevision{
					{
						Repo:     "go",
						Revision: "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						Date:     "2009-11-10T23:00:01Z",
						Branch:   "master",
						Author:   "Foo Bar <foo@example.com>",
						Desc:     "runtime: fix all the bugs",
						Results:  []string{"", ""},
					},
					{
						Repo:       "net",
						Revision:   "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
						GoRevision: "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						Date:       "2009-11-10T23:00:01Z",
						Branch:     "master",
						GoBranch:   "master",
						Author:     "Ee Yore <e@e.net>",
						Desc:       "all: fix networking",
						Results:    []string{"", ""},
					},
					{
						Repo:       "sys",
						Revision:   "dddddddddddddddddddddddddddddddddddddddd",
						GoRevision: "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						Date:       "2009-11-10T23:00:01Z",
						Branch:     "master",
						GoBranch:   "master",
						Author:     "Sys Tem <sys@s.net>",
						Desc:       "sys: support more systems",
						Results:    []string{"", ""},
					},
					{
						Repo:       "net",
						Revision:   "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
						GoRevision: "ffffffffffffffffffffffffffffffffffffffff",
						Date:       "2009-11-10T23:00:01Z",
						Branch:     "master",
						GoBranch:   "release-branch.go1.99",
						Author:     "Ee Yore <e@e.net>",
						Desc:       "all: fix networking",
						Results:    []string{"", ""},
					},
					{
						Repo:       "sys",
						Revision:   "dddddddddddddddddddddddddddddddddddddddd",
						GoRevision: "ffffffffffffffffffffffffffffffffffffffff",
						Date:       "2009-11-10T23:00:01Z",
						Branch:     "master",
						GoBranch:   "release-branch.go1.99",
						Author:     "Sys Tem <sys@s.net>",
						Desc:       "sys: support more systems",
						Results:    []string{"", ""},
					},
				},
			},
		},
		{
			name: "other repo",
			data: &uiTemplateData{
				Dashboard: goDash,
				Repo:      "tools",
				Builders:  []string{"linux", "windows"},
				Commits: []*CommitInfo{
					{
						PackagePath: "golang.org/x/tools",
						Hash:        "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						User:        "Foo Bar <foo@example.com>",
						Desc:        "tools: fix all the bugs",
						Time:        time.Unix(1257894001, 0).UTC(),
						Branch:      "master",
						ResultData: []string{
							"linux|false|123|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
							"windows|false|456|bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
						},
					},
				},
			},
			want: types.BuildStatus{
				Builders: []string{"linux", "windows"},
				Revisions: []types.BuildRevision{
					{
						Repo:       "tools",
						Revision:   "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						GoRevision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
						Date:       "2009-11-10T23:00:01Z",
						Branch:     "master",
						Author:     "Foo Bar <foo@example.com>",
						Desc:       "tools: fix all the bugs",
						Results:    []string{"", "https://build.golang.org/log/456"},
					},
					{
						Repo:       "tools",
						Revision:   "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
						GoRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						Date:       "2009-11-10T23:00:01Z",
						Branch:     "master",
						Author:     "Foo Bar <foo@example.com>",
						Desc:       "tools: fix all the bugs",
						Results:    []string{"https://build.golang.org/log/123", ""},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toBuildStatus("build.golang.org", tt.data)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("buildStatus(...) mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
