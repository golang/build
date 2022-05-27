package task

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

// VersionTasks contains tasks related to versioning the release.
type VersionTasks struct {
	Gerrit  GerritClient
	Project string
}

type NextVersions struct {
	CurrentMinor  string
	PreviousMinor string
	Beta          string
	RC            string
	Major         string
}

// GetNextVersions returns the next version for each type of release.
func (t *VersionTasks) GetNextVersions(ctx *workflow.TaskContext) (NextVersions, error) {
	tags, err := t.Gerrit.ListTags(ctx, t.Project)
	if err != nil {
		return NextVersions{}, err
	}
	tagSet := map[string]bool{}
	for _, tag := range tags {
		tagSet[tag] = true
	}
	// Find the most recently released major version.
	currentMajor := 0
	for ; ; currentMajor++ {
		if !tagSet[fmt.Sprintf("go1.%d", currentMajor+1)] {
			break
		}
	}
	var savedError error
	findUnused := func(v string) string {
		for {
			if !tagSet[v] {
				return v
			}
			var err error
			v, err = nextVersion(v)
			if err != nil {
				savedError = err
				return ""
			}
		}
	}
	// Find the next missing tag for each release type.
	result := NextVersions{
		CurrentMinor:  findUnused(fmt.Sprintf("go1.%d.1", currentMajor)),
		PreviousMinor: findUnused(fmt.Sprintf("go1.%d.1", currentMajor-1)),
		Beta:          findUnused(fmt.Sprintf("go1.%dbeta1", currentMajor+1)),
		RC:            findUnused(fmt.Sprintf("go1.%drc1", currentMajor+1)),
		Major:         fmt.Sprintf("go1.%d", currentMajor+1),
	}
	return result, savedError
}

func nextVersion(version string) (string, error) {
	lastNonDigit := strings.LastIndexFunc(version, func(r rune) bool {
		return r < '0' || r > '9'
	})
	if lastNonDigit == -1 || len(version) == lastNonDigit {
		return "", fmt.Errorf("malformatted Go version %q", version)
	}
	n, err := strconv.Atoi(version[lastNonDigit+1:])
	if err != nil {
		return "", fmt.Errorf("malformatted Go version %q (%v)", version, err)
	}
	return fmt.Sprintf("%s%d", version[:lastNonDigit+1], n+1), nil
}

// CreateAutoSubmitVersionCL mails an auto-submit change to update VERSION on branch.
func (t *VersionTasks) CreateAutoSubmitVersionCL(ctx *workflow.TaskContext, branch, version string) (string, error) {
	return t.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: t.Project,
		Branch:  branch,
		Subject: fmt.Sprintf("[%v] %v", branch, version),
	}, map[string]string{
		"VERSION": version,
	})
}

// AwaitCL waits for the specified CL to be submitted.
func (t *VersionTasks) AwaitCL(ctx *workflow.TaskContext, changeID string) (string, error) {
	ctx.Printf("Awaiting review/submit of %v", changeLink(changeID))
	return t.Gerrit.AwaitSubmit(ctx, changeID)
}

// TagRelease tags commit as version.
func (t *VersionTasks) TagRelease(ctx *workflow.TaskContext, version, commit string) (string, error) {
	return "", t.Gerrit.Tag(ctx, t.Project, version, commit)
}
