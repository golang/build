// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

// Package dashboard contains the implementation of the build dashboard for the Coordinator.
package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/datastore"
	"golang.org/x/build/cmd/coordinator/internal"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/maintner/maintnerd/apipb"
	grpc4 "grpc.go4.org"
)

var firstClassPorts = map[string]bool{
	"darwin-amd64":  true,
	"linux-386":     true,
	"linux-amd64":   true,
	"linux-arm":     true,
	"linux-arm64":   true,
	"windows-386":   true,
	"windows-amd64": true,
}

type data struct {
	Branch    string
	Builders  []*builder
	Commits   []*commit
	Dashboard struct {
		Name string
	}
	Package    dashPackage
	Pagination *struct{}
	TagState   []struct{}
}

// MaintnerClient is a subset of apipb.MaintnerServiceClient.
type MaintnerClient interface {
	// GetDashboard is extracted from apipb.MaintnerServiceClient.
	GetDashboard(ctx context.Context, in *apipb.DashboardRequest, opts ...grpc4.CallOption) (*apipb.DashboardResponse, error)
}

type Handler struct {
	// Datastore is a client used for fetching build status. If nil, it uses in-memory storage of build status.
	Datastore *datastore.Client
	// Maintner is a client for Maintner, used for fetching lists of commits.
	Maintner MaintnerClient

	// memoryResults is an in-memory storage of CI results. Used in development and testing for datastore data.
	memoryResults map[string][]string
}

func (d *Handler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	dd := &data{
		Builders: d.getBuilders(dashboard.Builders),
		Commits:  d.commits(r.Context()),
		Package:  dashPackage{Name: "Go"},
	}

	var buf bytes.Buffer
	if err := templ.Execute(&buf, dd); err != nil {
		log.Printf("handleDashboard: error rendering template: %v", err)
		http.Error(rw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(rw)
}

func (d *Handler) commits(ctx context.Context) []*commit {
	var commits []*commit
	resp, err := d.Maintner.GetDashboard(ctx, &apipb.DashboardRequest{})
	if err != nil {
		log.Printf("handleDashboard: error fetching from maintner: %v", err)
		return commits
	}
	for _, c := range resp.GetCommits() {
		commits = append(commits, &commit{
			Desc: c.Title,
			Hash: c.Commit,
			Time: time.Unix(c.CommitTimeSec, 0).Format("02 Jan 15:04"),
			User: formatGitAuthor(c.AuthorName, c.AuthorEmail),
		})
	}
	d.getResults(ctx, commits)
	return commits
}

// getResults populates result data on commits, fetched from Datastore or in-memory storage.
func (d *Handler) getResults(ctx context.Context, commits []*commit) {
	if d.Datastore == nil {
		for _, c := range commits {
			if result, ok := d.memoryResults[c.Hash]; ok {
				c.ResultData = result
			}
		}
		return
	}
	getDatastoreResults(ctx, d.Datastore, commits, "go")
}

func (d *Handler) getBuilders(conf map[string]*dashboard.BuildConfig) []*builder {
	bm := make(map[string]builder)
	for _, b := range conf {
		if !b.BuildsRepoPostSubmit("go", "master", "master") {
			continue
		}
		db := bm[b.GOOS()]
		db.OS = b.GOOS()
		db.Archs = append(db.Archs, &arch{
			Arch: b.GOARCH(),
			Name: b.Name,
			Tag:  strings.TrimPrefix(b.Name, fmt.Sprintf("%s-%s-", b.GOOS(), b.GOARCH())),
		})
		bm[b.GOOS()] = db
	}
	var builders builderSlice
	for _, db := range bm {
		db := db
		sort.Sort(&db.Archs)
		builders = append(builders, &db)
	}
	sort.Sort(builders)
	return builders
}

type arch struct {
	Arch string
	Name string
	Tag  string
}

func (a arch) FirstClass() bool {
	segs := strings.SplitN(a.Name, "-", 3)
	if len(segs) < 2 {
		return false
	}
	if fc, ok := firstClassPorts[strings.Join(segs[0:2], "-")]; ok {
		return fc
	}
	return false
}

type archSlice []*arch

func (d archSlice) Len() int {
	return len(d)
}

// Less sorts first-class ports first, then it sorts by name.
func (d archSlice) Less(i, j int) bool {
	iFirst, jFirst := d[i].FirstClass(), d[j].FirstClass()
	if iFirst && !jFirst {
		return true
	}
	if !iFirst && jFirst {
		return false
	}
	return d[i].Name < d[j].Name
}

func (d archSlice) Swap(i, j int) {
	d[i], d[j] = d[j], d[i]
}

type builder struct {
	Active      bool
	Archs       archSlice
	OS          string
	Unsupported bool
}

func (b *builder) FirstClass() bool {
	for _, a := range b.Archs {
		if a.FirstClass() {
			return true
		}
	}
	return false
}

func (b *builder) FirstClassArchs() archSlice {
	var as archSlice
	for _, a := range b.Archs {
		if a.FirstClass() {
			as = append(as, a)
		}
	}
	return as
}

type builderSlice []*builder

func (d builderSlice) Len() int {
	return len(d)
}

// Less sorts first-class ports first, then it sorts by name.
func (d builderSlice) Less(i, j int) bool {
	iFirst, jFirst := d[i].FirstClass(), d[j].FirstClass()
	if iFirst && !jFirst {
		return true
	}
	if !iFirst && jFirst {
		return false
	}
	return d[i].OS < d[j].OS
}

func (d builderSlice) Swap(i, j int) {
	d[i], d[j] = d[j], d[i]
}

type dashPackage struct {
	Name string
	Path string
}

type commit struct {
	Desc       string
	Hash       string
	ResultData []string
	Time       string
	User       string
}

// shortUser returns a shortened version of a user string.
func (c *commit) ShortUser() string {
	user := c.User
	if i, j := strings.Index(user, "<"), strings.Index(user, ">"); 0 <= i && i < j {
		user = user[i+1 : j]
	}
	if i := strings.Index(user, "@"); i >= 0 {
		return user[:i]
	}
	return user
}

func (c *commit) ResultForBuilder(builder string) result {
	for _, rd := range c.ResultData {
		segs := strings.Split(rd, "|")
		if len(segs) < 4 {
			continue
		}
		if segs[0] == builder {
			return result{
				OK:      segs[1] == "true",
				LogHash: segs[2],
			}
		}
	}
	return result{}
}

type result struct {
	BuildingURL string
	OK          bool
	LogHash     string
}

// formatGitAuthor formats the git author name and email (as split by
// maintner) back into the unified string how they're stored in a git
// commit, so the shortUser func (used by the HTML template) can parse
// back out the email part's username later. Maybe we could plumb down
// the parsed proto into the template later.
func formatGitAuthor(name, email string) string {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name != "" && email != "" {
		return fmt.Sprintf("%s <%s>", name, email)
	}
	if name != "" {
		return name
	}
	return "<" + email + ">"
}

var templ = template.Must(
	template.New("dashboard.html").ParseFiles(
		internal.FilePath("dashboard.html", "internal/dashboard", "cmd/coordinator/internal/dashboard"),
	),
)

// A Commit describes an individual commit in a package.
//
// Each Commit entity is a descendant of its associated Package entity.
// In other words, all Commits with the same PackagePath belong to the same
// datastore entity group.
type Commit struct {
	PackagePath string // (empty for main repo commits)
	Hash        string

	// ResultData is the Data string of each build Result for this Commit.
	// For non-Go commits, only the Results for the current Go tip, weekly,
	// and release Tags are stored here. This is purely de-normalized data.
	// The complete data set is stored in Result entities.
	//
	// Each string is formatted as builder|OK|LogHash|GoHash.
	ResultData []string `datastore:",noindex"`
}
