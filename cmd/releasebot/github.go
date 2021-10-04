// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/go-github/github"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/oauth2"
)

const (
	projectOwner = "golang"
	projectRepo  = "go"
)

var githubClient *github.Client

// GitHub personal access token, from https://github.com/settings/applications.
var githubAuthToken string

var goRepo *maintner.GitHubRepo

func loadMaintner() {
	corpus, err := godata.Get(context.Background())
	if err != nil {
		log.Fatal("failed to load maintner data:", err)
	}
	goRepo = corpus.GitHub().Repo(projectOwner, projectRepo)
}

func loadGithubAuth() {
	const short = ".github-issue-token"
	filename := filepath.Clean(os.Getenv("HOME") + "/" + short)
	shortFilename := filepath.Clean("$HOME/" + short)
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatal("reading token: ", err, "\n\n"+
			"Please create a personal access token at https://github.com/settings/tokens/new\n"+
			"and write it to ", shortFilename, " to use this program.\n"+
			"** The token only needs the public_repo scope. **\n"+
			"The benefit of using a personal access token over using your GitHub\n"+
			"password directly is that you can limit its use and revoke it at any time.\n\n")
	}
	fi, err := os.Stat(filename)
	if err != nil {
		log.Fatalln("reading token:", err)
	}
	if fi.Mode()&0077 != 0 {
		log.Fatalf("reading token: %s mode is %#o, want %#o", shortFilename, fi.Mode()&0777, fi.Mode()&0700)
	}
	githubAuthToken = strings.TrimSpace(string(data))
	t := &oauth2.Transport{
		Source: &tokenSource{AccessToken: githubAuthToken},
	}
	githubClient = github.NewClient(&http.Client{Transport: t})
}

// releaseStatusTitle returns the title of the release status issue
// for the given milestone.
// If you change this function, releasebot will not be able to find an
// existing tracking issue using the old name and will create a new one.
func (w *Work) releaseStatusTitle() string {
	return "all: " + strings.Replace(w.Version, "go", "Go ", -1) + " release status"
}

type tokenSource oauth2.Token

func (t *tokenSource) Token() (*oauth2.Token, error) {
	return (*oauth2.Token)(t), nil
}

func (w *Work) findOrCreateReleaseIssue() {
	if w.Security {
		// There is no release status issue for security releases
		// to avoid the risk of leaking sensitive test failures.
		return
	}
	w.log.Printf("Release status issue title: %q", w.releaseStatusTitle())
	if dryRun {
		return
	}
	if w.ReleaseIssue == 0 {
		title := w.releaseStatusTitle()
		body := fmt.Sprintf("Issue tracking the %s release by releasebot.", w.Version)
		num, err := w.createGitHubIssue(title, body)
		if err != nil {
			w.log.Panic(err)
		}
		w.ReleaseIssue = num
		w.log.Printf("Release status issue: https://golang.org/issue/%d", num)
	}
}

// createGitHubIssue creates an issue in the release milestone and returns its number.
func (w *Work) createGitHubIssue(title, msg string) (int, error) {
	if dryRun {
		return 0, errors.New("attempted write operation in dry-run mode")
	}
	var dup int
	goRepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Title == title {
			dup = int(gi.Number)
			return errors.New("stop iteration")
		}
		return nil
	})
	if dup != 0 {
		return dup, nil
	}
	opts := &github.IssueListByRepoOptions{
		State:       "all",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	if !w.BetaRelease && !w.RCRelease {
		opts.Milestone = strconv.Itoa(int(w.Milestone.Number))
	}
	is, _, err := githubClient.Issues.ListByRepo(context.TODO(), "golang", "go", opts)
	if err != nil {
		return 0, err
	}
	for _, i := range is {
		if i.GetTitle() == title {
			// Dup.
			return i.GetNumber(), nil
		}
	}
	copts := &github.IssueRequest{
		Title: github.String(title),
		Body:  github.String(msg),
	}
	if !w.BetaRelease && !w.RCRelease {
		copts.Milestone = github.Int(int(w.Milestone.Number))
	}
	i, _, err := githubClient.Issues.Create(context.TODO(), "golang", "go", copts)
	return i.GetNumber(), err
}

// pushIssues moves open issues to the milestone of the next release of the same kind,
// creating the milestone if it doesn't already exist.
// For major releases, it's the milestone of the next major release (e.g., 1.14 → 1.15).
// For minor releases, it's the milestone of the next minor release (e.g., 1.14.1 → 1.14.2).
// For other release types, it does nothing.
//
// For major releases, it also creates the first minor release milestone if it doesn't already exist.
func (w *Work) pushIssues() {
	if w.BetaRelease || w.RCRelease {
		// Nothing to do.
		return
	}

	// Get the milestone for the next release.
	var nextMilestone *github.Milestone
	nextV, err := nextVersion(w.Version)
	if err != nil {
		w.logError("error determining next version: %v", err)
		return
	}
	nextMilestone, err = w.findOrCreateMilestone(nextV)
	if err != nil {
		w.logError("error finding or creating %s, the next GitHub milestone after release %s: %v", nextV, w.Version, err)
		return
	}

	// For major releases (go1.X), also create the first minor release milestone (go1.X.1). See issue 44404.
	if strings.Count(w.Version, ".") == 1 {
		firstMinor := w.Version + ".1"
		_, err := w.findOrCreateMilestone(firstMinor)
		if err != nil {
			// Log this error, but continue executing the rest of the task.
			w.logError("error finding or creating %s, the first minor release GitHub milestone after major release %s: %v", firstMinor, w.Version, err)
		}
	}

	if err := goRepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Milestone == nil || gi.Milestone.ID != w.Milestone.ID {
			return nil
		}
		if gi.Number == int32(w.ReleaseIssue) {
			return nil
		}
		// All issues are unrelated if this is a security release.
		if gi.Closed && !w.Security {
			return nil
		}
		w.log.Printf("changing milestone of issue %d to %s", gi.Number, nextMilestone.GetTitle())
		if dryRun {
			return nil
		}
		_, _, err := githubClient.Issues.Edit(context.TODO(), projectOwner, projectRepo, int(gi.Number), &github.IssueRequest{
			Milestone: github.Int(nextMilestone.GetNumber()),
		})
		if err != nil {
			return fmt.Errorf("#%d: %s", gi.Number, err)
		}
		return nil
	}); err != nil {
		w.logError("error moving issues to the next minor release: %v", err)
		return
	}
}

// findOrCreateMilestone finds or creates a GitHub milestone corresponding
// to the specified Go version. This is done via the GitHub API, using githubClient.
// If the milestone exists but isn't open, an error is returned.
func (w *Work) findOrCreateMilestone(version string) (*github.Milestone, error) {
	// Look for an existing open milestone corresponding to version,
	// and return it if found.
	for opt := (&github.MilestoneListOptions{ListOptions: github.ListOptions{PerPage: 100}}); ; {
		ms, resp, err := githubClient.Issues.ListMilestones(context.Background(), projectOwner, projectRepo, opt)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if strings.ToLower(m.GetTitle()) == version {
				// Found an existing milestone.
				return m, nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	// Create a new milestone.
	// For historical reasons, Go milestone titles use a capital "Go1.n" format,
	// in contrast to go versions which are like "go1.n". Do the same here.
	title := strings.Replace(version, "go", "Go", 1)
	w.log.Printf("creating milestone titled %q", title)
	if dryRun {
		return &github.Milestone{Title: github.String(title)}, nil
	}
	m, _, err := githubClient.Issues.CreateMilestone(context.Background(), projectOwner, projectRepo, &github.Milestone{
		Title: github.String(title),
	})
	if e := (*github.ErrorResponse)(nil); errors.As(err, &e) && e.Response != nil && e.Response.StatusCode == http.StatusUnprocessableEntity && len(e.Errors) == 1 && e.Errors[0].Code == "already_exists" {
		// We'll run into an already_exists error here if the milestone exists,
		// but it wasn't found in the loop above because the milestone isn't open.
		// That shouldn't happen under normal circumstances, so if it does,
		// let humans figure out how to best deal with it.
		return nil, errors.New("a closed milestone with the same title already exists")
	} else if err != nil {
		return nil, err
	}
	return m, nil
}

// closeMilestone closes the milestone for the current release.
func (w *Work) closeMilestone() {
	w.log.Printf("closing milestone %s", w.Milestone.Title)
	if dryRun {
		return
	}
	closed := "closed"
	_, _, err := githubClient.Issues.EditMilestone(context.TODO(), projectOwner, projectRepo, int(w.Milestone.Number), &github.Milestone{
		State: &closed,
	})
	if err != nil {
		w.logError("closing milestone: %v", err)
	}

}

func (w *Work) removeOkayAfterBeta1() {
	if !w.BetaRelease || !strings.HasSuffix(w.Version, "beta1") {
		// Nothing to do.
		return
	}

	if err := goRepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Milestone == nil || gi.Milestone.ID != w.Milestone.ID {
			return nil
		}
		if gi.Number == int32(w.ReleaseIssue) {
			return nil
		}
		if gi.Closed || !gi.HasLabel("release-blocker") || !gi.HasLabel("okay-after-beta1") {
			return nil
		}
		w.log.Printf("removing okay-after-beta1 label in issue %d", gi.Number)
		if dryRun {
			return nil
		}
		_, err := githubClient.Issues.RemoveLabelForIssue(context.Background(),
			projectOwner, projectRepo, int(gi.Number), "okay-after-beta1")
		if err != nil {
			return fmt.Errorf("#%d: %s", gi.Number, err)
		}
		return nil
	}); err != nil {
		w.logError("error removing okay-after-beta1 label from issues in current milestone: %v", err)
		return
	}
}

const githubCommentCharacterLimit = 65536 // discovered in golang.org/issue/45998

func postGithubComment(number int, body string) error {
	if dryRun {
		return errors.New("attempted write operation in dry-run mode")
	}
	_, _, err := githubClient.Issues.CreateComment(context.TODO(), projectOwner, projectRepo, number, &github.IssueComment{
		Body: &body,
	})
	return err
}
