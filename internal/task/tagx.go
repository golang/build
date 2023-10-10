// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/releasetargets"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/types"
	"golang.org/x/exp/slices"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

type TagXReposTasks struct {
	IgnoreProjects map[string]bool // project name -> ignore
	Gerrit         GerritClient
	DashboardURL   string
	CloudBuild     CloudBuildClient
}

func (x *TagXReposTasks) NewDefinition() *wf.Definition {
	wd := wf.New()
	reviewers := wf.Param(wd, reviewersParam)
	repos := wf.Task0(wd, "Select repositories", x.SelectRepos)
	done := wf.Expand2(wd, "Create plan", x.BuildPlan, repos, reviewers)
	wf.Output(wd, "done", done)
	return wd
}

func (x *TagXReposTasks) NewSingleDefinition() *wf.Definition {
	wd := wf.New()
	reviewers := wf.Param(wd, reviewersParam)
	repos := wf.Task0(wd, "Load all repositories", x.SelectRepos)
	name := wf.Param(wd, wf.ParamDef[string]{Name: "Repository name", Example: "tools"})
	// TODO: optional is required to avoid the "required" check, but since it's a checkbox
	// it's obviously yes/no, should probably be exempted from that check.
	skipPostSubmit := wf.Param(wd, wf.ParamDef[bool]{Name: "Skip post submit result (optional)", ParamType: wf.Bool})
	tagged := wf.Expand4(wd, "Create single-repo plan", x.BuildSingleRepoPlan, repos, name, skipPostSubmit, reviewers)
	wf.Output(wd, "tagged repository", tagged)
	return wd
}

var reviewersParam = wf.ParamDef[[]string]{
	Name:      "Reviewer usernames (optional)",
	ParamType: wf.SliceShort,
	Doc:       `Send code reviews to these users.`,
	Example:   "heschi",
	Check:     CheckCoordinators,
}

// TagRepo contains information about a repo that can be updated and possibly tagged.
type TagRepo struct {
	Name         string    // Gerrit project name, e.g., "tools".
	ModPath      string    // Module path, e.g., "golang.org/x/tools".
	Deps         []*TagDep // Dependency modules.
	Compat       string    // The Go version to pass to go mod tidy -compat for this repository.
	StartVersion string    // The version of the module when the workflow started. Empty string means repo hasn't begun release version tagging yet.
	NewerVersion string    // The version of the module that will be tagged, or the empty string when the repo is being updated only and not tagged.
}

// UpdateOnlyAndNotTag reports whether repo
// r should be updated only, and not tagged.
func (r TagRepo) UpdateOnlyAndNotTag() bool {
	// Consider a repo without an existing tag as one
	// that hasn't yet opted in for automatic tagging.
	return r.StartVersion == ""
}

// TagDep represents a dependency of a repo being updated and possibly tagged.
type TagDep struct {
	ModPath string // Module path, e.g., "golang.org/x/sys".
	Wait    bool   // Wait controls whether to wait for this dependency to be processed first.
}

func (x *TagXReposTasks) SelectRepos(ctx *wf.TaskContext) ([]TagRepo, error) {
	projects, err := x.Gerrit.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	projects = slices.DeleteFunc(projects, func(proj string) bool { return proj == "go" })

	// Read the starting state for all relevant repos.
	ctx.Printf("Examining repositories %v", projects)
	var repos []TagRepo
	var updateOnly = make(map[string]bool) // Key is module path.
	for _, p := range projects {
		r, err := x.readRepo(ctx, p)
		if err != nil {
			return nil, err
		} else if r == nil {
			continue
		}
		repos = append(repos, *r)
		updateOnly[r.ModPath] = r.UpdateOnlyAndNotTag()
	}
	// Now that we know all repos and their deps,
	// do a second pass to update the Wait field.
	for _, r := range repos {
		for _, dep := range r.Deps {
			if updateOnly[dep.ModPath] {
				// No need to wait for repos that we don't plan to tag.
				dep.Wait = false
			}
		}
	}

	// Check for cycles.
	var cycleProneRepos []TagRepo
	for _, r := range repos {
		if r.UpdateOnlyAndNotTag() {
			// Cycles in repos we don't plan to tag don't matter.
			continue
		}
		cycleProneRepos = append(cycleProneRepos, r)
	}
	if cycles := checkCycles(cycleProneRepos); len(cycles) != 0 {
		return nil, fmt.Errorf("cycles detected (there may be more): %v", cycles)
	}

	return repos, nil
}

// readRepo fetches and returns information about the named project
// to be updated and possibly tagged, or nil if the project doesn't
// satisfy some criteria needed to be eligible.
func (x *TagXReposTasks) readRepo(ctx *wf.TaskContext, project string) (*TagRepo, error) {
	if project == "go" {
		return nil, fmt.Errorf("readRepo: refusing to read the main Go repository, it's out of scope in the context of TagXReposTasks")
	} else if x.IgnoreProjects[project] {
		ctx.Printf("ignoring %v: marked as ignored", project)
		return nil, nil
	}

	head, err := x.Gerrit.ReadBranchHead(ctx, project, "master")
	if errors.Is(err, gerrit.ErrResourceNotExist) {
		ctx.Printf("ignoring %v: no master branch: %v", project, err)
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	goMod, err := x.Gerrit.ReadFile(ctx, project, head, "go.mod")
	if errors.Is(err, gerrit.ErrResourceNotExist) {
		ctx.Printf("ignoring %v: no go.mod: %v", project, err)
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	mf, err := modfile.ParseLax("go.mod", goMod, nil)
	if err != nil {
		return nil, err
	}

	// TODO(heschi): ignoring nested modules for now. We should find and handle
	// x/exp/event, maybe by reading release tags? But don't tag gopls...
	isXRoot := func(path string) bool {
		return strings.HasPrefix(path, "golang.org/x/") &&
			!strings.Contains(strings.TrimPrefix(path, "golang.org/x/"), "/")
	}
	if !isXRoot(mf.Module.Mod.Path) {
		ctx.Printf("ignoring %v: not golang.org/x", project)
		return nil, nil
	}

	currentTag, _, err := x.latestReleaseTag(ctx, project, "")
	if err != nil {
		return nil, err
	}

	result := &TagRepo{
		Name:         project,
		ModPath:      mf.Module.Mod.Path,
		StartVersion: currentTag,
	}

	compatRe := regexp.MustCompile(`tagx:compat\s+([\d.]+)`)
	if mf.Go != nil {
		for _, c := range mf.Go.Syntax.Comments.Suffix {
			if matches := compatRe.FindStringSubmatch(c.Token); matches != nil {
				result.Compat = matches[1]
			}
		}
	}
	for _, req := range mf.Require {
		if !isXRoot(req.Mod.Path) {
			continue
		} else if x.IgnoreProjects[strings.TrimPrefix(req.Mod.Path, "golang.org/x/")] {
			ctx.Printf("Dependency %v is ignored", req.Mod.Path)
			continue
		}
		wait := true
		for _, c := range req.Syntax.Comments.Suffix {
			// We have cycles in the x repo dependency graph. Allow a magic
			// comment, `// tagx:ignore`, to exclude requirements from
			// consideration.
			if strings.Contains(c.Token, "tagx:ignore") {
				ctx.Printf("ignoring %v's requirement on %v: %q", project, req.Mod, c.Token)
				wait = false
			}
		}
		result.Deps = append(result.Deps, &TagDep{
			ModPath: req.Mod.Path,
			Wait:    wait,
		})
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
		if !dep.Wait {
			// Deps we don't wait for don't matter for cycles.
			continue
		}
		cycles = append(cycles, checkCycles1(reposByModule, reposByModule[dep.ModPath], stack)...)
	}
	return cycles
}

// BuildPlan adds the tasks needed to update repos to wd.
func (x *TagXReposTasks) BuildPlan(wd *wf.Definition, repos []TagRepo, reviewers []string) (wf.Value[string], error) {
	// repo.ModPath to the wf.Value produced by planning it.
	planned := map[string]wf.Value[TagRepo]{}

	// Find all repositories whose dependencies are satisfied and update
	// them, proceeding until all are planned or no progress can be made.
	for len(planned) != len(repos) {
		progress := false
		for _, repo := range repos {
			if _, ok := planned[repo.ModPath]; ok {
				continue
			}
			dep, ok := x.planRepo(wd, repo, planned, reviewers, false)
			if !ok {
				continue
			}
			planned[repo.ModPath] = dep
			progress = true
		}

		if !progress {
			var missing []string
			for _, r := range repos {
				if planned[r.ModPath] == nil {
					missing = append(missing, r.Name)
				}
			}
			return nil, fmt.Errorf("failed to progress the plan: todo: %v", missing)
		}
	}
	var allDeps []wf.Dependency
	for _, dep := range planned {
		allDeps = append(allDeps, dep)
	}
	done := wf.Task0(wd, "done", func(_ context.Context) (string, error) { return "done!", nil }, wf.After(allDeps...))
	return done, nil
}

func (x *TagXReposTasks) BuildSingleRepoPlan(wd *wf.Definition, repoSlice []TagRepo, name string, skipPostSubmit bool, reviewers []string) (wf.Value[TagRepo], error) {
	repos := map[string]TagRepo{}
	plannedRepos := map[string]wf.Value[TagRepo]{}
	for _, r := range repoSlice {
		repos[r.Name] = r

		// Pretend that we've just tagged version that was live when we started.
		r.NewerVersion = r.StartVersion
		plannedRepos[r.ModPath] = wf.Const(r)
	}
	repo, ok := repos[name]
	if !ok {
		return nil, fmt.Errorf("no repository %q", name)
	}
	tagged, ok := x.planRepo(wd, repo, plannedRepos, reviewers, skipPostSubmit)
	if !ok {
		var deps []string
		for _, d := range repo.Deps {
			deps = append(deps, d.ModPath)
		}
		return nil, fmt.Errorf("%q doesn't have all of its dependencies (%q)", repo.Name, deps)
	}
	return tagged, nil
}

// planRepo adds tasks to wf to update and possibly tag repo. It returns
// a Value containing the tagged repository's information, or nil, false
// if the dependencies it's waiting on haven't been planned yet.
func (x *TagXReposTasks) planRepo(wd *wf.Definition, repo TagRepo, planned map[string]wf.Value[TagRepo], reviewers []string, skipPostSubmit bool) (_ wf.Value[TagRepo], ready bool) {
	var plannedDeps []wf.Value[TagRepo]
	for _, dep := range repo.Deps {
		if !dep.Wait {
			continue
		} else if r, ok := planned[dep.ModPath]; ok {
			plannedDeps = append(plannedDeps, r)
		} else {
			return nil, false
		}
	}
	wd = wd.Sub(repo.Name)
	repoName, branch := wf.Const(repo.Name), wf.Const("master")

	var tagCommit wf.Value[string]
	if len(plannedDeps) == 0 {
		tagCommit = wf.Task2(wd, "read branch head", x.Gerrit.ReadBranchHead, repoName, branch)
	} else {
		goMod := wf.Task3(wd, "generate go.mod", x.UpdateGoMod, wf.Const(repo), wf.Slice(plannedDeps...), branch)
		cl := wf.Task4(wd, "mail go.mod", x.MailGoMod, repoName, branch, goMod, wf.Const(reviewers))
		tagCommit = wf.Task3(wd, "wait for submit", x.AwaitGoMod, cl, repoName, branch)
	}
	if repo.UpdateOnlyAndNotTag() {
		noop := func(_ context.Context, r TagRepo, _ string) (TagRepo, error) { return r, nil }
		return wf.Task2(wd, "don't tag", noop, wf.Const(repo), tagCommit), true
	}
	if !skipPostSubmit {
		tagCommit = wf.Task2(wd, "wait for green post-submit", x.AwaitGreen, wf.Const(repo), tagCommit)
	}
	tagged := wf.Task2(wd, "tag if appropriate", x.MaybeTag, wf.Const(repo), tagCommit)
	return tagged, true
}

func (x *TagXReposTasks) UpdateGoMod(ctx *wf.TaskContext, repo TagRepo, deps []TagRepo, branch string) (files map[string]string, _ error) {
	// Update the root module to the selected versions.
	var script strings.Builder
	script.WriteString("go get")
	for _, dep := range deps {
		script.WriteString(" " + dep.ModPath + "@" + dep.NewerVersion)
	}
	script.WriteString("\n")

	// Tidy the root module.
	// Also tidy nested modules with a replace directive.
	dirs := []string{"."}
	switch repo.Name {
	case "exp":
		dirs = append(dirs, "slog/benchmarks/zap_benchmarks")     // A local replace directive as of 2023-09-05.
		dirs = append(dirs, "slog/benchmarks/zerolog_benchmarks") // A local replace directive as of 2023-09-05.
	case "telemetry":
		dirs = append(dirs, "godev") // A local replace directive as of 2023-09-05.
	case "tools":
		dirs = append(dirs, "gopls") // A local replace directive as of 2023-09-05.
	}
	var outputs []string
	for _, dir := range dirs {
		compat := ""
		if repo.Compat != "" {
			compat = "-compat " + repo.Compat
		}
		script.WriteString(fmt.Sprintf("(cd %v && touch go.sum && go mod tidy %v)\n", dir, compat))
		outputs = append(outputs, dir+"/go.mod", dir+"/go.sum")
	}
	build, err := x.CloudBuild.RunScript(ctx, script.String(), repo.Name, outputs)
	if err != nil {
		return nil, err
	}
	return buildToOutputs(ctx, x.CloudBuild, build)
}

func buildToOutputs(ctx *wf.TaskContext, buildClient CloudBuildClient, build CloudBuild) (map[string]string, error) {
	if _, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return buildClient.Completed(ctx, build)
	}); err != nil {
		return nil, err
	}

	outfs, err := buildClient.ResultFS(ctx, build)
	if err != nil {
		return nil, err
	}
	outMap := map[string]string{}
	return outMap, fs.WalkDir(outfs, ".", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}
		bytes, err := fs.ReadFile(outfs, path)
		outMap[path] = string(bytes)
		return err
	})
}

func (x *TagXReposTasks) MailGoMod(ctx *wf.TaskContext, repo, branch string, files map[string]string, reviewers []string) (string, error) {
	const subject = `go.mod: update golang.org/x dependencies

Update golang.org/x dependencies to their latest tagged versions.`
	return x.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: repo,
		Branch:  branch,
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
	// Check if commit is already the latest tagged version.
	// If so, it's deemed green and there's no need to wait.
	if _, highestReleaseTagIsCommit, err := x.latestReleaseTag(ctx, repo.Name, commit); err != nil {
		return "", err
	} else if highestReleaseTagIsCommit {
		return commit, nil
	}

	// Wait for a green commit.
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
// the latest tagged version. repo is returned with NewerVersion populated.
func (x *TagXReposTasks) MaybeTag(ctx *wf.TaskContext, repo TagRepo, commit string) (TagRepo, error) {
	// Check if commit is already the latest tagged version.
	highestRelease, highestReleaseTagIsCommit, err := x.latestReleaseTag(ctx, repo.Name, commit)
	if err != nil {
		return TagRepo{}, err
	} else if highestRelease == "" {
		return TagRepo{}, fmt.Errorf("no semver tags found in %v", repo.Name)
	}
	if highestReleaseTagIsCommit {
		repo.NewerVersion = highestRelease
		return repo, nil
	}

	// Tag commit.
	repo.NewerVersion, err = nextMinor(highestRelease)
	if err != nil {
		return TagRepo{}, fmt.Errorf("couldn't pick next version for %v: %v", repo.Name, err)
	}
	ctx.Printf("Tagging %v at %v as %v", repo.Name, commit, repo.NewerVersion)
	return repo, x.Gerrit.Tag(ctx, repo.Name, repo.NewerVersion, commit)
}

// latestReleaseTag fetches tags for repo and returns the latest release tag,
// or the empty string if there are no release tags. It also reports whether
// commit, if provided, matches the latest release tag's revision.
func (x *TagXReposTasks) latestReleaseTag(ctx context.Context, repo, commit string) (highestRelease string, isCommit bool, _ error) {
	tags, err := x.Gerrit.ListTags(ctx, repo)
	if err != nil {
		return "", false, fmt.Errorf("listing project %q tags: %v", repo, err)
	}
	for _, tag := range tags {
		if semver.IsValid(tag) && semver.Prerelease(tag) == "" &&
			(highestRelease == "" || semver.Compare(highestRelease, tag) < 0) {
			highestRelease = tag
		}
	}
	if commit != "" && highestRelease != "" {
		tagInfo, err := x.Gerrit.GetTag(ctx, repo, highestRelease)
		if err != nil {
			return "", false, fmt.Errorf("reading project %q tag %q: %v", repo, highestRelease, err)
		}
		isCommit = tagInfo.Revision == commit
	}
	return highestRelease, isCommit, nil
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
