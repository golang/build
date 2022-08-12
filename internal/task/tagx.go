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

type TagRepo struct {
	Name string
	Next string
	Deps []string
}

func (x *TagXReposTasks) SelectRepos(ctx *wf.TaskContext) ([]*TagRepo, error) {
	projects, err := x.Gerrit.ListProjects(ctx)
	if err != nil {
		return nil, err
	}

	ctx.Printf("Examining repositories %v", projects)
	var repos []*TagRepo
	for _, p := range projects {
		repo, err := x.readRepo(ctx, p)
		if err != nil {
			return nil, err
		}
		if repo != nil {
			repos = append(repos, repo)
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
