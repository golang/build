// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

const (
	projectOwner = "golang"
	projectRepo  = "go"
)

var githubClient *github.Client

// GitHub personal access token, from https://github.com/settings/applications.
var githubAuthToken string

func loadGithubAuth() {
	const short = ".github-issue-token"
	filename := filepath.Clean(os.Getenv("HOME") + "/" + short)
	shortFilename := filepath.Clean("$HOME/" + short)
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatal("reading token: ", err, "\n\n"+
			"Please create a personal access token at https://github.com/settings/tokens/new\n"+
			"and write it to ", shortFilename, " to use this program.\n"+
			"The token only needs the repo scope, or private_repo if you want to\n"+
			"view or edit issues for private repositories.\n"+
			"The benefit of using a personal access token over using your GitHub\n"+
			"password directly is that you can limit its use and revoke it at any time.\n\n")
	}
	fi, err := os.Stat(filename)
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
func releaseStatusTitle(m *github.Milestone) string {
	return "all: " + strings.Replace(m.GetTitle(), "Go", "Go ", -1) + " release status"
}

type tokenSource oauth2.Token

func (t *tokenSource) Token() (*oauth2.Token, error) {
	return (*oauth2.Token)(t), nil
}

func loadMilestones() ([]*github.Milestone, error) {
	// NOTE(rsc): There appears to be no paging possible.
	all, _, err := githubClient.Issues.ListMilestones(context.TODO(), projectOwner, projectRepo, &github.MilestoneListOptions{
		State: "open",
	})
	if err != nil {
		return nil, err
	}
	if all == nil {
		all = []*github.Milestone{}
	}
	return all, nil
}

// findIssues finds all the issues for the given milestone and
// categorizes them into approved cherry-picks (w.Picks)
// and other issues (w.OtherIssues).
// It also finds the release summary issue (w.ReleaseIssue).
func (w *Work) findIssues() {
	issues, err := listRepoIssues(github.IssueListByRepoOptions{
		Milestone: fmt.Sprint(w.Milestone.GetNumber()),
	})
	if err != nil {
		w.log.Panic(err)
	}

	for _, issue := range issues {
		if issue.GetTitle() == releaseStatusTitle(w.Milestone) {
			if w.ReleaseIssue != nil {
				w.log.Printf("**warning**: multiple release issues: #%d and #%d\n", w.ReleaseIssue.GetNumber(), issue.GetNumber())
				continue
			}
			w.ReleaseIssue = issue
			continue
		}
		if hasLabel(issue, "cherry-pick-approved") {
			w.Picks = append(w.Picks, issue)
			continue
		}
		w.OtherIssues = append(w.OtherIssues, issue)
	}
	sort.Slice(w.Picks, func(i, j int) bool { return w.Picks[i].GetNumber() < w.Picks[j].GetNumber() })

	if w.ReleaseIssue == nil {
		title := releaseStatusTitle(w.Milestone)
		body := wrapStatus(w.Milestone, "Nothing yet.")
		req := &github.IssueRequest{
			Title:     &title,
			Body:      &body,
			Milestone: w.Milestone.Number,
		}
		issue, _, err := githubClient.Issues.Create(context.TODO(), projectOwner, projectRepo, req)
		if err != nil {
			w.log.Panic(err)
		}
		w.ReleaseIssue = issue
	}
}

// listRepoIssues wraps Issues.ListByRepo to deal with paging.
func listRepoIssues(opt github.IssueListByRepoOptions) ([]*github.Issue, error) {
	var all []*github.Issue
	for page := 1; ; {
		xopt := opt
		xopt.ListOptions = github.ListOptions{
			Page:    page,
			PerPage: 100,
		}
		list, resp, err := githubClient.Issues.ListByRepo(context.TODO(), projectOwner, projectRepo, &xopt)
		all = append(all, list...)
		if err != nil {
			return all, err
		}
		if resp.NextPage < page {
			break
		}
		page = resp.NextPage
	}
	return all, nil
}

// hasLabel reports whether issue has the given label.
func hasLabel(issue *github.Issue, label string) bool {
	for _, l := range issue.Labels {
		if l.GetName() == label {
			return true
		}
	}
	return false
}

var clOK = regexp.MustCompile(`(?i)^CL (\d+) OK(( for Go \d+\.\d+\.\d+)?.*)`)
var afterCL = regexp.MustCompile(`(?i)after CL (\d+)`)

// listIssueComments wraps Issues.ListComments to deal with paging.
func listIssueComments(number int) ([]*github.IssueComment, error) {
	var all []*github.IssueComment
	for page := 1; ; {
		list, resp, err := githubClient.Issues.ListComments(context.TODO(), projectOwner, projectRepo, number, &github.IssueListCommentsOptions{
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: 100,
			},
		})
		all = append(all, list...)
		if err != nil {
			return all, err
		}
		if resp.NextPage < page {
			break
		}
		page = resp.NextPage
	}
	return all, nil
}

func (w *Work) findCLs() {
	// Preload all CLs in parallel.
	type comments struct {
		list []*github.IssueComment
		err  error
	}
	preload := make([]comments, len(w.Picks))
	var wg sync.WaitGroup
	for i, pick := range w.Picks {
		i := i
		number := pick.GetNumber()
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := listIssueComments(number)
			preload[i] = comments{list, err}
		}()
	}
	wg.Wait()

	var cls []*CL
	for i, pick := range w.Picks {
		number := pick.GetNumber()
		fmt.Printf("load #%d\n", number)
		found := false
		list, err := preload[i].list, preload[i].err
		if err != nil {
			w.log.Panic(err)
		}
		var last *CL
		for _, com := range list {
			user := com.User.GetLogin()
			text := com.GetBody()
			for _, line := range strings.Split(text, "\n") {
				if m := clOK.FindStringSubmatch(line); m != nil {
					if m[3] != " for Go "+strings.TrimPrefix(w.Milestone.GetTitle(), "Go") {
						w.log.Printf("#%d: %s: wrong milestone: %s\n", number, user, line)
						continue
					}
					if !githubCherryPickApprovers[user] {
						w.log.Printf("#%d: %s: not an approver: %s\n", number, user, line)
						continue
					}
					n, err := strconv.Atoi(m[1])
					if err != nil {
						w.log.Printf("#%d: %s: invalid CL number: %s\n", number, user, line)
						continue
					}
					cl := &CL{Num: n, Approver: user, Issues: []int{number}}
					if last != nil {
						cl.Prereq = []int{last.Num}
					}
					for _, am := range afterCL.FindAllStringSubmatch(m[2], -1) {
						n, err := strconv.Atoi(am[1])
						if err != nil {
							w.log.Printf("#%d: %s: invalid after CL number: %s\n", number, user, line)
							continue
						}
						cl.Prereq = append(cl.Prereq, n)
					}
					cls = append(cls, cl)
					found = true
					last = cl
				}
			}
		}
		if !found {
			log.Printf("#%d: has cherry-pick-approved label but no approvals found", number)
		}
	}

	sort.Slice(cls, func(i, j int) bool {
		return cls[i].Num < cls[j].Num || cls[i].Num == cls[j].Num && cls[i].Approver < cls[j].Approver
	})

	out := cls[:0]
	var last CL
	for _, cl := range cls {
		if cl.Num == last.Num {
			end := out[len(out)-1]
			if cl.Approver != last.Approver {
				end.Approver += "," + cl.Approver
			}
			end.Issues = append(end.Issues, cl.Issues...)
			end.Prereq = append(end.Prereq, cl.Prereq...)
		} else {
			out = append(out, cl)
		}
		last = *cl
	}
	w.CLs = out
}

func (w *Work) closeIssues() {
	all := append(w.Picks[:len(w.Picks):len(w.Picks)], w.ReleaseIssue)
	for _, issue := range all {
		if issue.GetState() == "closed" {
			continue
		}
		number := issue.GetNumber()
		var md bytes.Buffer
		fmt.Fprintf(&md, "%s has been packaged and includes:\n\n", w.Version)
		for _, cl := range w.CLs {
			match := issue == w.ReleaseIssue
			for _, n := range cl.Issues {
				if n == number {
					match = true
					break
				}
			}
			if match {
				fmt.Fprintf(&md, "  - %s %s\n", mdChangeLink(cl.Num), mdEscape(cl.Title))
			}
		}
		fmt.Fprintf(&md, "\nThe release is posted at [golang.org/dl](https://golang.org/dl).\n")
		md.WriteString(signature())
		postGithubComment(number, md.String())
		closed := "closed"
		_, _, err := githubClient.Issues.Edit(context.TODO(), projectOwner, projectRepo, number, &github.IssueRequest{
			State: &closed,
		})
		if err != nil {
			w.logError(nil, fmt.Sprintf("closing #%d: %v", number, err))
		}
	}
}

func (w *Work) closeMilestone() {
	closed := "closed"
	_, _, err := githubClient.Issues.EditMilestone(context.TODO(), projectOwner, projectRepo, w.Milestone.GetNumber(), &github.Milestone{
		State: &closed,
	})
	if err != nil {
		w.logError(nil, fmt.Sprintf("closing milestone: %v", err))
	}

}

func findGithubComment(number int, prefix string) *github.IssueComment {
	list, _ := listIssueComments(number)
	for _, com := range list {
		if strings.HasPrefix(com.GetBody(), prefix) {
			return com
		}
	}
	return nil
}

func updateGithubComment(number int, com *github.IssueComment, body string) error {
	_, _, err := githubClient.Issues.EditComment(context.TODO(), projectOwner, projectRepo, number, &github.IssueComment{
		ID:   com.ID,
		Body: &body,
	})
	return err
}

func postGithubComment(number int, body string) error {
	_, _, err := githubClient.Issues.CreateComment(context.TODO(), projectOwner, projectRepo, number, &github.IssueComment{
		Body: &body,
	})
	return err
}
