// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package legacydash

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/datastore"
	bbpb "go.chromium.org/luci/buildbucket/proto"
	"golang.org/x/build/cmd/coordinator/internal/lucipoll"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/migration"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/repos"
	"golang.org/x/build/types"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// uiHandler is the HTTP handler for the https://build.golang.org/.
func (h handler) uiHandler(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()
	tb := &uiTemplateDataBuilder{
		view: view,
		req:  dashReq,
	}
	var rpcs errgroup.Group
	rpcs.Go(func() error {
		var err error
		tb.res, err = h.maintnerCl.GetDashboard(ctx, dashReq)
		return err
	})
	if view.ShowsActiveBuilds() {
		rpcs.Go(func() error {
			tb.activeBuilds = getActiveBuilds(ctx)
			return nil
		})
	}
	if err := rpcs.Wait(); err != nil {
		http.Error(w, "maintner.GetDashboard: "+err.Error(), httpStatusOfErr(err))
		return
	}
	var luci lucipoll.Snapshot
	if h.LUCI != nil && tb.showLUCI() {
		luci = h.LUCI.PostSubmitSnapshot()
	}
	data, err := tb.buildTemplateData(ctx, h.datastoreCl, luci)
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
	// ShowsActiveBuilds reports whether this view uses
	// information about the currently active builds.
	ShowsActiveBuilds() bool
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
		var showLUCI = true
		if legacyOnly, _ := strconv.ParseBool(r.URL.Query().Get("legacyonly")); legacyOnly {
			showLUCI = false
		}
		return htmlView{ShowLUCI: showLUCI}, nil
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
	view         dashboardView
	req          *apipb.DashboardRequest
	res          *apipb.DashboardResponse
	activeBuilds []types.ActivePostSubmitBuild // optional; for blue gopher links

	// testCommitData, if non-nil, provides an alternate data
	// source to use for testing instead of making real datastore
	// calls. The keys are stringified datastore.Keys.
	testCommitData map[string]*Commit
}

// getCommitsToLoad returns a set (all values are true) of which commits to load
// from the datastore. If fakeResults is on, it returns an empty set.
func (tb *uiTemplateDataBuilder) getCommitsToLoad() map[commitInPackage]bool {
	if fakeResults {
		return nil
	}
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
func (tb *uiTemplateDataBuilder) loadDatastoreCommits(ctx context.Context, cl *datastore.Client, want map[commitInPackage]bool) (map[string]*Commit, error) {
	var keys []*datastore.Key
	for k := range want {
		key := (&Commit{
			PackagePath: k.packagePath,
			Hash:        k.commit,
		}).Key()
		keys = append(keys, key)
	}
	commits, err := fetchCommits(ctx, cl, keys)
	if err != nil {
		return nil, fmt.Errorf("fetchCommits: %v", err)
	}
	var ret = make(map[string]*Commit)
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
		Desc:        cleanTitle(dc.Title, tb.branch()),
		Time:        time.Unix(dc.CommitTimeSec, 0),
		Branch:      branch,
	}
	if dsc, ok := dsCommits[dc.Commit]; ok {
		// Remove extra dsc.ResultData capacity to make it
		// okay to append additional data to ci.ResultData.
		ci.ResultData = dsc.ResultData[:len(dsc.ResultData):len(dsc.ResultData)]
	}
	// For non-go repos, add the rows for the Go commits that were
	// at HEAD overlapping in time with dc.Commit.
	if !tb.isGoRepo() {
		if dc.GoCommitAtTime != "" {
			ci.addEmptyResultGoHash(dc.GoCommitAtTime)
		}
		if dc.GoCommitLatest != "" && dc.GoCommitLatest != dc.GoCommitAtTime {
			ci.addEmptyResultGoHash(dc.GoCommitLatest)
		}
	}
	return ci
}

func (tb *uiTemplateDataBuilder) showLUCI() bool {
	v, ok := tb.view.(htmlView)
	return ok && v == htmlView{ShowLUCI: true} &&
		// Only show LUCI build results for the default top-level view.
		tb.req.Page == 0 &&
		tb.req.Repo == "" &&
		tb.branch() == "master"
}

// showXRepoSection reports whether the dashboard should show the state of the x/foo repos at the bottom of
// the page in the three branches (master, latest release branch, two releases ago).
func (tb *uiTemplateDataBuilder) showXRepoSection() bool {
	return tb.req.Page == 0 &&
		(tb.branch() == "master" || tb.req.Branch == "mixed") &&
		tb.isGoRepo()
}

func (tb *uiTemplateDataBuilder) isGoRepo() bool { return tb.req.Repo == "" || tb.req.Repo == "go" }

// repoGerritProj returns the Gerrit project name on go.googlesource.com for
// the repo requested, or empty if unknown.
func (tb *uiTemplateDataBuilder) repoGerritProj() string {
	if tb.isGoRepo() {
		return "go"
	}
	if r, ok := repos.ByImportPath[tb.req.Repo]; ok {
		return r.GoGerritProject
	}
	return ""
}

// branch returns the request branch, or "master" if empty.
func (tb *uiTemplateDataBuilder) branch() string {
	if tb.req.Branch == "" {
		return "master"
	}
	return tb.req.Branch
}

// repoImportPath returns the import path for rh, unless rh is the
// main "go" repo or is configured to be hidden from the dashboard, in
// which case it returns the empty string.
func repoImportPath(rh *apipb.DashRepoHead) string {
	if rh.GerritProject == "go" {
		return ""
	}
	ri, ok := repos.ByGerritProject[rh.GerritProject]
	if !ok || !ri.ShowOnDashboard() {
		return ""
	}
	return ri.ImportPath
}

// appendLUCIResults appends result data polled from LUCI to commits.
func appendLUCIResults(luci lucipoll.Snapshot, commits []*CommitInfo, repo string) {
	commitBuilds, ok := luci.RepoCommitBuilds[repo]
	if !ok {
		return
	}
	for _, c := range commits {
		builds, ok := commitBuilds[c.Hash]
		if !ok {
			// No builds for this commit.
			continue
		}
		for _, b := range builds {
			builder := luci.Builders[b.BuilderName]
			if builder.Repo != repo || builder.GoBranch != "master" {
				// TODO: Support builder.GoBranch != master for x/ repos.
				// Or maybe it's fine to leave that to appendLUCIResultsXRepo only,
				// and have this appendLUCIResults function deal repo == "go" only.
				// Hmm, maybe it needs to handle repo != "go" for when viewing the
				// individual repo commit views... I'll see later.
				continue
			}
			switch b.Status {
			case bbpb.Status_STARTED, bbpb.Status_SUCCESS, bbpb.Status_FAILURE, bbpb.Status_INFRA_FAILURE:
			default:
				continue
			}
			tagFriendly := builder.Name
			if after, ok := strings.CutPrefix(tagFriendly, fmt.Sprintf("gotip-%s-%s_", builder.Target.GOOS, builder.Target.GOARCH)); ok {
				// Convert os-arch_osversion-mod1-mod2 (an underscore at start of "_osversion")
				// to have os-arch-osversion-mod1-mod2 (a dash at start of "-osversion") form.
				// The tag computation below uses this to find both "osversion-mod1" or "mod1".
				tagFriendly = fmt.Sprintf("gotip-%s-%s-", builder.Target.GOOS, builder.Target.GOARCH) + after
			}
			builderName := strings.TrimPrefix(tagFriendly, "gotip-") + "-🐇"
			// Note: goHash is empty for the main Go repo.
			goHash := ""
			if repo != "go" {
				// TODO: Generalize goHash for non-go builds.
				//
				// If the non-go build was triggered by an x/ repo trigger,
				// its b.GetInput().GetGitilesCommit().GetId() will be the
				// x/ repo commit, and the Go repo commit will be selected
				// at runtime...
				//
				// Can get it out of output properties but that would mean
				// not being able to support STARTED builds.
			}
			switch b.Status {
			case bbpb.Status_STARTED:
				if c.BuildingURLs == nil {
					c.BuildingURLs = make(map[builderAndGoHash]string)
				}
				c.BuildingURLs[builderAndGoHash{builderName, goHash}] = buildURL(b.ID)
			case bbpb.Status_SUCCESS, bbpb.Status_FAILURE:
				c.ResultData = append(c.ResultData, fmt.Sprintf("%s|%t|%s|%s",
					builderName,
					b.Status == bbpb.Status_SUCCESS,
					buildURL(b.ID),
					goHash,
				))
			case bbpb.Status_INFRA_FAILURE:
				c.ResultData = append(c.ResultData, fmt.Sprintf("%s|%s|%s|%s",
					builderName,
					"infra_failure",
					buildURL(b.ID),
					goHash,
				))
			}
		}
	}
}

func appendLUCIResultsXRepo(luci lucipoll.Snapshot, c *CommitInfo, repo, goBranch, goHash string) {
	commitBuilds, ok := luci.RepoCommitBuilds[repo]
	if !ok {
		return
	}
	builds, ok := commitBuilds[c.Hash]
	if !ok {
		// No builds for this commit.
		return
	}
	for _, b := range builds {
		builder := luci.Builders[b.BuilderName]
		if builder.Repo != repo || builder.GoBranch != goBranch {
			continue
		}
		switch b.Status {
		case bbpb.Status_STARTED, bbpb.Status_SUCCESS, bbpb.Status_FAILURE, bbpb.Status_INFRA_FAILURE:
		default:
			continue
		}
		// TODO: Maybe dedup builder name calculation with addLUCIBuilders and more places.
		// That said, those few places have slightly different details, so maybe it's fine.
		shortGoBranch := "tip"
		if after, ok := strings.CutPrefix(goBranch, "release-branch.go"); ok {
			shortGoBranch = after
		}
		tagFriendly := builder.Name
		if after, ok := strings.CutPrefix(tagFriendly, fmt.Sprintf("x_%s-go%s-%s-%s_", repo, shortGoBranch, builder.Target.GOOS, builder.Target.GOARCH)); ok {
			// Convert os-arch_osversion-mod1-mod2 (an underscore at start of "_osversion")
			// to have os-arch-osversion-mod1-mod2 (a dash at start of "-osversion") form.
			// The tag computation below uses this to find both "osversion-mod1" or "mod1".
			tagFriendly = fmt.Sprintf("x_%s-go%s-%s-%s-", repo, shortGoBranch, builder.Target.GOOS, builder.Target.GOARCH) + after
		}
		// The builder name is "os-arch{-suffix}-🐇". The "🐇" is used to avoid collisions
		// in builder names between LUCI builders and coordinator builders, and to make it
		// possible to tell LUCI builders apart by their name.
		builderName := strings.TrimPrefix(tagFriendly, fmt.Sprintf("x_%s-go%s-", repo, shortGoBranch)) + "-🐇"
		switch b.Status {
		case bbpb.Status_STARTED:
			if c.BuildingURLs == nil {
				c.BuildingURLs = make(map[builderAndGoHash]string)
			}
			c.BuildingURLs[builderAndGoHash{builderName, goHash}] = buildURL(b.ID)
		case bbpb.Status_SUCCESS, bbpb.Status_FAILURE:
			c.ResultData = append(c.ResultData, fmt.Sprintf("%s|%t|%s|%s",
				builderName,
				b.Status == bbpb.Status_SUCCESS,
				buildURL(b.ID),
				goHash,
			))
		case bbpb.Status_INFRA_FAILURE:
			c.ResultData = append(c.ResultData, fmt.Sprintf("%s|%s|%s|%s",
				builderName,
				"infra_failure",
				buildURL(b.ID),
				goHash,
			))
		}
	}
}

func buildURL(buildID int64) string {
	return fmt.Sprintf("https://ci.chromium.org/b/%d", buildID)
}

func (tb *uiTemplateDataBuilder) buildTemplateData(ctx context.Context, datastoreCl *datastore.Client, luci lucipoll.Snapshot) (*uiTemplateData, error) {
	wantCommits := tb.getCommitsToLoad()
	var dsCommits map[string]*Commit
	if datastoreCl != nil {
		var err error
		dsCommits, err = tb.loadDatastoreCommits(ctx, datastoreCl, wantCommits)
		if err != nil {
			return nil, err
		}
	} else if m := tb.testCommitData; m != nil {
		// Allow tests to fake what the datastore would've loaded, and
		// thus also allow tests to be run without a real (or fake) datastore.
		dsCommits = make(map[string]*Commit)
		for k := range wantCommits {
			if c, ok := m[k.commit]; ok {
				dsCommits[k.commit] = c
			}
		}
	}

	var commits []*CommitInfo
	for _, dc := range tb.res.Commits {
		ci := tb.newCommitInfo(dsCommits, tb.req.Repo, dc)
		commits = append(commits, ci)
	}
	appendLUCIResults(luci, commits, tb.repoGerritProj())

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
				ci := tb.newCommitInfo(dsCommits, path, rh.Commit)
				appendLUCIResultsXRepo(luci, ci, rh.GerritProject, ts.Branch(), gorel.BranchCommit)
				ts.Packages = append(ts.Packages, &PackageState{
					Package: &Package{
						Name: rh.GerritProject,
						Path: path,
					},
					Commit: ci,
				})
			}
			builders := map[string]bool{}
			for _, pkg := range ts.Packages {
				addBuilders(builders, pkg.Package.Name, ts.Branch())
			}
			if len(luci.Builders) > 0 {
				for name := range builders {
					if migration.BuildersPortedToLUCI[name] {
						// Don't display old builders that have been ported
						// to LUCI if willing to show LUCI builders as well.
						delete(builders, name)
					}
				}
			}
			addLUCIBuilders(luci, builders, ts.Packages, gorel.BranchName)
			ts.Builders = builderKeys(builders)

			sort.Slice(ts.Packages, func(i, j int) bool {
				return ts.Packages[i].Package.Name < ts.Packages[j].Package.Name
			})
			xRepoSections = append(xRepoSections, ts)
		}
	}

	gerritProject := "go"
	if repo := repos.ByImportPath[tb.req.Repo]; repo != nil {
		gerritProject = repo.GoGerritProject
	}

	data := &uiTemplateData{
		Dashboard:  goDash,
		Package:    goDash.packageWithPath(tb.req.Repo),
		Commits:    commits,
		TagState:   xRepoSections,
		Pagination: &Pagination{},
		Branches:   tb.res.Branches,
		Branch:     tb.branch(),
		Repo:       gerritProject,
	}

	builders := buildersOfCommits(commits)
	if tb.branch() == "mixed" {
		for _, gr := range tb.res.Releases {
			addBuilders(builders, tb.repoGerritProj(), gr.BranchName)
		}
	} else {
		addBuilders(builders, tb.repoGerritProj(), tb.branch())
	}
	if len(luci.Builders) > 0 {
		for name := range builders {
			if migration.BuildersPortedToLUCI[name] {
				// Don't display old builders that have been ported
				// to LUCI if willing to show LUCI builders as well.
				delete(builders, name)
			}
		}
	}
	data.Builders = builderKeys(builders)

	if tb.res.CommitsTruncated {
		data.Pagination.Next = int(tb.req.Page) + 1
	}
	if tb.req.Page > 0 {
		data.Pagination.Prev = int(tb.req.Page) - 1
		data.Pagination.HasPrev = true
	}

	if tb.view.ShowsActiveBuilds() {
		// Populate building URLs for the HTML UI only.
		data.populateBuildingURLs(ctx, tb.activeBuilds)

		// Appending LUCI active builds can be done here, but it's done
		// earlier in appendLUCIResults instead because it's convenient
		// to do there in the case of LUCI.
	}

	if tb.showLUCI() {
		data.Pagination = &Pagination{} // Disable pagination links because they don't have LUCI results now.
		data.LinkToLUCI = true
	}

	return data, nil
}

// htmlView renders the HTML (default) form of https://build.golang.org/ with no mode parameter.
type htmlView struct {
	// ShowLUCI controls whether to show build results from the LUCI post-submit dashboard
	// in addition to coordinator-backed build results from Datastore.
	//
	// It has no effect if there's no LUCI client.
	ShowLUCI bool
}

func (htmlView) ShowsActiveBuilds() bool { return true }
func (htmlView) ServeDashboard(w http.ResponseWriter, r *http.Request, data *uiTemplateData) {
	var buf bytes.Buffer
	if err := uiTemplate.Execute(&buf, data); err != nil {
		log.Printf("Error: %v", err)
		http.Error(w, "Error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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

	repo := r.FormValue("repo")     // empty for main go repo, else e.g. "golang.org/x/net"
	branch := r.FormValue("branch") // empty means "master"
	maxCommits := commitsPerPage
	if branch == "mixed" {
		maxCommits = 0 // let maintner decide
	}
	return &apipb.DashboardRequest{
		Page:       int32(page),
		Repo:       repo,
		Branch:     branch,
		MaxCommits: int32(maxCommits),
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
//
//	hash builder failure-url
type failuresView struct{}

func (failuresView) ShowsActiveBuilds() bool { return false }
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
	// TODO: this doesn't include the TagState commit. It would be
	// needed if we want to do golang.org/issue/36131, to permit
	// the retrybuilds command to wipe flaky non-go builds.
}

// jsonView renders https://build.golang.org/?mode=json.
// The output is a types.BuildStatus JSON object.
type jsonView struct{}

func (jsonView) ShowsActiveBuilds() bool { return false }
func (jsonView) ServeDashboard(w http.ResponseWriter, r *http.Request, data *uiTemplateData) {
	res := toBuildStatus(r.Host, data)
	v, _ := json.MarshalIndent(res, "", "\t")
	w.Header().Set("Content-Type", "text/json; charset=utf-8")
	w.Write(v)
}

func toBuildStatus(host string, data *uiTemplateData) types.BuildStatus {
	// cell returns one of "" (no data), "ok", or a failure URL.
	cell := func(res *DisplayResult) string {
		switch {
		case res == nil:
			return ""
		case res.OK:
			return "ok"
		}
		return fmt.Sprintf("https://%v/log/%v", host, res.LogHash)
	}

	builders := data.allBuilders()

	var res types.BuildStatus
	res.Builders = builders

	// First the commits from the main section (the requested repo)
	for _, c := range data.Commits {
		// The logic below works for both the go repo and other subrepos: if c is
		// in the main go repo, ResultGoHashes returns a slice of length 1
		// containing the empty string.
		for _, h := range c.ResultGoHashes() {
			rev := types.BuildRevision{
				Repo:       data.Repo,
				Results:    make([]string, len(res.Builders)),
				GoRevision: h,
			}
			commitToBuildRevision(c, &rev)
			for i, b := range res.Builders {
				rev.Results[i] = cell(c.Result(b, h))
			}
			res.Revisions = append(res.Revisions, rev)
		}
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
				Results:    make([]string, len(res.Builders)),
				GoBranch:   goBranch,
			}
			commitToBuildRevision(pkgState.Commit, &rev)
			for i, b := range res.Builders {
				rev.Results[i] = cell(pkgState.Commit.Result(b, goRev))
			}
			res.Revisions = append(res.Revisions, rev)
		}
	}
	return res
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
func fetchCommits(ctx context.Context, cl *datastore.Client, keys []*datastore.Key) ([]*Commit, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([]*Commit, len(keys))
	for i := range keys {
		out[i] = new(Commit)
	}

	err := cl.GetMulti(ctx, keys, out)
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

// buildersOfCommits returns the set of builders that provided
// Results for the provided commits.
func buildersOfCommits(commits []*CommitInfo) map[string]bool {
	m := make(map[string]bool)
	for _, commit := range commits {
		for _, r := range commit.Results() {
			if r.Builder != "" {
				m[r.Builder] = true
			}
		}
	}
	return m
}

// addBuilders adds builders to the provided map that should be active for
// the named Gerrit project & branch. (Issue 19930)
func addBuilders(builders map[string]bool, gerritProj, branch string) {
	for name, bc := range dashboard.Builders {
		if bc.BuildsRepoPostSubmit(gerritProj, branch, branch) {
			builders[name] = true
		}
	}
}

// addLUCIBuilders adds LUCI builders that match the given xRepos and goBranch
// to the provided builders map.
func addLUCIBuilders(luci lucipoll.Snapshot, builders map[string]bool, xRepos []*PackageState, goBranch string) {
	for _, r := range xRepos {
		repoName := r.Package.Name
		for _, b := range luci.Builders {
			if b.Repo != repoName || b.GoBranch != goBranch {
				// Filter out builders whose repo or Go branch doesn't match.
				continue
			}
			shortGoBranch := "tip"
			if after, ok := strings.CutPrefix(b.GoBranch, "release-branch.go"); ok {
				shortGoBranch = after
			}
			tagFriendly := b.Name
			if after, ok := strings.CutPrefix(tagFriendly, fmt.Sprintf("x_%s-go%s-%s-%s_", b.Repo, shortGoBranch, b.Target.GOOS, b.Target.GOARCH)); ok {
				// Convert os-arch_osversion-mod1-mod2 (an underscore at start of "_osversion")
				// to have os-arch-osversion-mod1-mod2 (a dash at start of "-osversion") form.
				// The tag computation below uses this to find both "osversion-mod1" or "mod1".
				tagFriendly = fmt.Sprintf("x_%s-go%s-%s-%s-", b.Repo, shortGoBranch, b.Target.GOOS, b.Target.GOARCH) + after
			}
			// The builder name is "os-arch{-suffix}-🐇". The "🐇" is used to avoid collisions
			// in builder names between LUCI builders and coordinator builders, and to make to
			// possible to tell LUCI builders apart by their name.
			builderName := strings.TrimPrefix(tagFriendly, fmt.Sprintf("x_%s-go%s-", b.Repo, shortGoBranch)) + "-🐇"
			builders[builderName] = true
		}
	}
}

func builderKeys(m map[string]bool) (s []string) {
	s = make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	sort.Sort(builderOrder(s))
	return
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
	return !releasetargets.IsFirstClass(builderOS(builder), builderArch(builder))
}

// osPriority encodes priorities for specific operating systems.
var osPriority = map[string]int{
	"all":    0,
	"darwin": 1, "linux": 1, "windows": 1,
	// race == 2
	"freebsd": 3, "openbsd": 3, "netbsd": 3,
	"android": 4, "ios": 4,
}

// TagState represents the state of all Packages at a branch.
type TagState struct {
	Name     string      // Go branch name: "master", "release-branch.go1.4", etc.
	Tag      *CommitInfo // current Go commit on the Name branch
	Packages []*PackageState
	Builders []string
}

// Branch returns the git branch name, converting from the old
// terminology we used from Go's hg days into git terminology.
func (ts *TagState) Branch() string {
	if ts.Name == "tip" {
		return "master"
	}
	return ts.Name
}

// LUCIBranch returns the short Go branch name as used in LUCI: "tip", "1.24", etc.
func (ts *TagState) LUCIBranch() string {
	shortGoBranch := "tip"
	if after, ok := strings.CutPrefix(ts.Name, "release-branch.go"); ok {
		shortGoBranch = after
	}
	return shortGoBranch
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

	// ResultData is a copy of the [Commit.ResultData] field from datastore,
	// with an additional rule that the second '|'-separated value may be "infra_failure"
	// to indicate a problem with the infrastructure rather than the code being tested.
	ResultData []string

	// BuildingURLs contains the status URL values for builds that
	// are currently in progress for this commit.
	BuildingURLs map[builderAndGoHash]string

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
	Builders   []string    // builders for just the main section; not the "TagState" sections
	TagState   []*TagState // x/foo repo overviews at master + last two releases
	Pagination *Pagination
	Branches   []string
	Branch     string
	Repo       string // the repo gerrit project name. "go" if unspecified in the request.
	LinkToLUCI bool   // whether to display links to LUCI UI
}

// getActiveBuilds returns the builds that coordinator is currently doing.
// This isn't critical functionality so errors are logged but otherwise ignored for now.
// Once this is merged into the coordinator we won't need to make an RPC to get
// this info. See https://github.com/golang/go/issues/34744#issuecomment-563398753.
func getActiveBuilds(ctx context.Context) (builds []types.ActivePostSubmitBuild) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequest("GET", "https://farmer.golang.org/status/post-submit-active.json", nil)
	req = req.WithContext(ctx)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("getActiveBuilds: Do: %v", err)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Printf("getActiveBuilds: %v", res.Status)
		return
	}
	if err := json.NewDecoder(res.Body).Decode(&builds); err != nil {
		log.Printf("getActiveBuilds: JSON decode: %v", err)
	}
	return builds
}

// populateBuildingURLs populates each commit in Commits' buildingURLs map with the
// URLs of builds which are currently in progress.
func (td *uiTemplateData) populateBuildingURLs(ctx context.Context, activeBuilds []types.ActivePostSubmitBuild) {
	// active maps from a build record with its status URL zeroed
	// out to the actual value of that status URL.
	active := map[types.ActivePostSubmitBuild]string{}
	for _, rec := range activeBuilds {
		statusURL := rec.StatusURL
		rec.StatusURL = ""
		active[rec] = statusURL
	}

	condAdd := func(c *CommitInfo, rec types.ActivePostSubmitBuild) {
		su, ok := active[rec]
		if !ok {
			return
		}
		if c.BuildingURLs == nil {
			c.BuildingURLs = make(map[builderAndGoHash]string)
		}
		c.BuildingURLs[builderAndGoHash{rec.Builder, rec.GoCommit}] = su
	}

	for _, b := range td.Builders {
		for _, c := range td.Commits {
			condAdd(c, types.ActivePostSubmitBuild{Builder: b, Commit: c.Hash})
		}
	}

	// Gather pending commits for sub-repos.
	for _, ts := range td.TagState {
		goHash := ts.Tag.Hash
		for _, b := range td.Builders {
			for _, pkg := range ts.Packages {
				c := pkg.Commit
				condAdd(c, types.ActivePostSubmitBuild{
					Builder:  b,
					Commit:   c.Hash,
					GoCommit: goHash,
				})
			}
		}
	}
}

// allBuilders returns the list of builders, unified over the main
// section and any x/foo branch overview (TagState) sections.
func (td *uiTemplateData) allBuilders() []string {
	m := map[string]bool{}
	for _, b := range td.Builders {
		m[b] = true
	}
	for _, ts := range td.TagState {
		for _, b := range ts.Builders {
			m[b] = true
		}
	}
	return builderKeys(m)
}

var uiTemplate = template.Must(
	template.New("ui.html").Funcs(tmplFuncs).Parse(uiHTML),
)

//go:embed ui.html
var uiHTML string

var tmplFuncs = template.FuncMap{
	"builderSpans":       builderSpans,
	"builderSubheading":  builderSubheading,
	"builderSubheading2": builderSubheading2,
	"shortDesc":          shortDesc,
	"shortHash":          shortHash,
	"shortUser":          shortUser,
	"unsupported":        unsupported,
	"isUntested":         isUntested,
	"knownIssue":         knownIssue,
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
		// Remove "race" and just take whatever the third component is after.
		var split []string
		for _, sc := range strings.Split(s, "-") {
			if sc == "race" {
				continue
			}
			split = append(split, sc)
		}
		s = strings.Join(split, "-")
	}
	_, secondThird := splitDash(s)
	_, third := splitDash(secondThird)
	return third
}

type builderSpan struct {
	N           int // Total number of builders.
	FirstN      int // Number of builders for a first-class port.
	OS          string
	Unsupported bool // Unsupported means the entire span has no builders for a first-class port.
}

// builderSpans creates a list of tags showing
// the builder's operating system names, spanning
// the appropriate number of columns.
func builderSpans(s []string) []builderSpan {
	var sp []builderSpan
	for len(s) > 0 {
		i := 1
		os := builderOSOrRace(s[0])
		for i < len(s) && builderOSOrRace(s[i]) == os {
			i++
		}
		var f int // First-class ports.
		for _, b := range s[:i] {
			if unsupported(b) {
				continue
			}
			f++
		}
		sp = append(sp, builderSpan{i, f, os, f == 0})
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

func httpStatusOfErr(err error) int {
	switch grpc.Code(err) {
	case codes.NotFound:
		return http.StatusNotFound
	case codes.InvalidArgument:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
