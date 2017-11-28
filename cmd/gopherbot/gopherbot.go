// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gopherbot command runs Go's gopherbot role account on
// GitHub and Gerrit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"github.com/google/go-github/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/oauth2"
)

var (
	dryRun          = flag.Bool("dry-run", false, "just report what would've been done, without changing anything")
	daemon          = flag.Bool("daemon", false, "run in daemon mode")
	githubTokenFile = flag.String("github-token-file", filepath.Join(os.Getenv("HOME"), "keys", "github-gobot"), `File to load Github token from. File should be of form <username>:<token>`)
	// go here: https://go-review.googlesource.com/settings#HTTPCredentials
	// click "Obtain Password"
	// The next page will have a .gitcookies file - look for the part that has
	// "git-youremail@yourcompany.com=password". Copy and paste that to the
	// token file with a colon in between the email and password.
	gerritTokenFile = flag.String("gerrit-token-file", filepath.Join(os.Getenv("HOME"), "keys", "gerrit-gobot"), `File to load Gerrit token from. File should be of form <git-email>:<token>`)
)

// GitHub Label IDs for the golang/go repo.
const (
	needsDecisionID      = 373401956
	needsFixID           = 373399998
	needsInvestigationID = 373402289
)

// Label names (that are used in multiple places).
const (
	frozenDueToAge = "FrozenDueToAge"
)

func getGithubToken() (string, error) {
	if metadata.OnGCE() {
		for _, key := range []string{"gopherbot-github-token", "maintner-github-token"} {
			token, err := metadata.ProjectAttributeValue(key)
			if err == nil {
				return token, nil
			}
		}
	}
	slurp, err := ioutil.ReadFile(*githubTokenFile)
	if err != nil {
		return "", err
	}
	f := strings.SplitN(strings.TrimSpace(string(slurp)), ":", 2)
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", fmt.Errorf("Expected token %q to be of form <username>:<token>", slurp)
	}
	return f[1], nil
}

func getGerritAuth() (username string, password string, err error) {
	var slurp string
	if metadata.OnGCE() {
		for _, key := range []string{"gopherbot-gerrit-token", "maintner-gerrit-token", "gobot-password"} {
			slurp, err = metadata.ProjectAttributeValue(key)
			if len(slurp) != 0 {
				continue
			}
		}
	}
	if len(slurp) == 0 {
		var slurpBytes []byte
		slurpBytes, err = ioutil.ReadFile(*gerritTokenFile)
		if err != nil {
			return "", "", err
		}
		slurp = string(slurpBytes)
	}
	f := strings.SplitN(strings.TrimSpace(slurp), ":", 2)
	if len(f) == 1 {
		// assume the whole thing is the token
		return "git-gobot.golang.org", f[0], nil
	}
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", "", fmt.Errorf("Expected Gerrit token %q to be of form <git-email>:<token>", slurp)
	}
	return f[0], f[1], nil
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

func getGerritClient() (*gerrit.Client, error) {
	username, token, err := getGerritAuth()
	if err != nil {
		return nil, err
	}
	c := gerrit.NewClient("https://go-review.googlesource.com", gerrit.BasicAuth(username, token))
	return c, nil
}

func init() {
	flag.Usage = func() {
		os.Stderr.WriteString("gopherbot runs Go's gopherbot role account on GitHub and Gerrit.\n\n")
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()

	ghc, err := getGithubClient()
	if err != nil {
		log.Fatal(err)
	}
	gerritc, err := getGerritClient()
	if err != nil {
		log.Fatal(err)
	}

	bot := &gopherbot{ghc: ghc, gerrit: gerritc}
	bot.initCorpus()

	ctx := context.Background()
	for {
		t0 := time.Now()
		err := bot.doTasks(ctx)
		if err != nil {
			log.Print(err)
		}
		botDur := time.Since(t0)
		log.Printf("gopherbot ran in %v", botDur)
		if !*daemon {
			if err != nil {
				os.Exit(1)
			}
			return
		}
		if err != nil {
			log.Printf("sleeping 30s after previous error.")
			time.Sleep(30 * time.Second)
		}
		for {
			t0 := time.Now()
			err := bot.corpus.Update(ctx)
			if err != nil {
				if err == maintner.ErrSplit {
					log.Print("Corpus out of sync. Re-fetching corpus.")
					bot.initCorpus()
				} else {
					log.Printf("corpus.Update: %v; sleeping 15s", err)
					time.Sleep(15 * time.Second)
					continue
				}
			}
			log.Printf("got corpus update after %v", time.Since(t0))
			break
		}
	}
}

type gopherbot struct {
	ghc    *github.Client
	gerrit *gerrit.Client
	corpus *maintner.Corpus
	gorepo *maintner.GitHubRepo

	// maxIssueMod is the latest modification time of all Go
	// github issues. It's updated at the end of the run of tasks.
	maxIssueMod       time.Time
	knownContributors map[string]bool
}

var tasks = []struct {
	name string
	fn   func(*gopherbot, context.Context) error
}{
	{"freeze old issues", (*gopherbot).freezeOldIssues},
	{"label proposals", (*gopherbot).labelProposals},
	{"set subrepo milestones", (*gopherbot).setSubrepoMilestones},
	{"set gccgo milestones", (*gopherbot).setGccgoMilestones},
	{"label build issues", (*gopherbot).labelBuildIssues},
	{"label mobile issues", (*gopherbot).labelMobileIssues},
	{"label documentation issues", (*gopherbot).labelDocumentationIssues},
	{"close stale WaitingForInfo", (*gopherbot).closeStaleWaitingForInfo},
	{"cl2issue", (*gopherbot).cl2issue},
	{"check cherry picks", (*gopherbot).checkCherryPicks},
	{"update needs", (*gopherbot).updateNeeds},
	{"congratulate new contributors", (*gopherbot).congratulateNewContributors},
}

func (b *gopherbot) initCorpus() {
	ctx := context.Background()
	corpus, err := godata.Get(ctx)
	if err != nil {
		log.Fatalf("godata.Get: %v", err)
	}

	repo := corpus.GitHub().Repo("golang", "go")
	if repo == nil {
		log.Fatal("Failed to find Go repo in Corpus.")
	}

	b.corpus = corpus
	b.gorepo = repo
}

func (b *gopherbot) doTasks(ctx context.Context) error {
	for _, task := range tasks {
		if err := task.fn(b, ctx); err != nil {
			log.Printf("%s: %v", task.name, err)
			return err
		}
	}

	// Update b.maxIssueMod.
	b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if t := gi.LastModified(); t.After(b.maxIssueMod) {
			b.maxIssueMod = t
		}
		return nil
	})

	return nil
}

func (b *gopherbot) addLabel(ctx context.Context, gi *maintner.GitHubIssue, label string) error {
	if *dryRun {
		return nil
	}
	_, _, err := b.ghc.Issues.AddLabelsToIssue(ctx, "golang", "go", int(gi.Number), []string{label})
	return err
}

// removeLabel makes an API call to GitHub to remove the provided
// label from the issue.
// If issue did not have the label already (or the label didn't
// exist), removeLabel returns nil.
func (b *gopherbot) removeLabel(ctx context.Context, gi *maintner.GitHubIssue, label string) error {
	if *dryRun {
		return nil
	}
	_, err := b.ghc.Issues.RemoveLabelForIssue(ctx, "golang", "go", int(gi.Number), label)
	if ge, ok := err.(*github.ErrorResponse); ok && ge.Response != nil && ge.Response.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func (b *gopherbot) addGitHubComment(ctx context.Context, org, repo string, issueNum int32, msg string) error {
	gr := b.corpus.GitHub().Repo(org, repo)
	if gr == nil {
		return fmt.Errorf("unknown github repo %s/%s", org, repo)
	}
	var since time.Time
	if gi := gr.Issue(issueNum); gi != nil {
		dup := false
		gi.ForeachComment(func(c *maintner.GitHubComment) error {
			since = c.Updated
			// TODO: check for gopherbot as author? check for exact match?
			// This seems fine for now.
			if strings.Contains(c.Body, msg) {
				dup = true
				return errStopIteration
			}
			return nil
		})
		if dup {
			// Comment's already been posted. Nothing to do.
			return nil
		}
	}
	// See if there is a dup comment from when gopherbot last got
	// its data from maintner.
	ics, _, err := b.ghc.Issues.ListComments(ctx, org, repo, int(issueNum), &github.IssueListCommentsOptions{
		Since:       since,
		ListOptions: github.ListOptions{PerPage: 1000},
	})
	if err != nil {
		return err
	}
	for _, ic := range ics {
		if strings.Contains(ic.GetBody(), msg) {
			// Dup.
			return nil
		}
	}
	if *dryRun {
		log.Printf("[dry run] would add comment to github.com/%s/%s/issues/%d: %v", org, repo, issueNum, msg)
		return nil
	}
	_, _, err = b.ghc.Issues.CreateComment(ctx, org, repo, int(issueNum), &github.IssueComment{
		Body: github.String(msg),
	})
	return err
}

type gerritCommentOpts struct {
	OldPhrases []string
	Version    string // if empty, latest version is used
}

var emptyGerritCommentOpts gerritCommentOpts

// addGerritComment adds the given comment to the CL specified by the changeID
// and the patch set identified by the version.
//
// As an idempotence check, before adding the comment the comment and the list
// of oldPhrases are checked against the CL to ensure that no phrase in the list
// has already been added to the list as a comment.
func (b *gopherbot) addGerritComment(ctx context.Context, changeID, comment string, opts *gerritCommentOpts) error {
	if b == nil {
		panic("nil gopherbot")
	}
	if opts == nil {
		opts = &emptyGerritCommentOpts
	}
	// One final staleness check before sending a message: get the list
	// of comments from the API and check whether any of them match.
	info, err := b.gerrit.GetChange(ctx, changeID, gerrit.QueryChangesOpt{
		Fields: []string{"MESSAGES", "CURRENT_REVISION"},
	})
	if err != nil {
		return err
	}
	for _, msg := range info.Messages {
		if strings.Contains(msg.Message, comment) {
			return nil // Our comment is already there
		}
		for j := range opts.OldPhrases {
			// Message looks something like "Patch set X:\n\n(our text)"
			if strings.Contains(msg.Message, opts.OldPhrases[j]) {
				return nil // Our comment is already there
			}
		}
	}
	var rev string
	if opts.Version != "" {
		rev = opts.Version
	} else {
		rev = info.CurrentRevision
	}
	return b.gerrit.SetReview(ctx, changeID, rev, gerrit.ReviewInput{
		Message: comment,
	})
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
		return b.addLabel(ctx, gi, frozenDueToAge)
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

func (b *gopherbot) labelMobileIssues(ctx context.Context) error {
	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed || !strings.HasPrefix(gi.Title, "x/mobile") || gi.HasLabel("mobile") || gi.HasEvent("unlabeled") {
			return nil
		}
		printIssue("label-mobile", gi)
		if *dryRun {
			return nil
		}
		return b.addLabel(ctx, gi, "mobile")
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
		if err := b.addGitHubComment(ctx, "golang", "go", gi.Number,
			"Timed out in state WaitingForInfo. Closing.\n\n(I am just a bot, though. Please speak up if this is a mistake or you have the requested information.)"); err != nil {
			return err
		}
		_, _, err := b.ghc.Issues.Edit(ctx, "golang", "go", int(gi.Number), &github.IssueRequest{State: github.String("closed")})
		return err
	})

}

// Issue 19776: assist with cherry picks based on Milestones
func (b *gopherbot) checkCherryPicks(ctx context.Context) error {
	// TODO(bradfitz): write this. Debugging stuff below only.
	return nil

	sum := map[string]int{}
	b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Milestone.IsNone() || gi.Milestone.IsUnknown() || gi.Milestone.Closed {
			return nil
		}
		title := gi.Milestone.Title
		if !strings.HasPrefix(title, "Go") || strings.Count(title, ".") != 2 {
			return nil
		}
		sum[title]++
		return nil
	})
	var titles []string
	for k := range sum {
		titles = append(titles, k)
	}
	sort.Slice(titles, func(i, j int) bool { return sum[titles[i]] < sum[titles[j]] })
	for _, title := range titles {
		fmt.Printf(" %10d %s\n", sum[title], title)
	}
	return nil
}

// cl2issue writes "Change https://golang.org/issue/NNNN mentions this issue"\
// and the change summary on GitHub when a new Gerrit change references a GitHub issue.
func (b *gopherbot) cl2issue(ctx context.Context) error {
	monthAgo := time.Now().Add(-30 * 24 * time.Hour)
	b.corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
			if cl.Meta == nil || cl.Meta.AuthorTime.Before(monthAgo) {
				// If the CL was last updated over a
				// month ago, assume (as an
				// optimization) that gopherbot
				// already processed this issue.
				return nil
			}
			for _, ref := range cl.GitHubIssueRefs {
				if id := ref.Repo.ID(); id.Owner != "golang" || id.Repo != "go" {
					continue
				}
				gi := ref.Repo.Issue(ref.Number)
				if gi == nil || gi.HasLabel(frozenDueToAge) {
					continue
				}
				hasComment := false
				substr := fmt.Sprintf("%d mentions this issue", cl.Number)
				gi.ForeachComment(func(c *maintner.GitHubComment) error {
					if strings.Contains(c.Body, substr) {
						hasComment = true
						return errStopIteration
					}
					return nil
				})
				if !hasComment {
					printIssue("cl2issue", gi)
					if *dryRun {
						return nil
					}
					msg := fmt.Sprintf("Change https://golang.org/cl/%d mentions this issue: `%s`", cl.Number, cl.Commit.Summary())
					if err := b.addGitHubComment(ctx, "golang", "go", gi.Number, msg); err != nil {
						return err
					}
				}
			}
			return nil
		})
	})
	return nil
}

// canonicalLabelName returns "needsfix" for "needs-fix" or "NeedsFix"
// in prep for future label renaming.
func canonicalLabelName(s string) string {
	return strings.Replace(strings.ToLower(s), "-", "", -1)
}

// If an issue has multiple "needs" labels, remove all but the most recent.
// These were originally called NeedsFix, NeedsDecision, and NeedsInvestigation,
// but are being renamed to "needs-foo".
func (b *gopherbot) updateNeeds(ctx context.Context) error {
	return b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed {
			return nil
		}
		var numNeeds int
		if gi.Labels[needsDecisionID] != nil {
			numNeeds++
		}
		if gi.Labels[needsFixID] != nil {
			numNeeds++
		}
		if gi.Labels[needsInvestigationID] != nil {
			numNeeds++
		}
		if numNeeds <= 1 {
			return nil
		}

		labels := map[string]int{} // lowercase no-hyphen "needsfix" -> position
		var pos, maxPos int
		gi.ForeachEvent(func(e *maintner.GitHubIssueEvent) error {
			var add bool
			switch e.Type {
			case "labeled":
				add = true
			case "unlabeled":
			default:
				return nil
			}
			if !strings.HasPrefix(e.Label, "Needs") && !strings.HasPrefix(e.Label, "needs-") {
				return nil
			}
			key := canonicalLabelName(e.Label)
			pos++
			if add {
				labels[key] = pos
				maxPos = pos
			} else {
				delete(labels, key)
			}
			return nil
		})
		if len(labels) <= 1 {
			return nil
		}

		// Remove any label that's not the newest (added in
		// last position).
		for _, lab := range gi.Labels {
			key := canonicalLabelName(lab.Name)
			if !strings.HasPrefix(key, "needs") || labels[key] == maxPos {
				continue
			}
			printIssue("updateneeds", gi)
			fmt.Printf("\t... removing label %q\n", lab.Name)
			if err := b.removeLabel(ctx, gi, lab.Name); err != nil {
				return err
			}
		}
		return nil
	})
}

// If any of the messages in this array have been posted on a CL, don't post
// again. If you amend the message even slightly, please prepend the new message
// to this list, to avoid re-spamming people.
//
// The first message is the "current" message.
var congratulatoryMessages = []string{
	// TODO: provide more helpful info? Amend, don't add 2nd commit, link to a
	// review guide?
	//
	// also TODO: make this a template? May want to provide more dynamic
	// information in the future. Would make it tougher to search and see if
	// a comment has been previously posted.
	`Congratulations on opening your first change. Thank you for your contribution!

Next steps:
Within the next week or so, a maintainer will review your change and provide
feedback. See https://golang.org/doc/contribute.html#review for more info and
tips to get your patch through code review.

Most changes in the Go project go through a few rounds of revision. This can be
surprising to people new to the project. The careful, iterative review process
is our way of helping mentor contributors and ensuring that their contributions
have a lasting impact.

During May-July and Nov-Jan the Go project is in a code freeze, during which
little code gets reviewed or merged. If a reviewer responds with a comment like
R=go1.11, it means that this CL will be reviewed as part of the next development
cycle. See https://golang.org/s/release for more details.`, // TODO only show freeze message during freeze
	"It's your first ever CL! Congrats, and thanks for sending!",
}

// Don't want to congratulate people on CL's they submitted a year ago.
var congratsEpoch = time.Date(2017, 6, 17, 0, 0, 0, 0, time.UTC)

func (b *gopherbot) congratulateNewContributors(ctx context.Context) error {
	cls := make(map[string]*maintner.GerritCL)
	b.corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
			// CLs can be returned by maintner in any order. Note also that
			// Gerrit CL numbers are sparse (CL N does not guarantee that CL N-1
			// exists) and Gerrit issues CL's out of order - it may issue CL N,
			// then CL (N - 18), then CL (N - 40).
			if b.knownContributors == nil {
				b.knownContributors = make(map[string]bool)
			}
			if cl.Commit == nil {
				return nil
			}
			email := cl.Commit.Author.Email()
			if email == "" {
				email = cl.Commit.Author.Str
			}
			if b.knownContributors[email] {
				return nil
			}
			if cls[email] != nil {
				// this person has multiple CLs; not a new contributor.
				b.knownContributors[email] = true
				delete(cls, email)
				return nil
			}
			cls[email] = cl
			return nil
		})
	})
	for email, cl := range cls {
		if cl.Commit == nil || cl.Commit.CommitTime.Before(congratsEpoch) {
			b.knownContributors[email] = true
			continue
		}
		if cl.Status == "merged" {
			b.knownContributors[email] = true
			continue
		}
		foundMessage := false
		for i := range cl.Messages {
			// TODO: once gopherbot starts posting these messages and we
			// have the author's name for Gopherbot, check the author name
			// matches as well.
			for j := range congratulatoryMessages {
				// Message looks something like "Patch set X:\n\n(our text)"
				if strings.Contains(cl.Messages[i].Message, congratulatoryMessages[j]) {
					foundMessage = true
					break
				}
			}
			if foundMessage {
				break
			}
		}

		if foundMessage {
			b.knownContributors[email] = true
			continue
		}
		if *dryRun {
			log.Printf("[dry run] would add comment to golang.org/cl/%d, congratulating %s on their first commit (committed on %v)", cl.Number, cl.Commit.Author.Str, cl.Commit.CommitTime)
			b.knownContributors[email] = true
			continue
		}
		opts := &gerritCommentOpts{
			OldPhrases: congratulatoryMessages,
		}
		err := b.addGerritComment(ctx, strconv.Itoa(int(cl.Number)), congratulatoryMessages[0], opts)
		if err != nil {
			return err
		}
		b.knownContributors[email] = true
	}
	return nil
}

// errStopIteration is used to stop iteration over issues or comments.
// It has no special meaning.
var errStopIteration = errors.New("stop iteration")

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
