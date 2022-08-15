package task

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

type TagXReposTasks struct {
	Gerrit GerritClient
}

func (x *TagXReposTasks) NewDefinition() *wf.Definition {
	wd := wf.New()
	repos := wf.Task0(wd, "Select repositories", x.SelectRepos)
	wf.Expand1(wd, "Create plan", x.BuildPlan, repos)
	return wd
}

type TagRepo struct {
	Name string
	Next string
	Deps []string
}

func (x *TagXReposTasks) SelectRepos(ctx *wf.TaskContext) ([]TagRepo, error) {
	projects, err := x.Gerrit.ListProjects(ctx)
	if err != nil {
		return nil, err
	}

	ctx.Printf("Examining repositories %v", projects)
	var repos []TagRepo
	for _, p := range projects {
		repo, err := x.readRepo(ctx, p)
		if err != nil {
			return nil, err
		}
		if repo != nil {
			repos = append(repos, *repo)
		}
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
	if !strings.HasPrefix(mf.Module.Mod.Path, "golang.org/x") {
		ctx.Printf("ignoring %v: not golang.org/x", project)
		return nil, nil
	}

	tags, err := x.Gerrit.ListTags(ctx, project)
	if err != nil {
		return nil, err
	}
	highestRelease := ""
	for _, tag := range tags {
		if semver.IsValid(tag) && semver.Prerelease(tag) == "" &&
			(highestRelease == "" || semver.Compare(highestRelease, tag) < 0) {
			highestRelease = tag
		}
	}
	nextTag := "v0.1.0"
	if highestRelease != "" {
		var err error
		nextTag, err = nextVersion(highestRelease)
		if err != nil {
			return nil, fmt.Errorf("couldn't pick next version for %v: %v", project, err)
		}
	}

	result := &TagRepo{
		Name: project,
		Next: nextTag,
	}
	for _, req := range mf.Require {
		if strings.HasPrefix(req.Mod.Path, "golang.org/x") {
			result.Deps = append(result.Deps, req.Mod.Path)
		}
	}
	return result, nil
}

func (x *TagXReposTasks) BuildPlan(wd *wf.Definition, repos []TagRepo) error {
	updated := map[string]wf.Dependency{}

	// Find all repositories whose dependencies are satisfied and update
	// them, proceeding until all are updated or no progress can be made.
	for len(updated) != len(repos) {
		progress := false
		for _, repo := range repos {
			dep, ok := x.planRepo(wd, repo, updated)
			if !ok {
				continue
			}
			updated[repo.Name] = dep
			progress = true
		}

		if !progress {
			return fmt.Errorf("cycle detected, sorry")
		}
	}
	return nil
}

func (x *TagXReposTasks) planRepo(wd *wf.Definition, repo TagRepo, updated map[string]wf.Dependency) (wf.Dependency, bool) {
	var deps []wf.Dependency
	for _, repoDeps := range repo.Deps {
		if dep, ok := updated[repoDeps]; ok {
			deps = append(deps, dep)
		} else {
			return nil, false
		}
	}
	wd = wd.Sub(repo.Name)
	commit := wf.Task1(wd, "update go.mod", x.UpdateGoMod, wf.Const(repo), wf.After(deps...))
	green := wf.Action2(wd, "wait for green post-submit", x.AwaitGreen, wf.Const(repo), commit)
	tagged := wf.Task2(wd, "tag", x.TagRepo, wf.Const(repo), commit, wf.After(green))
	wf.Output(wd, "tagged version", tagged)
	return tagged, true
}

func (x *TagXReposTasks) UpdateGoMod(ctx *wf.TaskContext, repo TagRepo) (string, error) {
	return "", nil
}

func (x *TagXReposTasks) AwaitGreen(ctx *wf.TaskContext, repo TagRepo, commit string) error {
	return nil
}

func (x *TagXReposTasks) TagRepo(ctx *wf.TaskContext, repo TagRepo, commit string) (string, error) {
	return repo.Next, nil
}
