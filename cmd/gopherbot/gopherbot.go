// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gopherbot command runs Go's gopherbot role account on
// GitHub and Gerrit.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/oauth2"
)

var (
	dryRun = flag.Bool("dry-run", false, "just report what would've been done, without changing anything")
)

func getGithubToken() (string, error) {
	// TODO: get from GCE metadata, etc.
	tokenFile := filepath.Join(os.Getenv("HOME"), "keys", "github-gobot")
	slurp, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		return "", err
	}
	f := strings.SplitN(strings.TrimSpace(string(slurp)), ":", 2)
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", fmt.Errorf("Expected token file %s to be of form <username>:<token>", tokenFile)
	}
	return f[1], nil
}

func getGithubClient() (*github.Client, error) {
	token, err := getGithubToken()
	if err != nil {
		if *dryRun {
			return github.NewClient(http.DefaultClient), nil
		}
		return nil, err
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return github.NewClient(tc), nil
}

func main() {
	flag.Parse()

	ghc, err := getGithubClient()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	corpus, err := godata.Get(ctx)
	if err != nil {
		log.Fatal(err)
	}

	repo := corpus.GitHub().Repo("golang", "go")
	if repo == nil {
		log.Fatal("Failed to find Go repo.")
	}

	bot := &gopherbot{
		ghc:    ghc,
		corpus: corpus,
		gorepo: repo,
	}

	var fail bool

	if err := bot.freezeOldIssues(ctx); err != nil {
		log.Printf("freezing old issues: %v", err)
		fail = true
	}

	if err := bot.labelProposals(ctx); err != nil {
		log.Printf("labeling proposals: %v", err)
		fail = true
	}

	if err := bot.setSubrepoMilestones(ctx); err != nil {
		log.Printf("setting subrepo milestones: %v", err)
		fail = true
	}

	if err := bot.setGccgoMilestones(ctx); err != nil {
		log.Printf("setting gccgo milestones: %v", err)
		fail = true
	}

	if err := bot.labelBuildIssues(ctx); err != nil {
		log.Printf("labeling build issues: %v", err)
		fail = true
	}

	if err := bot.labelDocumentationIssues(ctx); err != nil {
		log.Printf("labeling documentation issues: %v", err)
		fail = true
	}

	if err := bot.closeStaleWaitingForInfo(ctx); err != nil {
		log.Printf("closing stale WaitingForInfo: %v", err)
		fail = true
	}

	if fail {
		os.Exit(1)
	}
}

type gopherbot struct {
	ghc    *github.Client
	corpus *maintner.Corpus
	gorepo *maintner.GitHubRepo
}

func (b *gopherbot) addLabel(ctx context.Context, gi *maintner.GitHubIssue, label string) error {
	_, _, err := b.ghc.Issues.AddLabelsToIssue(ctx, "golang", "go", int(gi.Number), []string{label})
	return err
}

// freezeOldIssues locks any issue that's old and closed.
// (Otherwise people find ancient bugs via searches and start asking questions
// into a void and it's sad for everybody.)
// This method doesn't need to explicitly avoid edit wars with humans because
// it bails out if the issue was edited recently. A human unlocking an issue
// causes the updated time to bump, which means the bot wouldn't try to lock it
// again for another year.
func (b *gopherbot) freezeOldIssues(ctx context.Context) error {
	tooOld := time.Now().Add(-365 * 24 * time.Hour)
	log.Printf("Freezing closed issues before %v", tooOld.Format("2006-01-02"))

	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if !gi.Closed || gi.Locked {
			return nil
		}
		if gi.Updated.After(tooOld) {
			return nil
		}
		printIssue("freeze", gi)
		if *dryRun {
			return nil
		}
		_, err := b.ghc.Issues.Lock(ctx, "golang", "go", int(gi.Number))
		if err != nil {
			return err
		}
		return b.addLabel(ctx, gi, "FrozenDueToAge")
	})
}

// labelProposals adds the "Proposal" label and "Proposal" milestone
// to open issues with title beginning with "Proposal:". It tries not
// to get into an edit war with a human.
func (b *gopherbot) labelProposals(ctx context.Context) error {
	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed {
			return nil
		}
		if !strings.HasPrefix(gi.Title, "proposal:") && !strings.HasPrefix(gi.Title, "Proposal:") {
			return nil
		}
		// Add Milestone if missing:
		if gi.Milestone.IsNone() && !gi.HasEvent("milestoned") && !gi.HasEvent("demilestoned") {
			printIssue("proposal-milestone", gi)
			if !*dryRun {
				_, _, err := b.ghc.Issues.Edit(ctx, "golang", "go", int(gi.Number), &github.IssueRequest{
					Milestone: github.Int(30), // "Proposal"
				})
				if err != nil {
					return err
				}
			}
		}
		// Add Proposal label if missing:
		if !gi.HasLabel("Proposal") && !gi.HasEvent("unlabeled") {
			printIssue("proposal-label", gi)
			if !*dryRun {
				if err := b.addLabel(ctx, gi, "Proposal"); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (b *gopherbot) setSubrepoMilestones(ctx context.Context) error {
	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed || !gi.Milestone.IsNone() || gi.HasEvent("demilestoned") || gi.HasEvent("milestoned") {
			return nil
		}
		if !strings.HasPrefix(gi.Title, "x/") {
			return nil
		}
		pkg := gi.Title
		if colon := strings.IndexByte(pkg, ':'); colon >= 0 {
			pkg = pkg[:colon]
		}
		if sp := strings.IndexByte(pkg, ' '); sp >= 0 {
			pkg = pkg[:sp]
		}
		if strings.HasPrefix(pkg, "x/arch") {
			return nil
		}
		switch pkg {
		case "",
			"x/crypto/chacha20poly1305",
			"x/crypto/curve25519",
			"x/crypto/poly1305",
			"x/net/http2",
			"x/net/idna",
			"x/net/lif",
			"x/net/proxy",
			"x/net/route",
			"x/text/unicode/norm",
			"x/text/width":
			// These get vendored in. Don't mess with them.
			return nil
		}
		printIssue("subrepo-unreleased", gi)
		if *dryRun {
			return nil
		}
		_, _, err := b.ghc.Issues.Edit(ctx, "golang", "go", int(gi.Number), &github.IssueRequest{
			Milestone: github.Int(22), // "Unreleased"
		})
		return err
	})
}

func (b *gopherbot) setGccgoMilestones(ctx context.Context) error {
	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed || !gi.Milestone.IsNone() || gi.HasEvent("demilestoned") || gi.HasEvent("milestoned") {
			return nil
		}
		if !strings.Contains(gi.Title, "gccgo") { // TODO: better gccgo bug report heuristic?
			return nil
		}
		printIssue("gccgo-milestone", gi)
		if *dryRun {
			return nil
		}
		_, _, err := b.ghc.Issues.Edit(ctx, "golang", "go", int(gi.Number), &github.IssueRequest{
			Milestone: github.Int(23), // "Gccgo"
		})
		return err
	})
}

func (b *gopherbot) labelBuildIssues(ctx context.Context) error {
	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed || !strings.HasPrefix(gi.Title, "x/build") || gi.HasLabel("Builders") || gi.HasEvent("unlabeled") {
			return nil
		}
		printIssue("label-builders", gi)
		if *dryRun {
			return nil
		}
		return b.addLabel(ctx, gi, "Builders")
	})
}

func (b *gopherbot) labelDocumentationIssues(ctx context.Context) error {
	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed || !isDocumentationTitle(gi.Title) || gi.HasLabel("Documentation") || gi.HasEvent("unlabeled") {
			return nil
		}
		printIssue("label-documentation", gi)
		if *dryRun {
			return nil
		}
		return b.addLabel(ctx, gi, "Documentation")
	})
}

func (b *gopherbot) closeStaleWaitingForInfo(ctx context.Context) error {
	const waitingForInfo = "WaitingForInfo"
	now := time.Now()
	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed || !gi.HasLabel("WaitingForInfo") {
			return nil
		}
		var waitStart time.Time
		gi.ForeachEvent(func(e *maintner.GitHubIssueEvent) error {
			if e.Type == "reopened" {
				// Ignore any previous WaitingForInfo label if it's reopend.
				waitStart = time.Time{}
				return nil
			}
			if e.Label == waitingForInfo {
				switch e.Type {
				case "unlabeled":
					waitStart = time.Time{}
				case "labeled":
					waitStart = e.Created
				}
				return nil
			}
			return nil
		})
		if waitStart.IsZero() {
			return nil
		}

		deadline := waitStart.AddDate(0, 1, 0) // 1 month
		if now.Before(deadline) {
			return nil
		}

		var lastOPComment time.Time
		gi.ForeachComment(func(c *maintner.GitHubComment) error {
			if c.User.ID == gi.User.ID {
				lastOPComment = c.Created
			}
			return nil
		})
		if lastOPComment.After(waitStart) {
			return nil
		}

		printIssue("close-stale-waiting-for-info", gi)
		if *dryRun {
			return nil
		}

		// TODO: write a task that reopens issues if the OP speaks up.
		_, _, err := b.ghc.Issues.CreateComment(ctx, "golang", "go", int(gi.Number), &github.IssueComment{
			Body: github.String("Timed out in state WaitingForInfo. Closing.\n\n(I am just a bot, though. Please speak up if this is a mistake or you have the requested information.)"),
		})
		if err != nil {
			return err
		}
		_, _, err = b.ghc.Issues.Edit(ctx, "golang", "go", int(gi.Number), &github.IssueRequest{State: github.String("closed")})
		return err
	})

}

func isDocumentationTitle(t string) bool {
	if !strings.Contains(t, "doc") && !strings.Contains(t, "Doc") {
		return false
	}
	t = strings.ToLower(t)
	if strings.HasPrefix(t, "doc:") {
		return true
	}
	if strings.HasPrefix(t, "docs:") {
		return true
	}
	if strings.HasPrefix(t, "cmd/doc:") {
		return false
	}
	if strings.HasPrefix(t, "go/doc:") {
		return false
	}
	if strings.Contains(t, "godoc:") { // in x/tools, or the dozen places people file it as
		return false
	}
	return strings.Contains(t, "document") ||
		strings.Contains(t, "docs ")
}

var lastTask string

func printIssue(task string, gi *maintner.GitHubIssue) {
	if *dryRun {
		task = task + " [dry-run]"
	}
	if task != lastTask {
		fmt.Println(task)
		lastTask = task
	}
	fmt.Printf("\thttps://golang.org/issue/%v  %s\n", gi.Number, gi.Title)
}
