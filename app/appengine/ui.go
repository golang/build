// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/dashboard"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/repos"
	"golang.org/x/build/types"
	"grpc.go4.org"
	"grpc.go4.org/codes"

	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
)

// uiHandler is the HTTP handler for the https://build.golang.org/.
func uiHandler(w http.ResponseWriter, r *http.Request) {
	view, err := viewForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	dashReq, err := dashboardRequest(view, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// TODO: if we end up fetching the building state from the
	// coordinator too, do that concurrently in an
	// x/sync/errgroup.Group here. But for now we're only doing
	// one RPC call.
	ctx := goDash.Context(appengine.NewContext(r))
	dashRes, err := maintnerClient.GetDashboard(ctx, dashReq)
	if err != nil {
		http.Error(w, "maintner.GetDashboard: "+err.Error(), httpStatusOfErr(err))
		return
	}

	tb := newUITemplateDataBuilder(view, dashReq, dashRes)
	data, err := tb.buildTemplateData(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view.ServeDashboard(w, r, data)
}

// dashboardView is something that can render uiTemplateData.
// See viewForRequest.
type dashboardView interface {
	ServeDashboard(w http.ResponseWriter, r *http.Request, data *uiTemplateData)
}

// viewForRequest selects the dashboardView based on the HTTP
// request's "mode" parameter. Any error should be considered
// an HTTP 400 Bad Request.
func viewForRequest(r *http.Request) (dashboardView, error) {
	if r.Method != "GET" && r.Method != "HEAD" {
		return nil, errors.New("unsupported method")
	}
	switch r.FormValue("mode") {
	case "failures":
		return failuresView{}, nil
	case "json":
		return jsonView{}, nil
	case "":
		return htmlView{}, nil
	}
	return nil, errors.New("unsupported mode argument")
}

type commitInPackage struct {
	packagePath string // "" for Go, else package import path
	commit      string // git commit hash
}

// uiTemplateDataBuilder builds the uiTemplateData used by the various
// dashboardViews. That is, it maps the maintner protobuf response to
// the data structure needed by the dashboardView/template.
type uiTemplateDataBuilder struct {
	view dashboardView
	req  *apipb.DashboardRequest
	res  *apipb.DashboardResponse

	// testCommitData, if non-nil, provides an alternate data
	// source to use for testing instead of making real datastore
	// calls. The keys are stringified datastore.Keys.
	testCommitData map[string]*Commit
}

// newUITemplateDataBuilder returns a new uiTemplateDataBuilder for a
// given view, dashboard request, and dashboard response from
// maintner.
func newUITemplateDataBuilder(view dashboardView, req *apipb.DashboardRequest, res *apipb.DashboardResponse) *uiTemplateDataBuilder {
	return &uiTemplateDataBuilder{
		view: view,
		req:  req,
		res:  res,
	}
}

// getCommitsToLoad returns a set (all values are true) of which commits to load from
// the datastore.
func (tb *uiTemplateDataBuilder) getCommitsToLoad() map[commitInPackage]bool {
	m := make(map[commitInPackage]bool)
	add := func(packagePath, commit string) {
		m[commitInPackage{packagePath: packagePath, commit: commit}] = true
	}

	for _, dc := range tb.res.Commits {
		add(tb.req.Repo, dc.Commit)
	}
	// We also want to load the Commits for the x/repo heads.
	if tb.showXRepoSection() {
		for _, rh := range tb.res.RepoHeads {
			if path := repoImportPath(rh); path != "" {
				add(path, rh.Commit.Commit)
			}
		}
	}
	return m
}

// loadDatastoreCommits loads the commits given in the keys of the
// want map. The returned map is keyed by the git hash and may not
// contain items that didn't exist in the datastore. (It is not an
// error if 1 or all don't exist.)
func (tb *uiTemplateDataBuilder) loadDatastoreCommits(ctx context.Context, want map[commitInPackage]bool) (map[string]*Commit, error) {
	ret := map[string]*Commit{}

	// Allow tests to fake what the datastore would've loaded, and
	// thus also allow tests to be run without a real (or
	// dev_appserver-based fake) datastore.
	if m := tb.testCommitData; m != nil {
		for k := range want {
			if c, ok := m[k.commit]; ok {
				ret[k.commit] = c
			}
		}
		return ret, nil
	}

	var keys []*datastore.Key
	for k := range want {
		key := (&Commit{
			PackagePath: k.packagePath,
			Hash:        k.commit,
		}).Key(ctx)
		keys = append(keys, key)
	}
	commits, err := fetchCommits(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("fetchCommits: %v", err)
	}
	for _, c := range commits {
		ret[c.Hash] = c
	}
	return ret, nil
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

// newCommitInfo returns a new CommitInfo populated for the template
// data given a repo name and a dashboard commit from that repo, using
// previously loaded datastore commit info in tb.
func (tb *uiTemplateDataBuilder) newCommitInfo(dsCommits map[string]*Commit, repo string, dc *apipb.DashCommit) *CommitInfo {
	branch := dc.Branch
	if branch == "" {
		branch = "master"
	}
	ci := &CommitInfo{
		Hash:        dc.Commit,
		PackagePath: repo,
		User:        formatGitAuthor(dc.AuthorName, dc.AuthorEmail),
		Desc:        cleanTitle(dc.Title, tb.req.Branch),
		Time:        time.Unix(dc.CommitTimeSec, 0),
		Branch:      branch,
	}
	if dsc, ok := dsCommits[dc.Commit]; ok {
		ci.ResultData = dsc.ResultData
	}
	// For non-go repos, add the rows for the Go commits that were
	// at HEAD overlapping in time with dc.Commit.
	isGo := tb.req.Repo == "" || tb.req.Repo == "go"
	if !isGo {
		if dc.GoCommitAtTime != "" {
			ci.addEmptyResultGoHash(dc.GoCommitAtTime)
		}
		if dc.GoCommitLatest != "" && dc.GoCommitLatest != dc.GoCommitAtTime {
			ci.addEmptyResultGoHash(dc.GoCommitLatest)
		}
	}
	return ci
}

// showXRepoSection reports whether the dashboard should show the state of the x/foo repos at the bottom of
// the page in the three branches (master, latest release branch, two releases ago).
func (tb *uiTemplateDataBuilder) showXRepoSection() bool {
	return tb.req.Page == 0 &&
		(tb.req.Branch == "" || tb.req.Branch == "master" || tb.req.Branch == "mixed") &&
		(tb.req.Repo == "" || tb.req.Repo == "go")
}

// repoImportPath returns the import path for rh, unless rh is the
// main "go" repo or is configured to be hidden from the dashboard, in
// which case it returns the empty string.
func repoImportPath(rh *apipb.DashRepoHead) string {
	if rh.GerritProject == "go" {
		return ""
	}
	ri, ok := repos.ByGerritProject[rh.GerritProject]
	if !ok || ri.HideFromDashboard {
		return ""
	}
	return ri.ImportPath
}

func (tb *uiTemplateDataBuilder) buildTemplateData(ctx context.Context) (*uiTemplateData, error) {
	dsCommits, err := tb.loadDatastoreCommits(ctx, tb.getCommitsToLoad())
	if err != nil {
		return nil, err
	}

	var commits []*CommitInfo
	for _, dc := range tb.res.Commits {
		ci := tb.newCommitInfo(dsCommits, tb.req.Repo, dc)
		commits = append(commits, ci)
	}

	// x/ repo sections at bottom (each is a "TagState", for historical reasons)
	var xRepoSections []*TagState
	if tb.showXRepoSection() {
		for _, gorel := range tb.res.Releases {
			ts := &TagState{
				Name: gorel.BranchName,
				Tag: &CommitInfo{ // only a minimally populated version is needed by the template
					Hash: gorel.BranchCommit,
				},
			}
			for _, rh := range tb.res.RepoHeads {
				path := repoImportPath(rh)
				if path == "" {
					continue
				}
				ts.Packages = append(ts.Packages, &PackageState{
					Package: &Package{
						Name: rh.GerritProject,
						Path: path,
					},
					Commit: tb.newCommitInfo(dsCommits, path, rh.Commit),
				})
			}
			sort.Slice(ts.Packages, func(i, j int) bool {
				return ts.Packages[i].Package.Name < ts.Packages[j].Package.Name
			})
			xRepoSections = append(xRepoSections, ts)
		}
	}

	// Release Branches
	var releaseBranches []string
	for _, gr := range tb.res.Releases {
		if gr.BranchName != "master" {
			releaseBranches = append(releaseBranches, gr.BranchName)
		}
	}

	builders := commitBuilders(commits, releaseBranches)
	data := &uiTemplateData{
		Dashboard:  goDash,
		Package:    goDash.packageWithPath(tb.req.Repo),
		Commits:    commits,
		Builders:   builders,
		TagState:   xRepoSections,
		Pagination: &Pagination{},
		Branches:   tb.res.Branches,
		Branch:     tb.req.Branch,
	}

	if tb.res.CommitsTruncated {
		data.Pagination.Next = int(tb.req.Page) + 1
	}
	if tb.req.Page > 0 {
		data.Pagination.Prev = int(tb.req.Page) - 1
		data.Pagination.HasPrev = true
	}

	if tb.view == (htmlView{}) {
		// In the UI, when viewing the master branch of the main
		// Go repository, hide builders that aren't active on it.
		if tb.req.Repo == "" && tb.req.Branch == "master" {
			data.Builders = onlyGoMasterBuilders(data.Builders)
		}

		// Populate building URLs for the HTML UI only.
		data.populateBuildingURLs(ctx)
	}

	return data, nil
}

// htmlView renders the HTML (default) form of https://build.golang.org/ with no mode parameter.
type htmlView struct{}

func (htmlView) ServeDashboard(w http.ResponseWriter, r *http.Request, data *uiTemplateData) {
	var buf bytes.Buffer
	if err := uiTemplate.Execute(&buf, data); err != nil {
		logErr(w, r, err)
		return
	}
	buf.WriteTo(w)
}

// dashboardRequest is a pure function that maps the provided HTTP
// request to a maintner DashboardRequest and lightly validates the
// HTTP request for the root dashboard handler. (It does not validate
// that, say, branches or repos are valid.)
// Any returned error is an HTTP 400 Bad Request.
func dashboardRequest(view dashboardView, r *http.Request) (*apipb.DashboardRequest, error) {
	page := 0
	if s := r.FormValue("page"); s != "" {
		var err error
		page, err = strconv.Atoi(r.FormValue("page"))
		if err != nil {
			return nil, fmt.Errorf("invalid page value %q", s)
		}
		if page < 0 {
			return nil, errors.New("negative page")
		}
	}

	repo := r.FormValue("repo") // empty for main go repo, else e.g. "golang.org/x/net"

	branch := r.FormValue("branch")
	if branch == "" {
		branch = "master"
	}
	return &apipb.DashboardRequest{
		Page:       int32(page),
		Branch:     branch,
		Repo:       repo,
		MaxCommits: commitsPerPage,
	}, nil
}

// cleanTitle returns a cleaned version of the provided title for
// users viewing the provided viewBranch.
func cleanTitle(title, viewBranch string) string {
	// Don't rewrite anything for master and mixed.
	if viewBranch == "master" || viewBranch == "mixed" {
		return title
	}
	// Strip the "[release-branch.go1.n]" prefixes from commit messages
	// when looking at a branch.
	if strings.HasPrefix(title, "[") {
		if i := strings.IndexByte(title, ']'); i != -1 {
			return strings.TrimSpace(title[i+1:])
		}
	}
	return title
}

// failuresView renders https://build.golang.org/?mode=failures, where it outputs
// one line per failure on the front page, in the form:
//    hash builder failure-url
type failuresView struct{}

func (failuresView) ServeDashboard(w http.ResponseWriter, r *http.Request, data *uiTemplateData) {
	w.Header().Set("Content-Type", "text/plain")
	for _, c := range data.Commits {
		for _, b := range data.Builders {
			res := c.Result(b, "")
			if res == nil || res.OK || res.LogHash == "" {
				continue
			}
			url := fmt.Sprintf("https://%v/log/%v", r.Host, res.LogHash)
			fmt.Fprintln(w, c.Hash, b, url)
		}
	}
}

// jsonView renders https://build.golang.org/?mode=json.
// The output is a types.BuildStatus JSON object.
type jsonView struct{}

func (jsonView) ServeDashboard(w http.ResponseWriter, r *http.Request, data *uiTemplateData) {
	// cell returns one of "" (no data), "ok", or a failure URL.
	cell := func(res *Result) string {
		switch {
		case res == nil:
			return ""
		case res.OK:
			return "ok"
		}
		return fmt.Sprintf("https://%v/log/%v", r.Host, res.LogHash)
	}

	var res types.BuildStatus
	res.Builders = data.Builders

	// First the commits from the main section (the "go" repo)
	for _, c := range data.Commits {
		rev := types.BuildRevision{
			Repo:    "go",
			Results: make([]string, len(data.Builders)),
		}
		commitToBuildRevision(c, &rev)
		for i, b := range data.Builders {
			rev.Results[i] = cell(c.Result(b, ""))
		}
		res.Revisions = append(res.Revisions, rev)
	}

	// Then the one commit each for the subrepos for each of the tracked tags.
	// (tip, Go 1.4, etc)
	for _, ts := range data.TagState {
		for _, pkgState := range ts.Packages {
			goRev := ts.Tag.Hash
			goBranch := ts.Name
			if goBranch == "tip" {
				// Normalize old hg terminology into
				// our git branch name.
				goBranch = "master"
			}
			rev := types.BuildRevision{
				Repo:       pkgState.Package.Name,
				GoRevision: goRev,
				Results:    make([]string, len(data.Builders)),
				GoBranch:   goBranch,
			}
			commitToBuildRevision(pkgState.Commit, &rev)
			for i, b := range res.Builders {
				rev.Results[i] = cell(pkgState.Commit.Result(b, goRev))
			}
			res.Revisions = append(res.Revisions, rev)
		}
	}

	v, _ := json.MarshalIndent(res, "", "\t")
	w.Header().Set("Content-Type", "text/json; charset=utf-8")
	w.Write(v)
}

// commitToBuildRevision fills in the fields of BuildRevision rev that
// are derived from Commit c.
func commitToBuildRevision(c *CommitInfo, rev *types.BuildRevision) {
	rev.Revision = c.Hash
	rev.Date = c.Time.Format(time.RFC3339)
	rev.Author = c.User
	rev.Desc = c.Desc
	rev.Branch = c.Branch
}

type Pagination struct {
	Next, Prev int
	HasPrev    bool
}

// fetchCommits loads any commits that exist given by keys.
// It is not an error if a commit doesn't exist.
// Only commits that were found in datastore are returned,
// in an unspecified order.
func fetchCommits(ctx context.Context, keys []*datastore.Key) ([]*Commit, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([]*Commit, len(keys))
	for i := range keys {
		out[i] = new(Commit)
	}

	err := datastore.GetMulti(ctx, keys, out)
	err = filterDatastoreError(err)
	err = filterNoSuchEntity(err)
	if err != nil {
		return nil, err
	}
	filtered := out[:0]
	for _, c := range out {
		if c.Valid() { // that is, successfully loaded
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

// commitBuilders returns the names of active builders that provided
// Results for the provided commits.
func commitBuilders(commits []*CommitInfo, releaseBranches []string) []string {
	builders := make(map[string]bool)
	for _, commit := range commits {
		for _, r := range commit.Results() {
			if r.Builder != "" {
				builders[r.Builder] = true
			}
		}
	}
	// Add all known builders from the builder configuration too.
	// We want to see columns even if there are no results so we
	// can identify missing builders. (Issue 19930)
	for name, bc := range dashboard.Builders {
		if !activePostSubmitBuilder(bc, releaseBranches) {
			continue
		}
		builders[name] = true
	}
	k := keys(builders)
	sort.Sort(builderOrder(k))
	return k
}

// activePostSubmitBuilder reports whether the builder bc
// is considered to be "active", meaning it's configured
// to test the Go repository on master branch or at least
// one of the supported release branches.
func activePostSubmitBuilder(bc *dashboard.BuildConfig, releaseBranches []string) bool {
	if bc.BuildsRepoPostSubmit("go", "master", "master") {
		return true
	}
	for _, rb := range releaseBranches {
		if bc.BuildsRepoPostSubmit("go", rb, rb) {
			return true
		}
	}
	// TODO(golang.org/issue/34744): This doesn't catch x-repo-only builders yet; adjust further as needed.
	return false
}

func keys(m map[string]bool) (s []string) {
	s = make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	sort.Strings(s)
	return
}

// onlyGoMasterBuilders returns a subset of all builders that are
// configured to do post-submit builds on master branch of Go repo.
func onlyGoMasterBuilders(all []string) []string {
	var goMaster []string
	for _, name := range all {
		bc, ok := dashboard.Builders[name]
		if !ok || !bc.BuildsRepoPostSubmit("go", "master", "master") {
			continue
		}
		goMaster = append(goMaster, name)
	}
	return goMaster
}

// builderOrder implements sort.Interface, sorting builder names
// ("darwin-amd64", etc) first by builderPriority and then alphabetically.
type builderOrder []string

func (s builderOrder) Len() int      { return len(s) }
func (s builderOrder) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s builderOrder) Less(i, j int) bool {
	pi, pj := builderPriority(s[i]), builderPriority(s[j])
	if pi == pj {
		return s[i] < s[j]
	}
	return pi < pj
}

func builderPriority(builder string) (p int) {
	// Group race builders together.
	if isRace(builder) {
		return 2
	}
	// If the OS has a specified priority, use it.
	if p, ok := osPriority[builderOS(builder)]; ok {
		return p
	}
	// The rest.
	return 10
}

func isRace(s string) bool {
	return strings.Contains(s, "-race-") || strings.HasSuffix(s, "-race")
}

func unsupported(builder string) bool {
	return unsupportedOS(builderOS(builder))
}

func unsupportedOS(os string) bool {
	if os == "race" || os == "android" || os == "all" {
		return false
	}
	p, ok := osPriority[os]
	return !ok || p > 1
}

// Priorities for specific operating systems.
var osPriority = map[string]int{
	"all":     0,
	"darwin":  1,
	"freebsd": 1,
	"linux":   1,
	"windows": 1,
	// race == 2
	"android":   3,
	"openbsd":   4,
	"netbsd":    5,
	"dragonfly": 6,
}

// TagState represents the state of all Packages at a branch.
type TagState struct {
	Name     string      // Go branch name: "master", "release-branch.go1.4", etc
	Tag      *CommitInfo // current Go commit on the Name branch
	Packages []*PackageState
}

// Branch returns the git branch name, converting from the old
// terminology we used from Go's hg days into git terminology.
func (ts *TagState) Branch() string {
	if ts.Name == "tip" {
		return "master"
	}
	return ts.Name
}

// PackageState represents the state of a Package (x/foo repo) for given Go branch.
type PackageState struct {
	Package *Package
	Commit  *CommitInfo
}

// A CommitInfo is a struct for use by html/template package.
// It is not stored in the datastore.
type CommitInfo struct {
	Hash string

	// ResultData is a copy of the Commit.ResultData field from datastore.
	ResultData []string

	buildingURLs map[builderAndGoHash]string

	PackagePath string    // (empty for main repo commits)
	User        string    // "Foo Bar <foo@bar.com>"
	Desc        string    // git commit title
	Time        time.Time // commit time
	Branch      string    // "master", "release-branch.go1.14"
}

// addEmptyResultGoHash adds an empty result containing goHash to
// ci.ResultData, unless ci already contains a result for that hash.
// This is used for non-go repos to show the go commits (both earliest
// and latest) that correspond to this repo's commit time. We add an
// empty result so it shows up on the dashboard (both for humans, and
// in JSON form for the coordinator to pick up as work). Once the
// coordinator does that work and posts its result, then ResultData
// will be populate and this turns into a no-op.
func (ci *CommitInfo) addEmptyResultGoHash(goHash string) {
	for _, exist := range ci.ResultData {
		if strings.Contains(exist, goHash) {
			return
		}
	}
	ci.ResultData = append(ci.ResultData, (&Result{GoHash: goHash}).Data())
}

type uiTemplateData struct {
	Dashboard  *Dashboard
	Package    *Package
	Commits    []*CommitInfo
	Builders   []string
	TagState   []*TagState
	Pagination *Pagination
	Branches   []string
	Branch     string
}

// buildingKey returns a memcache key that points to the log URL
// of an inflight build for the given hash, goHash, and builder.
func buildingKey(hash, goHash, builder string) string {
	return fmt.Sprintf("building|%v|%v|%v", hash, goHash, builder)
}

// skipMemcacheForTest, if true, disables memcache operations for use in tests.
var skipMemcacheForTest = false

// populateBuildingURLs populates each commit in Commits' buildingURLs map with the
// URLs of builds which are currently in progress.
func (td *uiTemplateData) populateBuildingURLs(ctx context.Context) {
	if skipMemcacheForTest {
		return
	}

	// need are memcache keys: "building|<hash>|<gohash>|<builder>"
	// The hash is of the main "go" repo, or the subrepo commit hash.
	// The gohash is empty for the main repo, else it's the Go hash.
	var need []string

	commit := map[string]*CommitInfo{} // commit hash -> Commit

	// Gather pending commits for main repo.
	for _, b := range td.Builders {
		for _, c := range td.Commits {
			if c.Result(b, "") == nil {
				commit[c.Hash] = c
				need = append(need, buildingKey(c.Hash, "", b))
			}
		}
	}

	// Gather pending commits for sub-repos.
	for _, ts := range td.TagState {
		goHash := ts.Tag.Hash
		for _, b := range td.Builders {
			for _, pkg := range ts.Packages {
				c := pkg.Commit
				commit[c.Hash] = c
				if c.Result(b, goHash) == nil {
					need = append(need, buildingKey(c.Hash, goHash, b))
				}
			}
		}
	}

	if len(need) == 0 {
		return
	}

	m, err := memcache.GetMulti(ctx, need)
	if err != nil {
		// oh well. this is a cute non-critical feature anyway.
		log.Debugf(ctx, "GetMulti of building keys: %v", err)
		return
	}
	for k, it := range m {
		f := strings.SplitN(k, "|", 4)
		if len(f) != 4 {
			continue
		}
		hash, goHash, builder := f[1], f[2], f[3]
		c, ok := commit[hash]
		if !ok {
			continue
		}
		m := c.buildingURLs
		if m == nil {
			m = make(map[builderAndGoHash]string)
			c.buildingURLs = m
		}
		m[builderAndGoHash{builder, goHash}] = string(it.Value)
	}

}

var uiTemplate = template.Must(
	template.New("ui.html").Funcs(tmplFuncs).ParseFiles(templateFile("ui.html")),
)

var tmplFuncs = template.FuncMap{
	"builderSpans":       builderSpans,
	"builderSubheading":  builderSubheading,
	"builderSubheading2": builderSubheading2,
	"shortDesc":          shortDesc,
	"shortHash":          shortHash,
	"shortUser":          shortUser,
	"tail":               tail,
	"unsupported":        unsupported,
	"isUntested":         isUntested,
	"formatTime":         formatTime,
}

func formatTime(t time.Time) string {
	if t.Year() != time.Now().Year() {
		return t.Format("02 Jan 06")
	}
	return t.Format("02 Jan 15:04")
}

func splitDash(s string) (string, string) {
	i := strings.Index(s, "-")
	if i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// builderOS returns the os tag for a builder string
func builderOS(s string) string {
	os, _ := splitDash(s)
	return os
}

// builderOSOrRace returns the builder OS or, if it is a race builder, "race".
func builderOSOrRace(s string) string {
	if isRace(s) {
		return "race"
	}
	return builderOS(s)
}

// builderArch returns the arch tag for a builder string
func builderArch(s string) string {
	_, arch := splitDash(s)
	arch, _ = splitDash(arch) // chop third part
	return arch
}

// builderSubheading returns a short arch tag for a builder string
// or, if it is a race builder, the builder OS.
func builderSubheading(s string) string {
	if isRace(s) {
		return builderOS(s)
	}
	return builderArch(s)
}

// builderSubheading2 returns any third part of a hyphenated builder name.
// For instance, for "linux-amd64-nocgo", it returns "nocgo".
// For race builders it returns the empty string.
func builderSubheading2(s string) string {
	if isRace(s) {
		return ""
	}
	_, secondThird := splitDash(s)
	_, third := splitDash(secondThird)
	return third
}

type builderSpan struct {
	N           int
	OS          string
	Unsupported bool
}

// builderSpans creates a list of tags showing
// the builder's operating system names, spanning
// the appropriate number of columns.
func builderSpans(s []string) []builderSpan {
	var sp []builderSpan
	for len(s) > 0 {
		i := 1
		os := builderOSOrRace(s[0])
		u := unsupportedOS(os)
		for i < len(s) && builderOSOrRace(s[i]) == os {
			i++
		}
		sp = append(sp, builderSpan{i, os, u})
		s = s[i:]
	}
	return sp
}

// shortDesc returns the first line of a description.
func shortDesc(desc string) string {
	if i := strings.Index(desc, "\n"); i != -1 {
		desc = desc[:i]
	}
	return limitStringLength(desc, 100)
}

// shortHash returns a short version of a hash.
func shortHash(hash string) string {
	if len(hash) > 7 {
		hash = hash[:7]
	}
	return hash
}

// shortUser returns a shortened version of a user string.
func shortUser(user string) string {
	if i, j := strings.Index(user, "<"), strings.Index(user, ">"); 0 <= i && i < j {
		user = user[i+1 : j]
	}
	if i := strings.Index(user, "@"); i >= 0 {
		return user[:i]
	}
	return user
}

// tail returns the trailing n lines of s.
func tail(n int, s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// templateFile returns the path to the provided HTML template file,
// conditionally prepending a relative path depending on the
// environment.
func templateFile(base string) string {
	// In tests the current directory is ".", but in prod it's up
	// two levels. So just look to see if it's in . first.
	if _, err := os.Stat(base); err == nil {
		return base
	}
	return filepath.Join("app/appengine", base)
}

func httpStatusOfErr(err error) int {
	fmt.Fprintf(os.Stderr, "Got error: %#v, code %v\n", err, grpc.Code(err))
	switch grpc.Code(err) {
	case codes.NotFound:
		return http.StatusNotFound
	case codes.InvalidArgument:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
