// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/releasetargets"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/types"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
	"golang.org/x/net/context/ctxhttp"
)

type TagXReposTasks struct {
	IgnoreProjects   map[string]bool // project name -> ignore
	Gerrit           GerritClient
	GerritURL        string
	CreateBuildlet   func(context.Context, string) (buildlet.RemoteClient, error)
	LatestGoBinaries func(context.Context) (string, error)
	DashboardURL     string
}

func (x *TagXReposTasks) NewDefinition() *wf.Definition {
	wd := wf.New()
	reviewers := wf.Param(wd, reviewersParam)
	repos := wf.Task0(wd, "Select repositories", x.SelectRepos)
	wf.Expand2(wd, "Create plan", x.BuildPlan, repos, reviewers)
	return wd
}

func (x *TagXReposTasks) NewSingleDefinition() *wf.Definition {
	wd := wf.New()
	reviewers := wf.Param(wd, reviewersParam)
	repos := wf.Task0(wd, "Load all repositories", x.SelectRepos)
	name := wf.Param(wd, wf.ParamDef[string]{Name: "Repository name", Example: "tools"})
	wf.Expand3(wd, "Create single-repo plan", x.BuildSingleRepoPlan, repos, name, reviewers)
	return wd
}

var reviewersParam = wf.ParamDef[[]string]{
	Name:      "Reviewer usernames (optional)",
	ParamType: wf.SliceShort,
	Doc:       `Send code reviews to these users.`,
	Example:   "heschi",
}

// TagRepo contains information about a repo that can be tagged.
type TagRepo struct {
	Name         string   // Gerrit project name, e.g. "tools".
	ModPath      string   // Module path, e.g. "golang.org/x/tools".
	Deps         []string // Dependency module paths.
	Compat       string   // The Go version to pass to go mod tidy -compat for this repository.
	StartVersion string   // The version of the module when the workflow started.
	Version      string   // After a tagging decision has been made, the version dependencies should upgrade to.
}

func (x *TagXReposTasks) SelectRepos(ctx *wf.TaskContext) ([]TagRepo, error) {
	projects, err := x.Gerrit.ListProjects(ctx)
	if err != nil {
		return nil, err
	}

	ctx.Printf("Examining repositories %v", projects)
	var repos []TagRepo
	for _, p := range projects {
		if x.IgnoreProjects[p] {
			ctx.Printf("Repository %v ignored", p)
			continue
		}
		repo, err := x.readRepo(ctx, p)
		if err != nil {
			return nil, err
		}
		if repo != nil {
			repos = append(repos, *repo)
		}
	}

	if cycles := checkCycles(repos); len(cycles) != 0 {
		return nil, fmt.Errorf("cycles detected (there may be more): %v", cycles)
	}

	return repos, nil
}

func (x *TagXReposTasks) readRepo(ctx *wf.TaskContext, project string) (*TagRepo, error) {
	head, err := x.Gerrit.ReadBranchHead(ctx, project, "master")
	if errors.Is(err, gerrit.ErrResourceNotExist) {
		ctx.Printf("ignoring %v: no master branch: %v", project, err)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	tag, err := x.latestReleaseTag(ctx, project)
	if err != nil {
		return nil, err
	}
	if tag == "" {
		ctx.Printf("ignoring %v: no semver tag", project)
		return nil, nil
	}

	gomod, err := x.Gerrit.ReadFile(ctx, project, head, "go.mod")
	if errors.Is(err, gerrit.ErrResourceNotExist) {
		ctx.Printf("ignoring %v: no go.mod: %v", project, err)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mf, err := modfile.ParseLax("go.mod", gomod, nil)
	if err != nil {
		return nil, err
	}

	// TODO(heschi): ignoring nested modules for now. We should find and handle
	// x/exp/event, maybe by reading release tags? But don't tag gopls...
	isX := func(path string) bool {
		return strings.HasPrefix(path, "golang.org/x/") &&
			!strings.Contains(strings.TrimPrefix(path, "golang.org/x/"), "/")
	}
	if !isX(mf.Module.Mod.Path) {
		ctx.Printf("ignoring %v: not golang.org/x", project)
		return nil, nil
	}

	result := &TagRepo{
		Name:         project,
		ModPath:      mf.Module.Mod.Path,
		StartVersion: tag,
	}

	compatRe := regexp.MustCompile(`tagx:compat\s+([\d.]+)`)
	if mf.Go != nil {
		for _, c := range mf.Go.Syntax.Comments.Suffix {
			if matches := compatRe.FindStringSubmatch(c.Token); matches != nil {
				result.Compat = matches[1]
			}
		}
	}
require:
	for _, req := range mf.Require {
		if !isX(req.Mod.Path) {
			continue
		}
		for _, c := range req.Syntax.Comments.Suffix {
			// We have cycles in the x repo dependency graph. Allow a magic
			// comment, `// tagx:ignore`, to exclude requirements from
			// consideration.
			if strings.Contains(c.Token, "tagx:ignore") {
				ctx.Printf("ignoring %v's requirement on %v: %q", project, req.Mod, c.Token)
				continue require
			}
		}
		result.Deps = append(result.Deps, req.Mod.Path)

	}
	return result, nil
}

// checkCycles returns all the shortest dependency cycles in repos.
func checkCycles(repos []TagRepo) [][]string {
	reposByModule := map[string]TagRepo{}
	for _, repo := range repos {
		reposByModule[repo.ModPath] = repo
	}

	var cycles [][]string

	for _, repo := range reposByModule {
		cycles = append(cycles, checkCycles1(reposByModule, repo, nil)...)
	}

	var shortestCycles [][]string
	for _, cycle := range cycles {
		switch {
		case len(shortestCycles) == 0 || len(shortestCycles[0]) > len(cycle):
			shortestCycles = [][]string{cycle}
		case len(shortestCycles[0]) == len(cycle):
			found := false
			for _, existing := range shortestCycles {
				if reflect.DeepEqual(existing, cycle) {
					found = true
					break
				}
			}
			if !found {
				shortestCycles = append(shortestCycles, cycle)
			}
		}
	}
	return shortestCycles
}

func checkCycles1(reposByModule map[string]TagRepo, repo TagRepo, stack []string) [][]string {
	var cycles [][]string
	stack = append(stack, repo.ModPath)
	for i, s := range stack[:len(stack)-1] {
		if s == repo.ModPath {
			cycles = append(cycles, append([]string(nil), stack[i:]...))
		}
	}
	if len(cycles) != 0 {
		return cycles
	}

	for _, dep := range repo.Deps {
		cycles = append(cycles, checkCycles1(reposByModule, reposByModule[dep], stack)...)
	}
	return cycles
}

// BuildPlan adds the tasks needed to update repos to wd.
func (x *TagXReposTasks) BuildPlan(wd *wf.Definition, repos []TagRepo, reviewers []string) error {
	// repo.ModPath to the wf.Value produced by updating it.
	updated := map[string]wf.Value[TagRepo]{}

	// Find all repositories whose dependencies are satisfied and update
	// them, proceeding until all are updated or no progress can be made.
	for len(updated) != len(repos) {
		progress := false
		for _, repo := range repos {
			if _, ok := updated[repo.ModPath]; ok {
				continue
			}
			dep, ok := x.planRepo(wd, repo, updated, reviewers)
			if !ok {
				continue
			}
			updated[repo.ModPath] = dep
			progress = true
		}

		if !progress {
			var missing []string
			for _, r := range repos {
				if updated[r.ModPath] == nil {
					missing = append(missing, r.Name)
				}
			}
			return fmt.Errorf("failed to progress the plan: todo: %v", missing)
		}
	}
	var allDeps []wf.Dependency
	for _, dep := range updated {
		allDeps = append(allDeps, dep)
	}
	done := wf.Task0(wd, "done", func(_ context.Context) (string, error) { return "done!", nil }, wf.After(allDeps...))
	wf.Output(wd, "done", done)
	return nil
}

func (x *TagXReposTasks) BuildSingleRepoPlan(wd *wf.Definition, repoSlice []TagRepo, name string, reviewers []string) error {
	repos := map[string]TagRepo{}
	updatedRepos := map[string]wf.Value[TagRepo]{}
	for _, r := range repoSlice {
		repos[r.Name] = r

		// Pretend that we've just tagged version that was live when we started.
		r.Version = r.StartVersion
		updatedRepos[r.ModPath] = wf.Const(r)
	}
	repo, ok := repos[name]
	if !ok {
		return fmt.Errorf("no repository %q", name)
	}
	tagged, ok := x.planRepo(wd, repo, updatedRepos, reviewers)
	if !ok {
		return fmt.Errorf("%q doesn't have all of its dependencies (%q)", repo.Name, repo.Deps)
	}
	wf.Output(wd, "tagged repository", tagged)
	return nil
}

// planRepo adds tasks to wf to update and tag repo. It returns a Value
// containing the tagged repository's information, or nil, false if its
// dependencies haven't been planned yet.
func (x *TagXReposTasks) planRepo(wd *wf.Definition, repo TagRepo, updated map[string]wf.Value[TagRepo], reviewers []string) (_ wf.Value[TagRepo], ready bool) {
	var deps []wf.Value[TagRepo]
	for _, repoDeps := range repo.Deps {
		if dep, ok := updated[repoDeps]; ok {
			deps = append(deps, dep)
		} else {
			return nil, false
		}
	}
	wd = wd.Sub(repo.Name)
	repoName, branch := wf.Const(repo.Name), wf.Const("master")

	var tagCommit wf.Value[string]
	if len(deps) == 0 {
		tagCommit = wf.Task2(wd, "read branch head", x.Gerrit.ReadBranchHead, repoName, branch)
	} else {
		gomod := wf.Task3(wd, "generate updated go.mod", x.UpdateGoMod, wf.Const(repo), wf.Slice(deps...), branch)
		cl := wf.Task3(wd, "mail updated go.mod", x.MailGoMod, repoName, gomod, wf.Const(reviewers))
		tagCommit = wf.Task3(wd, "wait for submit", x.AwaitGoMod, cl, repoName, branch)
	}
	greenCommit := wf.Task2(wd, "wait for green post-submit", x.AwaitGreen, wf.Const(repo), tagCommit)
	tagged := wf.Task2(wd, "tag if appropriate", x.MaybeTag, wf.Const(repo), greenCommit)
	return tagged, true
}

func (x *TagXReposTasks) UpdateGoMod(ctx *wf.TaskContext, repo TagRepo, deps []TagRepo, branch string) (files map[string]string, _ error) {
	commit, err := x.Gerrit.ReadBranchHead(ctx, repo.Name, branch)
	if err != nil {
		return nil, err
	}

	binaries, err := x.LatestGoBinaries(ctx)
	if err != nil {
		return nil, err
	}
	bc, err := x.CreateBuildlet(ctx, "linux-amd64-longtest") // longtest to allow network access. A little yucky.
	if err != nil {
		return nil, err
	}
	defer bc.Close()

	if err := bc.PutTarFromURL(ctx, binaries, ""); err != nil {
		return nil, err
	}
	tarURL := fmt.Sprintf("%s/%s/+archive/%s.tar.gz", x.GerritURL, repo.Name, commit)
	if err := bc.PutTarFromURL(ctx, tarURL, "repo"); err != nil {
		return nil, err
	}

	writer := &LogWriter{Logger: ctx}
	go writer.Run(ctx)

	// Update the root module to the selected versions.
	getCmd := []string{"get"}
	for _, dep := range deps {
		getCmd = append(getCmd, dep.ModPath+"@"+dep.Version)
	}
	remoteErr, execErr := bc.Exec(ctx, "go/bin/go", buildlet.ExecOpts{
		Dir:    "repo",
		Args:   getCmd,
		Output: writer,
	})
	if execErr != nil {
		return nil, execErr
	}
	if remoteErr != nil {
		return nil, fmt.Errorf("Command failed: %v", remoteErr)
	}

	// Tidy the root module. For tools, also tidy gopls so that its replaced
	// version still works.
	dirs := []string{""}
	if repo.Name == "tools" {
		dirs = append(dirs, "gopls")
	}
	var fetchCmd []string
	for _, dir := range dirs {
		var tidyFlags []string
		if repo.Compat != "" {
			tidyFlags = append(tidyFlags, "-compat", repo.Compat)
		}
		remoteErr, execErr = bc.Exec(ctx, "go/bin/go", buildlet.ExecOpts{
			Dir:    path.Join("repo", dir),
			Args:   append([]string{"mod", "tidy"}, tidyFlags...),
			Output: writer,
		})
		if execErr != nil {
			return nil, execErr
		}
		if remoteErr != nil {
			return nil, fmt.Errorf("Command failed: %v", remoteErr)
		}

		repoDir, fetchDir := path.Join("repo", dir), path.Join("fetch", dir)
		fetchCmd = append(fetchCmd, fmt.Sprintf("mkdir -p %[2]v && cp %[1]v/go.mod %[2]v && touch %[1]v/go.sum && cp %[1]v/go.sum %[2]v", repoDir, fetchDir))
	}

	remoteErr, execErr = bc.Exec(ctx, "bash", buildlet.ExecOpts{
		Dir:         ".",
		Args:        []string{"-c", strings.Join(fetchCmd, " && ")},
		Output:      writer,
		SystemLevel: true,
	})
	if execErr != nil {
		return nil, execErr
	}
	if remoteErr != nil {
		return nil, fmt.Errorf("Command failed: %v", remoteErr)
	}

	tgz, err := bc.GetTar(ctx, "fetch")
	if err != nil {
		return nil, err
	}
	defer tgz.Close()
	return tgzToMap(tgz)
}

func tgzToMap(r io.Reader) (map[string]string, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gzr.Close()

	result := map[string]string{}
	tr := tar.NewReader(gzr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		result[h.Name] = string(b)
	}
	return result, nil
}

// LatestGoBinaries returns a URL to the latest linux/amd64
// Go distribution archive using the go.dev/dl/ JSON API.
func LatestGoBinaries(ctx context.Context) (string, error) {
	resp, err := ctxhttp.Get(ctx, http.DefaultClient, "https://go.dev/dl/?mode=json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %v", resp.Status)
	}

	releases := []*WebsiteRelease{}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", err
	}
	for _, r := range releases {
		for _, f := range r.Files {
			if f.Arch == "amd64" && f.OS == "linux" && f.Kind == "archive" {
				return "https://go.dev/dl/" + f.Filename, nil
			}
		}
	}
	return "", fmt.Errorf("no linux-amd64??")
}

func (x *TagXReposTasks) MailGoMod(ctx *wf.TaskContext, repo string, files map[string]string, reviewers []string) (string, error) {
	const subject = `go.mod: update golang.org/x dependencies

Update golang.org/x dependencies to their latest tagged versions.
Once this CL is submitted, and post-submit testing succeeds on all
first-class ports across all supported Go versions, this repository
will be tagged with its next minor version.
`
	return x.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: repo,
		Branch:  "master",
		Subject: subject,
	}, reviewers, files)
}

func (x *TagXReposTasks) AwaitGoMod(ctx *wf.TaskContext, changeID, repo, branch string) (string, error) {
	if changeID == "" {
		ctx.Printf("No CL was necessary")
		return x.Gerrit.ReadBranchHead(ctx, repo, branch)
	}

	ctx.Printf("Awaiting review/submit of %v", ChangeLink(changeID))
	return AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return x.Gerrit.Submitted(ctx, changeID, "")
	})
}

func (x *TagXReposTasks) AwaitGreen(ctx *wf.TaskContext, repo TagRepo, commit string) (string, error) {
	return AwaitCondition(ctx, time.Minute, func() (string, bool, error) {
		return x.findGreen(ctx, repo, commit, false)
	})
}

func (x *TagXReposTasks) findGreen(ctx *wf.TaskContext, repo TagRepo, commit string, verbose bool) (string, bool, error) {
	// Read the front status page to discover live Go release branches.
	frontStatus, err := x.getBuildStatus("")
	if err != nil {
		return "", false, fmt.Errorf("reading dashboard front page: %v", err)
	}
	branchSet := map[string]bool{}
	for _, rev := range frontStatus.Revisions {
		if rev.GoBranch != "" {
			branchSet[rev.GoBranch] = true
		}
	}
	var refs []string
	for b := range branchSet {
		refs = append(refs, "refs/heads/"+b)
	}

	// Read the page for the given repo to get the list of commits and statuses.
	repoStatus, err := x.getBuildStatus(repo.ModPath)
	if err != nil {
		return "", false, fmt.Errorf("reading dashboard for %q: %v", repo.ModPath, err)
	}
	// Some slow-moving repos have years of Go history. Throw away old stuff.
	firstRev := repoStatus.Revisions[0].Revision
	for i, rev := range repoStatus.Revisions {
		ts, err := time.Parse(time.RFC3339, rev.Date)
		if err != nil {
			return "", false, fmt.Errorf("parsing date of rev %#v: %v", rev, err)
		}
		if i == 200 || (rev.Revision != firstRev && ts.Add(6*7*24*time.Hour).Before(time.Now())) {
			repoStatus.Revisions = repoStatus.Revisions[:i]
			break
		}
	}

	// Associate Go revisions with branches.
	var goCommits []string
	for _, rev := range repoStatus.Revisions {
		goCommits = append(goCommits, rev.GoRevision)
	}
	commitsInRefs, err := x.Gerrit.GetCommitsInRefs(ctx, "go", goCommits, refs)
	if err != nil {
		return "", false, err
	}

	// Determine which builders have to pass to consider a CL green.
	firstClass := releasetargets.LatestFirstClassPorts()
	required := map[string][]bool{}
	for goBranch := range branchSet {
		required[goBranch] = make([]bool, len(repoStatus.Builders))
		for i, b := range repoStatus.Builders {
			cfg, ok := dashboard.Builders[b]
			if !ok {
				continue
			}
			runs := cfg.BuildsRepoPostSubmit(repo.Name, "master", goBranch)
			ki := len(cfg.KnownIssues) != 0
			fc := firstClass[releasetargets.OSArch{OS: cfg.GOOS(), Arch: cfg.GOARCH()}]
			google := cfg.HostConfig().IsGoogle()
			required[goBranch][i] = runs && !ki && fc && google
		}
	}

	foundCommit := false
	for _, rev := range repoStatus.Revisions {
		if rev.Revision == commit {
			foundCommit = true
			break
		}
	}
	if !foundCommit {
		ctx.Printf("commit %v not found on first page of results; too old or too new?", commit)
		return "", false, nil
	}

	// x/ repo statuses are:
	// <x commit> <go commit>
	//            <go commit>
	//            <go commit>
	// Process one x/ commit at a time, looking for green go commits from
	// each release branch.
	var currentRevision, earliestGreen string
	greenOnBranches := map[string]bool{}
	for i := 0; i <= len(repoStatus.Revisions); i++ {
		if currentRevision != "" && (i == len(repoStatus.Revisions) || repoStatus.Revisions[i].Revision != currentRevision) {
			// Finished an x/ commit.
			if verbose {
				ctx.Printf("rev %v green = %v", currentRevision, len(greenOnBranches) == len(branchSet))
			}
			if len(greenOnBranches) == len(branchSet) {
				// All the branches were green, so this is a candidate.
				earliestGreen = currentRevision
			}
			if currentRevision == commit {
				// We've passed the desired commit. Stop.
				break
			}
			greenOnBranches = map[string]bool{}
		}
		if i == len(repoStatus.Revisions) {
			break
		}

		rev := repoStatus.Revisions[i]
		currentRevision = rev.Revision

		for _, ref := range commitsInRefs[rev.GoRevision] {
			branch := strings.TrimPrefix(ref, "refs/heads/")
			allOK := true
			var missing []string
			for i, result := range rev.Results {
				ok := result == "ok" || !required[branch][i]
				if !ok {
					missing = append(missing, repoStatus.Builders[i])
				}
				allOK = allOK && ok
			}
			if allOK {
				greenOnBranches[branch] = true
			}
			if verbose {
				ctx.Printf("branch %v at %v: green = %v (missing: %v)", branch, rev.GoRevision, allOK, missing)
			}
		}
	}
	return earliestGreen, earliestGreen != "", nil
}

func (x *TagXReposTasks) getBuildStatus(modPath string) (*types.BuildStatus, error) {
	resp, err := http.Get(x.DashboardURL + "/?mode=json&repo=" + url.QueryEscape(modPath))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %v", resp.Status)
	}
	status := &types.BuildStatus{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return status, nil
}

// MaybeTag tags repo at commit with the next version, unless commit is already
// the latest tagged version. repo is returned with Version populated.
func (x *TagXReposTasks) MaybeTag(ctx *wf.TaskContext, repo TagRepo, commit string) (TagRepo, error) {
	highestRelease, err := x.latestReleaseTag(ctx, repo.Name)
	if err != nil {
		return TagRepo{}, err
	}

	if highestRelease == "" {
		return TagRepo{}, fmt.Errorf("no semver tags found in %v", repo.Name)
	}
	tagInfo, err := x.Gerrit.GetTag(ctx, repo.Name, highestRelease)
	if err != nil {
		return TagRepo{}, fmt.Errorf("reading project %v tag %v: %v", repo.Name, highestRelease, err)
	}
	if tagInfo.Revision == commit {
		repo.Version = highestRelease
		return repo, nil
	}
	repo.Version, err = nextMinor(highestRelease)
	if err != nil {
		return TagRepo{}, fmt.Errorf("couldn't pick next version for %v: %v", repo.Name, err)
	}

	ctx.Printf("Tagging %v at %v as %v", repo.Name, commit, repo.Version)
	return repo, x.Gerrit.Tag(ctx, repo.Name, repo.Version, commit)
}

func (x *TagXReposTasks) latestReleaseTag(ctx context.Context, repo string) (string, error) {
	tags, err := x.Gerrit.ListTags(ctx, repo)
	if err != nil {
		return "", err
	}
	highestRelease := ""
	for _, tag := range tags {
		if semver.IsValid(tag) && semver.Prerelease(tag) == "" &&
			(highestRelease == "" || semver.Compare(highestRelease, tag) < 0) {
			highestRelease = tag
		}
	}
	return highestRelease, nil
}

var majorMinorRestRe = regexp.MustCompile(`^v(\d+)\.(\d+)\..*$`)

func nextMinor(version string) (string, error) {
	parts := majorMinorRestRe.FindStringSubmatch(version)
	if parts == nil {
		return "", fmt.Errorf("malformatted version %q", version)
	}
	minor, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", fmt.Errorf("malformatted version %q (%v)", version, err)
	}
	return fmt.Sprintf("v%s.%d.0", parts[1], minor+1), nil
}
