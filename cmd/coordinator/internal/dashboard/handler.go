// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

// Package dashboard contains the implementation of the build dashboard for the Coordinator.
package dashboard

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/datastore"
	"golang.org/x/build/cmd/coordinator/internal/lucipoll"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/migration"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"google.golang.org/grpc"
)

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
	GetDashboard(ctx context.Context, in *apipb.DashboardRequest, opts ...grpc.CallOption) (*apipb.DashboardResponse, error)
}

type luciClient interface {
	PostSubmitSnapshot() lucipoll.Snapshot
}

type Handler struct {
	// Datastore is a client used for fetching build status. If nil, it uses in-memory storage of build status.
	Datastore *datastore.Client
	// Maintner is a client for Maintner, used for fetching lists of commits.
	Maintner MaintnerClient
	// LUCI is a client for LUCI, used for fetching build results from there.
	LUCI luciClient

	// memoryResults is an in-memory storage of CI results. Used in development and testing for datastore data.
	memoryResults map[string][]string
}

func (d *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var showLUCI = true
	if legacyOnly, _ := strconv.ParseBool(req.URL.Query().Get("legacyonly")); legacyOnly {
		showLUCI = false
	}

	var luci lucipoll.Snapshot
	if d.LUCI != nil && showLUCI {
		luci = d.LUCI.PostSubmitSnapshot()
	}

	dd := &data{
		Builders: d.getBuilders(dashboard.Builders, luci),
		Commits:  d.commits(req.Context(), luci),
		Package:  dashPackage{Name: "Go"},
	}

	var buf bytes.Buffer
	if err := templ.Execute(&buf, dd); err != nil {
		log.Printf("handleDashboard: error rendering template: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

func (d *Handler) commits(ctx context.Context, luci lucipoll.Snapshot) []*commit {
	resp, err := d.Maintner.GetDashboard(ctx, &apipb.DashboardRequest{})
	if err != nil {
		log.Printf("handleDashboard: error fetching from maintner: %v", err)
		return nil
	}
	var commits []*commit
	for _, c := range resp.GetCommits() {
		commits = append(commits, &commit{
			Desc: c.Title,
			Hash: c.Commit,
			Time: time.Unix(c.CommitTimeSec, 0).Format("02 Jan 15:04"),
			User: formatGitAuthor(c.AuthorName, c.AuthorEmail),
		})
	}
	d.getResults(ctx, commits, luci)
	return commits
}

// getResults populates result data on commits, fetched from Datastore or in-memory storage
// and, if luci is non-zero, also from LUCI.
func (d *Handler) getResults(ctx context.Context, commits []*commit, luci lucipoll.Snapshot) {
	if d.Datastore != nil {
		getDatastoreResults(ctx, d.Datastore, commits, "go")
	} else {
		for _, c := range commits {
			if result, ok := d.memoryResults[c.Hash]; ok {
				c.ResultData = result
			}
		}
	}
	appendLUCIResults(luci, commits, "go")
}

func (d *Handler) getBuilders(conf map[string]*dashboard.BuildConfig, luci lucipoll.Snapshot) []*builder {
	bm := make(map[string]builder)
	for _, b := range conf {
		if !b.BuildsRepoPostSubmit("go", "master", "master") {
			continue
		}
		if migration.BuildersPortedToLUCI[b.Name] && len(luci.Builders) > 0 {
			// Don't display old builders that have been ported
			// to LUCI if willing to show LUCI builders as well.
			continue
		}
		db := bm[b.GOOS()]
		db.OS = b.GOOS()
		db.Archs = append(db.Archs, &arch{
			os: b.GOOS(), Arch: b.GOARCH(),
			Name: b.Name,
			// Tag is the part after "os-arch", if any, without leading dash.
			Tag: strings.TrimPrefix(strings.TrimPrefix(b.Name, fmt.Sprintf("%s-%s", b.GOOS(), b.GOARCH())), "-"),
		})
		bm[b.GOOS()] = db
	}

	for _, b := range luci.Builders {
		if b.Repo != "go" || b.GoBranch != "master" {
			continue
		}
		db := bm[b.Target.GOOS]
		db.OS = b.Target.GOOS
		tagFriendly := b.Name + "-🐇"
		if after, ok := strings.CutPrefix(tagFriendly, fmt.Sprintf("gotip-%s-%s_", b.Target.GOOS, b.Target.GOARCH)); ok {
			// Convert os-arch_osversion-mod1-mod2 (an underscore at start of "_osversion")
			// to have os-arch-osversion-mod1-mod2 (a dash at start of "-osversion") form.
			// The tag computation below uses this to find both "osversion-mod1" or "mod1".
			tagFriendly = fmt.Sprintf("gotip-%s-%s-", b.Target.GOOS, b.Target.GOARCH) + after
		}
		db.Archs = append(db.Archs, &arch{
			os: b.Target.GOOS, Arch: b.Target.GOARCH,
			Name: b.Name,
			// Tag is the part after "os-arch", if any, without leading dash.
			Tag: strings.TrimPrefix(strings.TrimPrefix(tagFriendly, fmt.Sprintf("gotip-%s-%s", b.Target.GOOS, b.Target.GOARCH)), "-"),
		})
		bm[b.Target.GOOS] = db
	}

	var builders builderSlice
	for _, db := range bm {
		sort.Sort(&db.Archs)
		builders = append(builders, &db)
	}
	sort.Sort(builders)
	return builders
}

type arch struct {
	os, Arch string
	Name     string
	Tag      string
}

func (a arch) FirstClass() bool { return releasetargets.IsFirstClass(a.os, a.Arch) }

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
	Desc string
	Hash string
	// ResultData is a copy of the [Commit.ResultData] field from datastore,
	// with an additional rule that the second '|'-separated value may be "infra_failure"
	// to indicate a problem with the infrastructure rather than the code being tested.
	//
	// It can also have the form of "builder|BuildingURL" for in progress builds.
	ResultData []string
	Time       string
	User       string
}

// ShortUser returns a shortened version of a user string.
func (c *commit) ShortUser() string {
	user := c.User
	if i, j := strings.Index(user, "<"), strings.Index(user, ">"); 0 <= i && i < j {
		user = user[i+1 : j]
	}
	if before, _, ok := strings.Cut(user, "@"); ok {
		return before
	}
	return user
}

func (c *commit) ResultForBuilder(builder string) *result {
	for _, rd := range c.ResultData {
		segs := strings.Split(rd, "|")
		if len(segs) == 2 && segs[0] == builder {
			return &result{
				BuildingURL: segs[1],
			}
		}
		if len(segs) < 4 {
			continue
		}
		if segs[0] == builder {
			return &result{
				OK:      segs[1] == "true",
				Noise:   segs[1] == "infra_failure",
				LogHash: segs[2],
			}
		}
	}
	return nil
}

type result struct {
	BuildingURL string
	OK          bool
	Noise       bool
	LogHash     string
}

func (r result) LogURL() string {
	if strings.HasPrefix(r.LogHash, "https://") {
		return r.LogHash
	} else {
		return "https://build.golang.org/log/" + r.LogHash
	}
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

//go:embed dashboard.html
var dashboardTemplate string

var templ = template.Must(
	template.New("dashboard.html").Funcs(template.FuncMap{
		"shortHash": shortHash,
	}).Parse(dashboardTemplate),
)

// shortHash returns a short version of a hash.
func shortHash(hash string) string {
	if len(hash) > 7 {
		hash = hash[:7]
	}
	return hash
}

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
