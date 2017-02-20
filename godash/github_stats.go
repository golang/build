// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package godash

import (
	"errors"
	"sort"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

// Update fetches new information from GitHub for any issues modified since s was last updated.
func (s *Stats) Update(ctx context.Context, gh *github.Client, log func(string, ...interface{})) error {
	res, err := listIssues(ctx, gh, github.IssueListByRepoOptions{State: "all", Since: s.Since})
	if err != nil {
		return err
	}
	log("Github returned %d issues", len(res))
	if s.Issues == nil {
		log("Initializing new data")
		s.Issues = make(map[int]*IssueStat)
	}
	for _, issue := range res {
		s.Issues[issue.Number] = &IssueStat{
			Created:   issue.Created,
			Closed:    issue.Closed,
			Updated:   issue.Updated,
			Milestone: issue.Milestone,
		}

		if issue.Updated.After(s.Since) {
			s.Since = issue.Updated
		}
	}
	// Ingest event details. We have to do this last because it
	// can blow through our rate limit.
	var issuenums []int
	for n, issue := range s.Issues {
		if !issue.Updated.Before(s.IssueDetailSince) {
			issuenums = append(issuenums, n)
		}
	}
	if len(issuenums) == 0 {
		log("No new issues; not updating")
		return nil
	}
	sort.Sort(issueUpdatedSort{issuenums, s.Issues})

	log("Need to update %d issues", len(issuenums))

	// TODO: Limit by time instead of a fixed cap?
	if len(issuenums) > 1000 {
		issuenums = issuenums[:1000]
	}

	numch := make(chan int)
	g, ctx := errgroup.WithContext(ctx)

	for i := 0; i < 5; i++ {
		g.Go(func() error {
			for num := range numch {
				if err := s.UpdateIssue(ctx, gh, num, log); err != nil {
					return err
				}
			}
			return nil
		})
	}
	g.Go(func() error {
		defer close(numch)
		for _, num := range issuenums {
			select {
			case numch <- num:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		// Failure to update issue details should not cause the whole update to fail.
		log("Failed updating stats: %v", err)
		return nil
	}
	s.IssueDetailSince = s.Issues[issuenums[len(issuenums)-1]].Updated
	log("Updated %d issues. Details now correct through %v", len(issuenums), s.IssueDetailSince)
	return nil
}

// UpdateIssue updates a single issue, without moving s.Since.
func (s *Stats) UpdateIssue(ctx context.Context, gh *github.Client, num int, log func(string, ...interface{})) error {
	issue := s.Issues[num]
	var milestone string
	var labels []string
	milestoneChange := func(m string, t time.Time) {
		if milestone != "" && m != milestone {
			issue.MilestoneHistory = append(issue.MilestoneHistory, MilestoneChange{milestone, t})
		}
		milestone = m
	}
	for page := 1; ; {
		events, resp, err := gh.Issues.ListIssueEvents(ctx, projectOwner, projectRepo, num, &github.ListOptions{
			Page:    page,
			PerPage: 100,
		})
		if err != nil {
			// TODO: Sometimes calls to GitHub seem to time out; if they do, perhaps we should retry?
			return err
		}
		if page == 1 {
			issue.MilestoneHistory = nil
		}
		for _, event := range events {
			evtype := getString(event.Event)
			evtime := getTime(event.CreatedAt)
			switch evtype {
			case "labeled":
				if event.Label == nil {
					continue
				}
				label := getString(event.Label.Name)
				labels = append(labels, label)
				switch label {
				case "fixed", "retracted", "done", "duplicate", "workingasintended", "wontfix", "invalid", "unfortunate", "timedout":
					// Old issues have these labels.
					issue.Closed = evtime
				}
				if label[:2] == "go" {
					milestoneChange("Go"+label[2:], evtime)
				}
			case "milestoned":
				if event.Milestone == nil {
					continue
				}
				m := getString(event.Milestone.Title)
				milestoneChange(m, evtime)
			}
		}
		if (resp.Remaining * 2) < (resp.Limit / 3) {
			// Save rate limit for the more important updates above.
			log("Out of quota (%d/%d) until %v", resp.Remaining, resp.Limit, resp.Reset)
			return errors.New("out of quota")
		}
		if resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}
	// GitHub has a massive number of issues closed on 2014/12/8;
	// I suspect this is when they first added the closed
	// field. If we still think the issue is closed on this date,
	// that probably means we failed to correctly process the
	// labels. Log the issue's labels so we can investigate and
	// possibly add to the list of labels above.
	c := issue.Closed
	if c.Year() == 2014 && c.Month() == 12 && c.Day() == 8 {
		log("Issue %d labels: %v", num, labels)
	}
	return nil
}

type issueUpdatedSort struct {
	nums   []int
	issues map[int]*IssueStat
}

func (x issueUpdatedSort) Len() int      { return len(x.nums) }
func (x issueUpdatedSort) Swap(i, j int) { x.nums[i], x.nums[j] = x.nums[j], x.nums[i] }
func (x issueUpdatedSort) Less(i, j int) bool {
	return x.issues[x.nums[i]].Updated.Before(x.issues[x.nums[j]].Updated)
}

// Stats contains information about all GitHub issues.
//
// We track statistics for each issue to produce graphs:
//  - Issue creation time
//  - TODO(quentin): First reply time from Go team member
//  - Issue close time
//  - Issue current milestone
//  - History of issue labels + milestones
// As well as the following global info
//  - Last issue update time
//  - Last issue detail update time
type Stats struct {
	// Issues is a map of issue number to per-issue data.
	Issues map[int]*IssueStat
	// Since is the high watermark for issue update times; any
	// issues updated since Since will be refetched.
	Since time.Time
	// IssueDetailSince is the high watermark for issue details;
	// this is separate because requesting issue details uses up
	// quota, and we cannot request all issues at once.
	IssueDetailSince time.Time
}

// MilestoneChange stores a historical milestone. We store historical
// milestones separately since most issues have only ever had one
// milestone; we can save on constructing and serializing the slice
// then.
type MilestoneChange struct {
	// Name is the name of the milestone.
	Name string
	// Until is the time that the milestone was removed.
	Until time.Time
}

// IssueStat holds an individual issue's important facts.
type IssueStat struct {
	Created, Closed, Updated time.Time
	// Milestone contains the milestone the issue is currently
	// associated with.
	Milestone string
	// MilestoneHistory contains previous milestones and the time
	// the issue ceased to be assigned to that milestone. We store
	// this so the slice can be empty for most issues that have
	// only ever been associated with one milestone.
	MilestoneHistory []MilestoneChange
}
