// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gopherbot command runs Go's gopherbot role account on
// GitHub and Gerrit.
//
// General documentation is at https://go.dev/wiki/gopherbot.
// Consult the tasks slice in gopherbot.go for an up-to-date
// list of all gopherbot tasks.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"cloud.google.com/go/compute/metadata"
	"github.com/google/go-github/v48/github"
	"github.com/shurcooL/githubv4"
	"go4.org/strutil"
	"golang.org/x/build/devapp/owners"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/foreach"
	"golang.org/x/build/internal/gophers"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/exp/slices"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	dryRun          = flag.Bool("dry-run", false, "just report what would've been done, without changing anything")
	daemon          = flag.Bool("daemon", false, "run in daemon mode")
	githubTokenFile = flag.String("github-token-file", filepath.Join(os.Getenv("HOME"), "keys", "github-gobot"), `File to load GitHub token from. File should be of form <username>:<token>`)
	// go here: https://go-review.googlesource.com/settings#HTTPCredentials
	// click "Obtain Password"
	// The next page will have a .gitcookies file - look for the part that has
	// "git-youremail@yourcompany.com=password". Copy and paste that to the
	// token file with a colon in between the email and password.
	gerritTokenFile = flag.String("gerrit-token-file", filepath.Join(os.Getenv("HOME"), "keys", "gerrit-gobot"), `File to load Gerrit token from. File should be of form <git-email>:<token>`)

	onlyRun = flag.String("only-run", "", "if non-empty, the name of a task to run. Mostly for debugging, but tasks (like 'kicktrain') may choose to only run in explicit mode")
)

func init() {
	flag.Usage = func() {
		output := flag.CommandLine.Output()
		fmt.Fprintf(output, "gopherbot runs Go's gopherbot role account on GitHub and Gerrit.\n\n")
		flag.PrintDefaults()
		fmt.Fprintln(output, "")
		fmt.Fprintln(output, "Tasks (can be used for the --only-run flag):")
		for _, t := range tasks {
			fmt.Fprintf(output, "  %q\n", t.name)
		}
	}
}

const (
	gopherbotGitHubID = 8566911
)

const (
	gobotGerritID     = "5976"
	gerritbotGerritID = "12446"
	kokoroGerritID    = "37747"
	goLUCIGerritID    = "60063"
	triciumGerritID   = "62045"
)

// GitHub Label IDs for the golang/go repo.
const (
	needsDecisionID      = 373401956
	needsFixID           = 373399998
	needsInvestigationID = 373402289
	earlyInCycleID       = 626114143
)

// Label names (that are used in multiple places).
const (
	frozenDueToAge = "FrozenDueToAge"
)

// GitHub Milestone numbers for the golang/go repo.
var (
	proposal      = milestone{30, "Proposal"}
	unreleased    = milestone{22, "Unreleased"}
	unplanned     = milestone{6, "Unplanned"}
	gccgo         = milestone{23, "Gccgo"}
	vgo           = milestone{71, "vgo"}
	vulnUnplanned = milestone{288, "vuln/unplanned"}
)

// GitHub Milestone numbers for the golang/vscode-go repo.
var vscodeUntriaged = milestone{26, "Untriaged"}

type milestone struct {
	Number int
	Name   string
}

func getGitHubToken(ctx context.Context, sc *secret.Client) (string, error) {
	if metadata.OnGCE() && sc != nil {
		ctxSc, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		token, err := sc.Retrieve(ctxSc, secret.NameMaintnerGitHubToken)
		if err == nil && token != "" {
			return token, nil
		}
	}
	slurp, err := os.ReadFile(*githubTokenFile)
	if err != nil {
		return "", err
	}
	f := strings.SplitN(strings.TrimSpace(string(slurp)), ":", 2)
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", fmt.Errorf("expected token %q to be of form <username>:<token>", slurp)
	}
	return f[1], nil
}

func getGerritAuth(ctx context.Context, sc *secret.Client) (username string, password string, err error) {
	if metadata.OnGCE() && sc != nil {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		token, err := sc.Retrieve(ctx, secret.NameGobotPassword)
		if err != nil {
			return "", "", err
		}
		return "git-gobot.golang.org", token, nil
	}

	var slurpBytes []byte
	slurpBytes, err = os.ReadFile(*gerritTokenFile)
	if err != nil {
		return "", "", err
	}
	slurp := string(slurpBytes)

	f := strings.SplitN(strings.TrimSpace(slurp), ":", 2)
	if len(f) == 1 {
		// assume the whole thing is the token
		return "git-gobot.golang.org", f[0], nil
	}
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", "", fmt.Errorf("expected Gerrit token %q to be of form <git-email>:<token>", slurp)
	}
	return f[0], f[1], nil
}

func getGitHubClients(ctx context.Context, sc *secret.Client) (*github.Client, *githubv4.Client, error) {
	token, err := getGitHubToken(ctx, sc)
	if err != nil {
		if *dryRun {
			// Note: GitHub API v4 requires requests to be authenticated, which isn't implemented here.
			return github.NewClient(http.DefaultClient), githubv4.NewClient(http.DefaultClient), nil
		}
		return nil, nil, err
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return github.NewClient(tc), githubv4.NewClient(tc), nil
}

func getGerritClient(ctx context.Context, sc *secret.Client) (*gerrit.Client, error) {
	username, token, err := getGerritAuth(ctx, sc)
	if err != nil {
		if *dryRun {
			c := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
			return c, nil
		}
		return nil, err
	}
	c := gerrit.NewClient("https://go-review.googlesource.com", gerrit.BasicAuth(username, token))
	return c, nil
}

func getMaintnerClient(ctx context.Context) (apipb.MaintnerServiceClient, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	mServer := "maintner.golang.org:443"
	cc, err := grpc.DialContext(ctx, mServer,
		grpc.WithBlock(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{NextProtos: []string{"h2"}})))
	if err != nil {
		return nil, err
	}
	return apipb.NewMaintnerServiceClient(cc), nil
}

type gerritChange struct {
	project string
	num     int32
}

func (c gerritChange) ID() string {
	// https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-id
	return fmt.Sprintf("%s~%d", c.project, c.num)
}

func (c gerritChange) String() string {
	return c.ID()
}

type githubIssue struct {
	repo maintner.GitHubRepoID
	num  int32
}

func main() {
	flag.Parse()

	var sc *secret.Client
	if metadata.OnGCE() {
		sc = secret.MustNewClient()
	}
	ctx := context.Background()

	ghV3, ghV4, err := getGitHubClients(ctx, sc)
	if err != nil {
		log.Fatal(err)
	}
	gerrit, err := getGerritClient(ctx, sc)
	if err != nil {
		log.Fatal(err)
	}
	mc, err := getMaintnerClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	var goRepo = maintner.GitHubRepoID{Owner: "golang", Repo: "go"}
	var vscode = maintner.GitHubRepoID{Owner: "golang", Repo: "vscode-go"}
	bot := &gopherbot{
		ghc:    ghV3,
		ghV4:   ghV4,
		gerrit: gerrit,
		mc:     mc,
		is:     ghV3.Issues,
		deletedChanges: map[gerritChange]bool{
			{"crypto", 35958}:  true,
			{"scratch", 71730}: true,
			{"scratch", 71850}: true,
			{"scratch", 72090}: true,
			{"scratch", 72091}: true,
			{"scratch", 72110}: true,
			{"scratch", 72131}: true,
		},
		deletedIssues: map[githubIssue]bool{
			{goRepo, 13084}: true,
			{goRepo, 23772}: true,
			{goRepo, 27223}: true,
			{goRepo, 28522}: true,
			{goRepo, 29309}: true,
			{goRepo, 32047}: true,
			{goRepo, 32048}: true,
			{goRepo, 32469}: true,
			{goRepo, 32706}: true,
			{goRepo, 32737}: true,
			{goRepo, 33315}: true,
			{goRepo, 33316}: true,
			{goRepo, 33592}: true,
			{goRepo, 33593}: true,
			{goRepo, 33697}: true,
			{goRepo, 33785}: true,
			{goRepo, 34296}: true,
			{goRepo, 34476}: true,
			{goRepo, 34766}: true,
			{goRepo, 34780}: true,
			{goRepo, 34786}: true,
			{goRepo, 34821}: true,
			{goRepo, 35493}: true,
			{goRepo, 35649}: true,
			{goRepo, 36322}: true,
			{goRepo, 36323}: true,
			{goRepo, 36324}: true,
			{goRepo, 36342}: true,
			{goRepo, 36343}: true,
			{goRepo, 36406}: true,
			{goRepo, 36517}: true,
			{goRepo, 36829}: true,
			{goRepo, 36885}: true,
			{goRepo, 36933}: true,
			{goRepo, 36939}: true,
			{goRepo, 36941}: true,
			{goRepo, 36947}: true,
			{goRepo, 36962}: true,
			{goRepo, 36963}: true,
			{goRepo, 37516}: true,
			{goRepo, 37522}: true,
			{goRepo, 37582}: true,
			{goRepo, 37896}: true,
			{goRepo, 38132}: true,
			{goRepo, 38241}: true,
			{goRepo, 38483}: true,
			{goRepo, 38560}: true,
			{goRepo, 38840}: true,
			{goRepo, 39112}: true,
			{goRepo, 39141}: true,
			{goRepo, 39229}: true,
			{goRepo, 39234}: true,
			{goRepo, 39335}: true,
			{goRepo, 39401}: true,
			{goRepo, 39453}: true,
			{goRepo, 39522}: true,
			{goRepo, 39718}: true,
			{goRepo, 40400}: true,
			{goRepo, 40593}: true,
			{goRepo, 40600}: true,
			{goRepo, 41211}: true,
			{goRepo, 41268}: true, // transferred to https://github.com/golang/tour/issues/1042
			{goRepo, 41336}: true,
			{goRepo, 41649}: true,
			{goRepo, 41650}: true,
			{goRepo, 41655}: true,
			{goRepo, 41675}: true,
			{goRepo, 41676}: true,
			{goRepo, 41678}: true,
			{goRepo, 41679}: true,
			{goRepo, 41714}: true,
			{goRepo, 42309}: true,
			{goRepo, 43102}: true,
			{goRepo, 43169}: true,
			{goRepo, 43231}: true,
			{goRepo, 43330}: true,
			{goRepo, 43409}: true,
			{goRepo, 43410}: true,
			{goRepo, 43411}: true,
			{goRepo, 43433}: true,
			{goRepo, 43613}: true,
			{goRepo, 43751}: true,
			{goRepo, 44124}: true,
			{goRepo, 44185}: true,
			{goRepo, 44566}: true,
			{goRepo, 44652}: true,
			{goRepo, 44711}: true,
			{goRepo, 44768}: true,
			{goRepo, 44769}: true,
			{goRepo, 44771}: true,
			{goRepo, 44773}: true,
			{goRepo, 44871}: true,
			{goRepo, 45018}: true,
			{goRepo, 45082}: true,
			{goRepo, 45201}: true,
			{goRepo, 45202}: true,
			{goRepo, 47140}: true,
			{goRepo, 62987}: true,
			{goRepo, 67913}: true,

			{vscode, 298}:  true,
			{vscode, 524}:  true,
			{vscode, 650}:  true,
			{vscode, 741}:  true,
			{vscode, 773}:  true,
			{vscode, 959}:  true,
			{vscode, 1402}: true,
			{vscode, 2260}: true, // transferred to https://go.dev/issue/53080
			{vscode, 2548}: true,
			{vscode, 2781}: true, // transferred to https://go.dev/issue/60435
		},
	}
	for n := int32(55359); n <= 55828; n++ {
		bot.deletedIssues[githubIssue{goRepo, n}] = true
	}
	bot.initCorpus()

	for {
		t0 := time.Now()
		taskErrors := bot.doTasks(ctx)
		for _, err := range taskErrors {
			log.Print(err)
		}
		botDur := time.Since(t0)
		log.Printf("gopherbot ran in %v", botDur)
		if !*daemon {
			if len(taskErrors) > 0 {
				os.Exit(1)
			}
			return
		}
		if len(taskErrors) > 0 {
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
		lastTask = ""
	}
}

type gopherbot struct {
	ghc    *github.Client
	ghV4   *githubv4.Client
	gerrit *gerrit.Client
	mc     apipb.MaintnerServiceClient
	corpus *maintner.Corpus
	gorepo *maintner.GitHubRepo
	is     issuesService

	knownContributors map[string]bool

	// Until golang.org/issue/22635 is fixed, keep a map of changes and issues
	// that were deleted to prevent calls to Gerrit or GitHub that will always 404.
	deletedChanges map[gerritChange]bool
	deletedIssues  map[githubIssue]bool

	releases struct {
		sync.Mutex
		lastUpdate time.Time
		major      []string          // Last two releases and the next upcoming release, like: "1.9", "1.10", "1.11".
		nextMinor  map[string]string // Key is a major release like "1.9", value is its next minor release like "1.9.7".
	}
}

var tasks = []struct {
	name string
	fn   func(*gopherbot, context.Context) error
}{
	// Tasks that are specific to the golang/go repo.
	{"kicktrain", (*gopherbot).getOffKickTrain},
	{"label access issues", (*gopherbot).labelAccessIssues},
	{"label build issues", (*gopherbot).labelBuildIssues},
	{"label compiler/runtime issues", (*gopherbot).labelCompilerRuntimeIssues},
	{"label mobile issues", (*gopherbot).labelMobileIssues},
	{"label tools issues", (*gopherbot).labelToolsIssues},
	{"label website issues", (*gopherbot).labelWebsiteIssues},
	{"label pkgsite issues", (*gopherbot).labelPkgsiteIssues},
	{"label proxy.golang.org issues", (*gopherbot).labelProxyIssues},
	{"label vulncheck or vulndb issues", (*gopherbot).labelVulnIssues},
	{"label proposals", (*gopherbot).labelProposals},
	{"handle gopls issues", (*gopherbot).handleGoplsIssues},
	{"handle telemetry issues", (*gopherbot).handleTelemetryIssues},
	{"open cherry pick issues", (*gopherbot).openCherryPickIssues},
	{"close cherry pick issues", (*gopherbot).closeCherryPickIssues},
	{"close luci-config issues", (*gopherbot).closeLUCIConfigIssues},
	{"set subrepo milestones", (*gopherbot).setSubrepoMilestones},
	{"set misc milestones", (*gopherbot).setMiscMilestones},
	{"apply minor release milestones", (*gopherbot).setMinorMilestones},
	{"update needs", (*gopherbot).updateNeeds},

	// Tasks that can be applied to many repos.
	{"freeze old issues", (*gopherbot).freezeOldIssues},
	{"label documentation issues", (*gopherbot).labelDocumentationIssues},
	{"close stale WaitingForInfo", (*gopherbot).closeStaleWaitingForInfo},
	{"apply labels from comments", (*gopherbot).applyLabelsFromComments},

	// Gerrit tasks are applied to all projects by default.
	{"abandon scratch reviews", (*gopherbot).abandonScratchReviews},
	{"assign reviewers to CLs", (*gopherbot).assignReviewersToCLs},
	{"auto-submit CLs", (*gopherbot).autoSubmitCLs},

	// Tasks that are specific to the golang/vscode-go repo.
	{"set vscode-go milestones", (*gopherbot).setVSCodeGoMilestones},

	{"access", (*gopherbot).whoNeedsAccess},
	{"cl2issue", (*gopherbot).cl2issue},
	{"congratulate new contributors", (*gopherbot).congratulateNewContributors},
	{"un-wait CLs", (*gopherbot).unwaitCLs},
	{"convert wait-release topic to hashtag", (*gopherbot).topicToHashtag},
}

// gardenIssues reports whether GopherBot should perform general issue
// gardening tasks for the repo.
func gardenIssues(repo *maintner.GitHubRepo) bool {
	if repo.ID().Owner != "golang" {
		return false
	}
	switch repo.ID().Repo {
	case "go", "vscode-go", "vulndb", "oscar":
		return true
	}
	return false
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

// doTasks performs tasks in sequence. It doesn't stop if
// if encounters an error, but reports errors at the end.
func (b *gopherbot) doTasks(ctx context.Context) []error {
	var errs []error
	for _, task := range tasks {
		if *onlyRun != "" && task.name != *onlyRun {
			continue
		}
		err := task.fn(b, ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", task.name, err))
		}
	}
	return errs
}

// issuesService represents portions of github.IssuesService that we want to override in tests.
type issuesService interface {
	ListLabelsByIssue(ctx context.Context, owner string, repo string, number int, opt *github.ListOptions) ([]*github.Label, *github.Response, error)
	AddLabelsToIssue(ctx context.Context, owner string, repo string, number int, labels []string) ([]*github.Label, *github.Response, error)
	RemoveLabelForIssue(ctx context.Context, owner string, repo string, number int, label string) (*github.Response, error)
}

func (b *gopherbot) addLabel(ctx context.Context, repoID maintner.GitHubRepoID, gi *maintner.GitHubIssue, label string) error {
	return b.addLabels(ctx, repoID, gi, []string{label})
}

func (b *gopherbot) addLabels(ctx context.Context, repoID maintner.GitHubRepoID, gi *maintner.GitHubIssue, labels []string) error {
	var toAdd []string
	for _, label := range labels {
		if gi.HasLabel(label) {
			log.Printf("Issue %d already has label %q; no need to send request to add it", gi.Number, label)
			continue
		}
		printIssue("label-"+label, repoID, gi)
		toAdd = append(toAdd, label)
	}

	if *dryRun || len(toAdd) == 0 {
		return nil
	}

	_, resp, err := b.is.AddLabelsToIssue(ctx, repoID.Owner, repoID.Repo, int(gi.Number), toAdd)
	if err != nil && resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) {
		// TODO(golang/go#40640) - This issue was transferred or otherwise is gone. We should permanently skip it. This
		// is a temporary fix to keep gopherbot working.
		log.Printf("addLabels: Issue %v#%v returned %s when trying to add labels. Skipping. See golang/go#40640.", repoID, gi.Number, resp.Status)
		b.deletedIssues[githubIssue{repoID, gi.Number}] = true
		return nil
	}
	return err
}

// removeLabel removes the label from the given issue in the given repo.
func (b *gopherbot) removeLabel(ctx context.Context, repoID maintner.GitHubRepoID, gi *maintner.GitHubIssue, label string) error {
	return b.removeLabels(ctx, repoID, gi, []string{label})
}

func (b *gopherbot) removeLabels(ctx context.Context, repoID maintner.GitHubRepoID, gi *maintner.GitHubIssue, labels []string) error {
	var removeLabels bool
	for _, l := range labels {
		if !gi.HasLabel(l) {
			log.Printf("Issue %d (in maintner) does not have label %q; no need to send request to remove it", gi.Number, l)
			continue
		}
		printIssue("label-"+l, repoID, gi)
		removeLabels = true
	}

	if *dryRun || !removeLabels {
		return nil
	}

	ghLabels, err := labelsForIssue(ctx, repoID, b.is, int(gi.Number))
	if err != nil {
		return err
	}
	toRemove := make(map[string]bool)
	for _, l := range labels {
		toRemove[l] = true
	}

	for _, l := range ghLabels {
		if toRemove[l] {
			if err := removeLabelFromIssue(ctx, repoID, b.is, int(gi.Number), l); err != nil {
				log.Printf("Could not remove label %q from issue %d: %v", l, gi.Number, err)
				continue
			}
		}
	}
	return nil
}

// labelsForIssue returns all labels for the given issue in the given repo.
func labelsForIssue(ctx context.Context, repoID maintner.GitHubRepoID, issues issuesService, issueNum int) ([]string, error) {
	ghLabels, _, err := issues.ListLabelsByIssue(ctx, repoID.Owner, repoID.Repo, issueNum, &github.ListOptions{PerPage: 100})
	if err != nil {
		return nil, fmt.Errorf("could not list labels for %s#%d: %v", repoID, issueNum, err)
	}
	var labels []string
	for _, l := range ghLabels {
		labels = append(labels, l.GetName())
	}
	return labels, nil
}

// removeLabelFromIssue removes the given label from the given repo with the
// given issueNum. If the issue did not have the label already (or the label
// didn't exist), return nil.
func removeLabelFromIssue(ctx context.Context, repoID maintner.GitHubRepoID, issues issuesService, issueNum int, label string) error {
	_, err := issues.RemoveLabelForIssue(ctx, repoID.Owner, repoID.Repo, issueNum, label)
	if ge, ok := err.(*github.ErrorResponse); ok && ge.Response != nil && ge.Response.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func (b *gopherbot) setMilestone(ctx context.Context, repoID maintner.GitHubRepoID, gi *maintner.GitHubIssue, m milestone) error {
	printIssue("milestone-"+m.Name, repoID, gi)
	if *dryRun {
		return nil
	}
	_, resp, err := b.ghc.Issues.Edit(ctx, repoID.Owner, repoID.Repo, int(gi.Number), &github.IssueRequest{
		Milestone: github.Int(m.Number),
	})
	if err != nil && resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) {
		// An issue can become gone on GitHub without maintner realizing it. See go.dev/issue/30184.
		log.Printf("setMilestone: Issue %v#%v returned %s when trying to set milestone. Skipping. See go.dev/issue/30184.", repoID, gi.Number, resp.Status)
		b.deletedIssues[githubIssue{repoID, gi.Number}] = true
		return nil
	}
	return err
}

func (b *gopherbot) addGitHubComment(ctx context.Context, repo *maintner.GitHubRepo, issueNum int32, msg string) error {
	var since time.Time
	if gi := repo.Issue(issueNum); gi != nil {
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
	opt := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 1000}}
	if !since.IsZero() {
		opt.Since = &since
	}
	ics, resp, err := b.ghc.Issues.ListComments(ctx, repo.ID().Owner, repo.ID().Repo, int(issueNum), opt)
	if err != nil {
		// TODO(golang/go#40640) - This issue was transferred or otherwise is gone. We should permanently skip it. This
		// is a temporary fix to keep gopherbot working.
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			log.Printf("addGitHubComment: Issue %v#%v returned a 404 when trying to load comments. Skipping. See golang/go#40640.", repo.ID(), issueNum)
			b.deletedIssues[githubIssue{repo.ID(), issueNum}] = true
			return nil
		}
		return err
	}
	for _, ic := range ics {
		if strings.Contains(ic.GetBody(), msg) {
			// Dup.
			return nil
		}
	}
	if *dryRun {
		log.Printf("[dry-run] would add comment to github.com/%s/issues/%d: %v", repo.ID(), issueNum, msg)
		return nil
	}
	_, resp, createError := b.ghc.Issues.CreateComment(ctx, repo.ID().Owner, repo.ID().Repo, int(issueNum), &github.IssueComment{
		Body: github.String(msg),
	})
	if createError != nil && resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
		// While maintner's tracking of deleted issues is incomplete (see go.dev/issue/30184),
		// we sometimes see a deleted issue whose /comments endpoint returns 200 OK with an
		// empty list, so the error check from ListComments doesn't catch it. (The deleted
		// issue 55403 is an example of such a case.) So check again with the Get endpoint,
		// which seems to return 404 more reliably in such cases at least as of 2022-10-11.
		if _, resp, err := b.ghc.Issues.Get(ctx, repo.ID().Owner, repo.ID().Repo, int(issueNum)); err != nil &&
			resp != nil && resp.StatusCode == http.StatusNotFound {
			log.Printf("addGitHubComment: Issue %v#%v returned a 404 after posting comment failed with 422. Skipping. See go.dev/issue/30184.", repo.ID(), issueNum)
			b.deletedIssues[githubIssue{repo.ID(), issueNum}] = true
			return nil
		}
	}
	return createError
}

// createGitHubIssue returns the number of the created issue, or 4242 in dry-run mode.
// baseEvent is the timestamp of the event causing this action, and is used for de-duplication.
func (b *gopherbot) createGitHubIssue(ctx context.Context, title, msg string, labels []string, baseEvent time.Time) (int, error) {
	var dup int
	b.gorepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		// TODO: check for gopherbot as author? check for exact match?
		// This seems fine for now.
		if gi.Title == title {
			dup = int(gi.Number)
			return errStopIteration
		}
		return nil
	})
	if dup != 0 {
		// Issue's already been posted. Nothing to do.
		return dup, nil
	}
	// See if there is a dup issue from when gopherbot last got its data from maintner.
	is, _, err := b.ghc.Issues.ListByRepo(ctx, "golang", "go", &github.IssueListByRepoOptions{
		State:       "all",
		ListOptions: github.ListOptions{PerPage: 100},
		Since:       baseEvent,
	})
	if err != nil {
		return 0, err
	}
	for _, i := range is {
		if i.GetTitle() == title {
			// Dup.
			return i.GetNumber(), nil
		}
	}
	if *dryRun {
		log.Printf("[dry-run] would create issue with title %s and labels %v\n%s", title, labels, msg)
		return 4242, nil
	}
	i, _, err := b.ghc.Issues.Create(ctx, "golang", "go", &github.IssueRequest{
		Title:  github.String(title),
		Body:   github.String(msg),
		Labels: &labels,
	})
	return i.GetNumber(), err
}

// issueCloseReason is a reason given when closing an issue on GitHub.
// See https://docs.github.com/en/issues/tracking-your-work-with-issues/closing-an-issue.
type issueCloseReason *string

var (
	completed  issueCloseReason = github.String("completed")   // Done, closed, fixed, resolved.
	notPlanned issueCloseReason = github.String("not_planned") // Won't fix, can't repro, duplicate, stale.
)

// closeGitHubIssue closes a GitHub issue.
// reason specifies why it's being closed. (GitHub's default reason on 2023-06-12 is "completed".)
func (b *gopherbot) closeGitHubIssue(ctx context.Context, repoID maintner.GitHubRepoID, number int32, reason issueCloseReason) error {
	if *dryRun {
		var suffix string
		if reason != nil {
			suffix = " as " + *reason
		}
		log.Printf("[dry-run] would close go.dev/issue/%v%s", number, suffix)
		return nil
	}
	_, _, err := b.ghc.Issues.Edit(ctx, repoID.Owner, repoID.Repo, int(number), &github.IssueRequest{
		State:       github.String("closed"),
		StateReason: reason,
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
// As an idempotence check, before adding the comment and the list
// of oldPhrases are checked against the CL to ensure that no phrase in the list
// has already been added to the list as a comment.
func (b *gopherbot) addGerritComment(ctx context.Context, changeID, comment string, opts *gerritCommentOpts) error {
	if b == nil {
		panic("nil gopherbot")
	}
	if *dryRun {
		log.Printf("[dry-run] would add comment to golang.org/cl/%s: %v", changeID, comment)
		return nil
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

// Move any issue to "Unplanned" if it looks like it keeps getting kicked along between releases.
func (b *gopherbot) getOffKickTrain(ctx context.Context) error {
	// We only run this task if it was explicitly requested via
	// the --only-run flag.
	if *onlyRun == "" {
		return nil
	}
	type match struct {
		url   string
		title string
		gi    *maintner.GitHubIssue
	}
	var matches []match
	b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		curMilestone := gi.Milestone.Title
		if !strings.HasPrefix(curMilestone, "Go1.") || strings.Count(curMilestone, ".") != 1 {
			return nil
		}
		if gi.HasLabel("release-blocker") || gi.HasLabel("Security") {
			return nil
		}
		if len(gi.Assignees) > 0 {
			return nil
		}
		was := map[string]bool{}
		gi.ForeachEvent(func(e *maintner.GitHubIssueEvent) error {
			if e.Type == "milestoned" {
				switch e.Milestone {
				case "Unreleased", "Unplanned", "Proposal":
					return nil
				}
				if strings.Count(e.Milestone, ".") > 1 {
					return nil
				}
				ms := strings.TrimSuffix(e.Milestone, "Maybe")
				ms = strings.TrimSuffix(ms, "Early")
				was[ms] = true
			}
			return nil
		})
		if len(was) > 2 {
			var mss []string
			for ms := range was {
				mss = append(mss, ms)
			}
			sort.Slice(mss, func(i, j int) bool {
				if len(mss[i]) == len(mss[j]) {
					return mss[i] < mss[j]
				}
				return len(mss[i]) < len(mss[j])
			})
			matches = append(matches, match{
				url:   fmt.Sprintf("https://go.dev/issue/%d", gi.Number),
				title: fmt.Sprintf("%s - %v", gi.Title, mss),
				gi:    gi,
			})
		}
		return nil
	})
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].title < matches[j].title
	})
	fmt.Printf("%d issues:\n", len(matches))
	for _, m := range matches {
		fmt.Printf("%-30s - %s\n", m.url, m.title)
		if !*dryRun {
			if err := b.setMilestone(ctx, b.gorepo.ID(), m.gi, unplanned); err != nil {
				return err
			}
		}
	}
	return nil
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
	return b.corpus.GitHub().ForeachRepo(func(repo *maintner.GitHubRepo) error {
		if !gardenIssues(repo) {
			return nil
		}
		if !repoHasLabel(repo, frozenDueToAge) {
			return nil
		}
		return b.foreachIssue(repo, closed, func(gi *maintner.GitHubIssue) error {
			if gi.Locked || gi.Updated.After(tooOld) {
				return nil
			}
			printIssue("freeze", repo.ID(), gi)
			if *dryRun {
				return nil
			}
			_, err := b.ghc.Issues.Lock(ctx, repo.ID().Owner, repo.ID().Repo, int(gi.Number), nil)
			if ge, ok := err.(*github.ErrorResponse); ok && ge.Response.StatusCode == http.StatusNotFound {
				// An issue can become 404 on GitHub due to being deleted or transferred. See go.dev/issue/30182.
				b.deletedIssues[githubIssue{repo.ID(), gi.Number}] = true
				return nil
			} else if err != nil {
				return err
			}
			return b.addLabel(ctx, repo.ID(), gi, frozenDueToAge)
		})
	})
}

// labelProposals adds the "Proposal" label and "Proposal" milestone
// to open issues with title beginning with "Proposal:". It tries not
// to get into an edit war with a human.
func (b *gopherbot) labelProposals(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !strings.HasPrefix(gi.Title, "proposal:") && !strings.HasPrefix(gi.Title, "Proposal:") {
			return nil
		}
		// Add Proposal label if missing:
		if !gi.HasLabel("Proposal") && !gi.HasEvent("unlabeled") {
			if err := b.addLabel(ctx, b.gorepo.ID(), gi, "Proposal"); err != nil {
				return err
			}
		}
		// Add Milestone if missing:
		if gi.Milestone.IsNone() && !gi.HasEvent("milestoned") && !gi.HasEvent("demilestoned") {
			if err := b.setMilestone(ctx, b.gorepo.ID(), gi, proposal); err != nil {
				return err
			}
		}

		// Remove NeedsDecision label if exists, but not for Go 2 issues:
		if !isGo2Issue(gi) && gi.HasLabel("NeedsDecision") && !gopherbotRemovedLabel(gi, "NeedsDecision") {
			if err := b.removeLabel(ctx, b.gorepo.ID(), gi, "NeedsDecision"); err != nil {
				return err
			}
		}
		return nil
	})
}

// gopherbotRemovedLabel reports whether gopherbot has
// previously removed label in the GitHub issue gi.
//
// Note that until golang.org/issue/28226 is resolved,
// there's a brief delay before maintner catches up on
// GitHub issue events and learns that it has happened.
func gopherbotRemovedLabel(gi *maintner.GitHubIssue, label string) bool {
	var hasRemoved bool
	gi.ForeachEvent(func(e *maintner.GitHubIssueEvent) error {
		if e.Actor != nil && e.Actor.ID == gopherbotGitHubID &&
			e.Type == "unlabeled" &&
			e.Label == label {
			hasRemoved = true
			return errStopIteration
		}
		return nil
	})
	return hasRemoved
}

// isGo2Issue reports whether gi seems like it's about Go 2, based on either labels or its title.
func isGo2Issue(gi *maintner.GitHubIssue) bool {
	if gi.HasLabel("Go2") {
		return true
	}
	if !strings.Contains(gi.Title, "2") {
		// Common case.
		return false
	}
	return strings.Contains(gi.Title, "Go 2") || strings.Contains(gi.Title, "go2") || strings.Contains(gi.Title, "Go2")
}

func (b *gopherbot) setSubrepoMilestones(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !gi.Milestone.IsNone() || gi.HasEvent("demilestoned") || gi.HasEvent("milestoned") {
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
		switch pkg {
		case "",
			"x/arch",
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
		case "x/vgo":
			// Handled by setMiscMilestones
			return nil
		}
		return b.setMilestone(ctx, b.gorepo.ID(), gi, unreleased)
	})
}

func (b *gopherbot) setMiscMilestones(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !gi.Milestone.IsNone() || gi.HasEvent("demilestoned") || gi.HasEvent("milestoned") {
			return nil
		}
		if strings.Contains(gi.Title, "gccgo") { // TODO: better gccgo bug report heuristic?
			return b.setMilestone(ctx, b.gorepo.ID(), gi, gccgo)
		}
		if strings.HasPrefix(gi.Title, "x/vgo") {
			return b.setMilestone(ctx, b.gorepo.ID(), gi, vgo)
		}
		if strings.HasPrefix(gi.Title, "x/vuln") {
			return b.setMilestone(ctx, b.gorepo.ID(), gi, vulnUnplanned)
		}
		return nil
	})
}

func (b *gopherbot) setVSCodeGoMilestones(ctx context.Context) error {
	vscode := b.corpus.GitHub().Repo("golang", "vscode-go")
	if vscode == nil {
		return nil
	}
	return b.foreachIssue(vscode, open, func(gi *maintner.GitHubIssue) error {
		if !gi.Milestone.IsNone() || gi.HasEvent("demilestoned") || gi.HasEvent("milestoned") {
			return nil
		}
		// Work-around golang/go#40640 by only milestoning new issues.
		if time.Since(gi.Created) > 24*time.Hour {
			return nil
		}
		return b.setMilestone(ctx, vscode.ID(), gi, vscodeUntriaged)
	})
}

func (b *gopherbot) labelAccessIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !strings.HasPrefix(gi.Title, "access: ") || gi.HasLabel("Access") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "Access")
	})
}

func (b *gopherbot) labelBuildIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !strings.HasPrefix(gi.Title, "x/build") || gi.HasLabel("Builders") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "Builders")
	})
}

func (b *gopherbot) labelCompilerRuntimeIssues(ctx context.Context) error {
	entries, err := getAllCodeOwners(ctx)
	if err != nil {
		return err
	}
	// Filter out any entries that don't contain compiler/runtime owners into
	// a set of compiler/runtime-owned packages whose names match the names
	// used in the issue tracker.
	crtPackages := make(map[string]struct{}) // Key is issue title prefix, like "cmd/compile" or "x/sys/unix."
	for pkg, entry := range entries {
		for _, owner := range entry.Primary {
			name := owner.GitHubUsername
			if name == "golang/compiler" || name == "golang/runtime" {
				crtPackages[owners.TranslatePathForIssues(pkg)] = struct{}{}
				break
			}
		}
	}
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if gi.HasLabel("compiler/runtime") || gi.HasEvent("unlabeled") {
			return nil
		}
		components := strings.SplitN(gi.Title, ":", 2)
		if len(components) != 2 {
			return nil
		}
		for _, p := range strings.Split(strings.TrimSpace(components[0]), ",") {
			if _, ok := crtPackages[strings.TrimSpace(p)]; !ok {
				continue
			}
			// TODO(mknyszek): Add this issue to the GitHub project as well.
			return b.addLabel(ctx, b.gorepo.ID(), gi, "compiler/runtime")
		}
		return nil
	})
}

func (b *gopherbot) labelMobileIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !strings.HasPrefix(gi.Title, "x/mobile") || gi.HasLabel("mobile") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "mobile")
	})
}

func (b *gopherbot) labelDocumentationIssues(ctx context.Context) error {
	const documentation = "Documentation"
	return b.corpus.GitHub().ForeachRepo(func(repo *maintner.GitHubRepo) error {
		if !gardenIssues(repo) {
			return nil
		}
		if !repoHasLabel(repo, documentation) {
			return nil
		}
		return b.foreachIssue(repo, open, func(gi *maintner.GitHubIssue) error {
			if !isDocumentationTitle(gi.Title) || gi.HasLabel("Documentation") || gi.HasEvent("unlabeled") {
				return nil
			}
			return b.addLabel(ctx, repo.ID(), gi, documentation)
		})
	})
}

func (b *gopherbot) labelToolsIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !strings.HasPrefix(gi.Title, "x/tools") || gi.HasLabel("Tools") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "Tools")
	})
}

func (b *gopherbot) labelWebsiteIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		hasWebsiteTitle := strings.HasPrefix(gi.Title, "x/website:")
		if !hasWebsiteTitle || gi.HasLabel("website") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "website")
	})
}

func (b *gopherbot) labelPkgsiteIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		hasPkgsiteTitle := strings.HasPrefix(gi.Title, "x/pkgsite:")
		if !hasPkgsiteTitle || gi.HasLabel("pkgsite") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "pkgsite")
	})
}

func (b *gopherbot) labelProxyIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		hasProxyTitle := strings.Contains(gi.Title, "proxy.golang.org") || strings.Contains(gi.Title, "sum.golang.org") || strings.Contains(gi.Title, "index.golang.org")
		if !hasProxyTitle || gi.HasLabel("proxy.golang.org") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "proxy.golang.org")
	})
}

func (b *gopherbot) labelVulnIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		hasVulnTitle := strings.HasPrefix(gi.Title, "x/vuln:") || strings.HasPrefix(gi.Title, "x/vuln/") ||
			strings.HasPrefix(gi.Title, "x/vulndb:") || strings.HasPrefix(gi.Title, "x/vulndb/")
		if !hasVulnTitle || gi.HasLabel("vulncheck or vulndb") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "vulncheck or vulndb")
	})
}

// handleGoplsIssues labels and asks for additional information on gopls issues.
//
// This is necessary because gopls issues often require additional information to diagnose,
// and we don't ask for this information in the Go issue template.
func (b *gopherbot) handleGoplsIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !isGoplsTitle(gi.Title) || gi.HasLabel("gopls") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "gopls")
	})
}

func (b *gopherbot) handleTelemetryIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !strings.HasPrefix(gi.Title, "x/telemetry") || gi.HasLabel("telemetry") || gi.HasEvent("unlabeled") {
			return nil
		}
		return b.addLabel(ctx, b.gorepo.ID(), gi, "telemetry")
	})
}

func (b *gopherbot) closeStaleWaitingForInfo(ctx context.Context) error {
	const waitingForInfo = "WaitingForInfo"
	now := time.Now()
	return b.corpus.GitHub().ForeachRepo(func(repo *maintner.GitHubRepo) error {
		if !gardenIssues(repo) {
			return nil
		}
		if !repoHasLabel(repo, waitingForInfo) {
			return nil
		}
		return b.foreachIssue(repo, open, func(gi *maintner.GitHubIssue) error {
			if !gi.HasLabel(waitingForInfo) {
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
			if gi.HasLabel("CherryPickCandidate") || gi.HasLabel("CherryPickApproved") {
				// Cherry-pick issues may sometimes need to wait while
				// fixes get prepared and soak, so give them more time.
				deadline = waitStart.AddDate(0, 6, 0)
			}
			if repo.ID().Repo == "vscode-go" && gi.HasLabel("automatedReport") {
				// Automated issue reports have low response rates.
				// Apply shorter timeout.
				deadline = waitStart.AddDate(0, 0, 7)
			}
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

			printIssue("close-stale-waiting-for-info", repo.ID(), gi)
			// TODO: write a task that reopens issues if the OP speaks up.
			if err := b.addGitHubComment(ctx, repo, gi.Number,
				"Timed out in state WaitingForInfo. Closing.\n\n(I am just a bot, though. Please speak up if this is a mistake or you have the requested information.)"); err != nil {
				return fmt.Errorf("b.addGitHubComment(_, %v, %v) = %w", repo.ID(), gi.Number, err)
			}
			return b.closeGitHubIssue(ctx, repo.ID(), gi.Number, notPlanned)
		})
	})
}

// cl2issue writes "Change https://go.dev/cl/NNNN mentions this issue"
// and the change summary on GitHub when a new Gerrit change references a GitHub issue.
func (b *gopherbot) cl2issue(ctx context.Context) error {
	monthAgo := time.Now().Add(-30 * 24 * time.Hour)
	return b.corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
			if cl.Meta.Commit.AuthorTime.Before(monthAgo) {
				// If the CL was last updated over a
				// month ago, assume (as an
				// optimization) that gopherbot
				// already processed this issue.
				return nil
			}
			for _, ref := range cl.GitHubIssueRefs {
				if !gardenIssues(ref.Repo) {
					continue
				}
				gi := ref.Repo.Issue(ref.Number)
				if gi == nil || gi.NotExist || gi.PullRequest || gi.Locked || b.deletedIssues[githubIssue{ref.Repo.ID(), gi.Number}] {
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
				if hasComment {
					continue
				}
				printIssue("cl2issue", ref.Repo.ID(), gi)
				msg := fmt.Sprintf("Change https://go.dev/cl/%d mentions this issue: `%s`", cl.Number, cl.Commit.Summary())
				if err := b.addGitHubComment(ctx, ref.Repo, gi.Number, msg); err != nil {
					return err
				}
			}
			return nil
		})
	})
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
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
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
			printIssue("updateneeds", b.gorepo.ID(), gi)
			fmt.Printf("\t... removing label %q\n", lab.Name)
			if err := b.removeLabel(ctx, b.gorepo.ID(), gi, lab.Name); err != nil {
				return err
			}
		}
		return nil
	})
}

// TODO: Improve this message. Some ideas:
//
// Provide more helpful info? Amend, don't add 2nd commit, link to a review guide?
// Make this a template? May want to provide more dynamic information in the future.
// Only show freeze message during freeze.
const (
	congratsSentence = `Congratulations on opening your first change. Thank you for your contribution!`

	defaultCongratsMsg = congratsSentence + `

Next steps:
A maintainer will review your change and provide feedback. See
https://go.dev/doc/contribute#review for more info and tips to get your
patch through code review.

Most changes in the Go project go through a few rounds of revision. This can be
surprising to people new to the project. The careful, iterative review process
is our way of helping mentor contributors and ensuring that their contributions
have a lasting impact.`

	// Not all x/ repos are subject to the freeze, and so shouldn't get the
	// warning about it. See isSubjectToFreeze for the complete list.
	freezeCongratsMsg = defaultCongratsMsg + `

During May-July and Nov-Jan the Go project is in a code freeze, during which
little code gets reviewed or merged. If a reviewer responds with a comment like
R=go1.11 or adds a tag like "wait-release", it means that this CL will be
reviewed as part of the next development cycle. See https://go.dev/s/release
for more details.`
)

// If messages containing any of the sentences in this array have been posted
// on a CL, don't post again. If you amend the message even slightly, please
// prepend the new message to this list, to avoid re-spamming people.
//
// The first message is the "current" message.
var oldCongratsMsgs = []string{
	congratsSentence,
	`It's your first ever CL! Congrats, and thanks for sending!`,
}

// isSubjectToFreeze returns true if a repository is subject to the release
// freeze. x/ repos can be subject if they are vendored into golang/go.
func isSubjectToFreeze(repo string) bool {
	switch repo {
	case "go": // main repo
		return true
	case "crypto", "net", "sys", "text": // vendored x/ repos
		return true
	}
	return false
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
		// See golang.org/issue/23865
		if cl.Branch() == "refs/meta/config" {
			b.knownContributors[email] = true
			continue
		}
		if cl.Commit == nil || cl.Commit.CommitTime.Before(congratsEpoch) {
			b.knownContributors[email] = true
			continue
		}
		if cl.Status == "merged" {
			b.knownContributors[email] = true
			continue
		}
		foundMessage := false
		congratulatoryMessage := defaultCongratsMsg
		if isSubjectToFreeze(cl.Project.Project()) {
			congratulatoryMessage = freezeCongratsMsg
		}
		for i := range cl.Messages {
			// TODO: once gopherbot starts posting these messages and we
			// have the author's name for Gopherbot, check the author name
			// matches as well.
			for j := range oldCongratsMsgs {
				// Message looks something like "Patch set X:\n\n(our text)"
				if strings.Contains(cl.Messages[i].Message, oldCongratsMsgs[j]) {
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
		// Don't add all of the old congratulatory messages here, since we've
		// already checked for them above.
		opts := &gerritCommentOpts{
			OldPhrases: []string{congratulatoryMessage},
		}
		err := b.addGerritComment(ctx, cl.ChangeID(), congratulatoryMessage, opts)
		if err != nil {
			return fmt.Errorf("could not add comment to golang.org/cl/%d: %v", cl.Number, err)
		}
		b.knownContributors[email] = true
	}
	return nil
}

// unwaitCLs removes wait-* hashtags from CLs.
func (b *gopherbot) unwaitCLs(ctx context.Context) error {
	return b.corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			tags := cl.Meta.Hashtags()
			if tags.Len() == 0 {
				return nil
			}
			// If the CL is tagged "wait-author", remove
			// that tag if the author has replied since
			// the last time the "wait-author" tag was
			// added.
			if tags.Contains("wait-author") {
				// Figure out the last index at which "wait-author" was added.
				waitAuthorIndex := -1
				for i := len(cl.Metas) - 1; i >= 0; i-- {
					if cl.Metas[i].HashtagsAdded().Contains("wait-author") {
						waitAuthorIndex = i
						break
					}
				}

				// Find out whether the author has replied since.
				authorEmail := cl.Metas[0].Commit.Author.Email() // Equivalent to "{{cl.OwnerID}}@62eb7196-b449-3ce5-99f1-c037f21e1705".
				hasReplied := false
				for _, m := range cl.Metas[waitAuthorIndex+1:] {
					if m.Commit.Author.Email() == authorEmail {
						hasReplied = true
						break
					}
				}
				if hasReplied {
					log.Printf("https://go.dev/cl/%d -- remove wait-author; reply from %s", cl.Number, cl.Owner())
					err := b.onLatestCL(ctx, cl, func() error {
						if *dryRun {
							log.Printf("[dry run] would remove hashtag 'wait-author' from CL %d", cl.Number)
							return nil
						}
						_, err := b.gerrit.RemoveHashtags(ctx, fmt.Sprint(cl.Number), "wait-author")
						if err != nil {
							log.Printf("https://go.dev/cl/%d: error removing wait-author: %v", cl.Number, err)
							return err
						}
						log.Printf("https://go.dev/cl/%d: removed wait-author", cl.Number)
						return nil
					})
					if err != nil {
						return err
					}
				}
			}
			return nil
		})
	})
}

// topicToHashtag converts CLs with 'wait-release' topic
// to the likely intended uses of 'wait-release' hashtag.
func (b *gopherbot) topicToHashtag(ctx context.Context) error {
	waitTopicCLs, err := b.gerrit.QueryChanges(ctx, "status:open topic:wait-release")
	if err != nil {
		return err
	}
	for _, cl := range waitTopicCLs {
		if *dryRun {
			log.Printf("[dry run] would replace 'wait-release' topic with hashtag on CL %d (%.32s…)", cl.ChangeNumber, cl.Subject)
			continue
		}
		_, err := b.gerrit.AddHashtags(ctx, cl.ID, "wait-release")
		if err != nil {
			return err
		}
		err = b.gerrit.DeleteTopic(ctx, cl.ID)
		if err != nil {
			return err
		}
		log.Printf("https://go.dev/cl/%d: replaced 'wait-release' topic with hashtag", cl.ChangeNumber)
	}
	return nil
}

// onLatestCL checks whether cl's metadata is up to date with Gerrit's
// upstream data and, if so, returns f(). If it's out of date, it does
// nothing more and returns nil.
func (b *gopherbot) onLatestCL(ctx context.Context, cl *maintner.GerritCL, f func() error) error {
	ci, err := b.gerrit.GetChangeDetail(ctx, fmt.Sprint(cl.Number), gerrit.QueryChangesOpt{Fields: []string{"MESSAGES"}})
	if err != nil {
		return err
	}
	if len(ci.Messages) == 0 {
		log.Printf("onLatestCL: CL %d has no messages. Odd. Ignoring.", cl.Number)
		return nil
	}
	latestGerritID := ci.Messages[len(ci.Messages)-1].ID
	// Check all metas and not just the latest, because there are some meta commits
	// that don't have a corresponding message in the Gerrit REST API response.
	for i := len(cl.Metas) - 1; i >= 0; i-- {
		metaHash := cl.Metas[i].Commit.Hash.String()
		if metaHash == latestGerritID {
			// latestGerritID is contained by maintner metadata for this CL, so run f().
			return f()
		}
	}
	log.Printf("onLatestCL: maintner metadata for CL %d is behind; skipping action for now.", cl.Number)
	return nil
}

// fetchReleases returns the two most recent major Go 1.x releases, and
// the next upcoming release, sorted and formatted like []string{"1.9", "1.10", "1.11"}.
// It also returns the next minor release for each major release,
// like map[string]string{"1.9": "1.9.7", "1.10": "1.10.4", "1.11": "1.11.1"}.
//
// The data returned is fetched from Maintner Service occasionally
// and cached for some time.
func (b *gopherbot) fetchReleases(ctx context.Context) (major []string, nextMinor map[string]string, _ error) {
	b.releases.Lock()
	defer b.releases.Unlock()

	if expiry := b.releases.lastUpdate.Add(10 * time.Minute); time.Now().Before(expiry) {
		return b.releases.major, b.releases.nextMinor, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := b.mc.ListGoReleases(ctx, &apipb.ListGoReleasesRequest{})
	if err != nil {
		return nil, nil, err
	}
	rs := resp.Releases // Supported Go releases, sorted with latest first.

	nextMinor = make(map[string]string)
	for i := len(rs) - 1; i >= 0; i-- {
		x, y, z := rs[i].Major, rs[i].Minor, rs[i].Patch
		major = append(major, fmt.Sprintf("%d.%d", x, y))
		nextMinor[fmt.Sprintf("%d.%d", x, y)] = fmt.Sprintf("%d.%d.%d", x, y, z+1)
	}
	// Include the next release in the list of major releases.
	if len(rs) > 0 {
		// Assume the next major release after Go X.Y is Go X.(Y+1). This is true more often than not.
		nextX, nextY := rs[0].Major, rs[0].Minor+1
		major = append(major, fmt.Sprintf("%d.%d", nextX, nextY))
		nextMinor[fmt.Sprintf("%d.%d", nextX, nextY)] = fmt.Sprintf("%d.%d.1", nextX, nextY)
	}

	b.releases.major = major
	b.releases.nextMinor = nextMinor
	b.releases.lastUpdate = time.Now()

	return major, nextMinor, nil
}

// openCherryPickIssues opens CherryPickCandidate issues for backport when
// asked on the main issue.
func (b *gopherbot) openCherryPickIssues(ctx context.Context) error {
	return b.foreachIssue(b.gorepo, open|closed|includePRs, func(gi *maintner.GitHubIssue) error {
		if gi.HasLabel("CherryPickApproved") && gi.HasLabel("CherryPickCandidate") {
			if err := b.removeLabel(ctx, b.gorepo.ID(), gi, "CherryPickCandidate"); err != nil {
				return err
			}
		}
		if gi.Locked || gi.PullRequest {
			return nil
		}
		var backportComment *maintner.GitHubComment
		if err := gi.ForeachComment(func(c *maintner.GitHubComment) error {
			if strings.HasPrefix(c.Body, "Backport issue(s) opened") {
				backportComment = nil
				return errStopIteration
			}
			body := strings.ToLower(c.Body)
			if strings.Contains(body, "@gopherbot") &&
				strings.Contains(body, "please") &&
				strings.Contains(body, "backport") {
				backportComment = c
			}
			return nil
		}); err != nil && err != errStopIteration {
			return err
		}
		if backportComment == nil {
			return nil
		}

		// Figure out releases to open backport issues for.
		var selectedReleases []string
		majorReleases, _, err := b.fetchReleases(ctx)
		if err != nil {
			return err
		}
		for _, r := range majorReleases {
			if strings.Contains(backportComment.Body, r) {
				selectedReleases = append(selectedReleases, r)
			}
		}
		if len(selectedReleases) == 0 {
			// Only backport to major releases unless explicitly
			// asked to backport to the upcoming release.
			selectedReleases = majorReleases[:len(majorReleases)-1]
		}

		// Figure out extra labels to include from the main issue.
		// Only copy a subset that's relevant to backport issue management.
		var extraLabels []string
		for _, l := range [...]string{
			"Security",
			"GoCommand",
			"Testing",
		} {
			if gi.HasLabel(l) {
				extraLabels = append(extraLabels, l)
			}
		}

		// Open backport issues.
		var openedIssues []string
		for _, rel := range selectedReleases {
			printIssue("open-backport-issue-"+rel, b.gorepo.ID(), gi)
			id, err := b.createGitHubIssue(ctx,
				fmt.Sprintf("%s [%s backport]", gi.Title, rel),
				fmt.Sprintf("@%s requested issue #%d to be considered for backport to the next %s minor release.\n\n%s\n",
					backportComment.User.Login, gi.Number, rel, blockqoute(backportComment.Body)),
				append([]string{"CherryPickCandidate"}, extraLabels...), backportComment.Created)
			if err != nil {
				return err
			}
			openedIssues = append(openedIssues, fmt.Sprintf("#%d (for %s)", id, rel))
		}
		return b.addGitHubComment(ctx, b.gorepo, gi.Number, fmt.Sprintf("Backport issue(s) opened: %s.\n\nRemember to create the cherry-pick CL(s) as soon as the patch is submitted to master, according to https://go.dev/wiki/MinorReleases.", strings.Join(openedIssues, ", ")))
	})
}

// setMinorMilestones applies the next minor release milestone
// to issues with [1.X backport] in the title.
func (b *gopherbot) setMinorMilestones(ctx context.Context) error {
	majorReleases, nextMinor, err := b.fetchReleases(ctx)
	if err != nil {
		return err
	}
	return b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if !gi.Milestone.IsNone() || gi.HasEvent("demilestoned") || gi.HasEvent("milestoned") {
			return nil
		}
		var majorRel string
		for _, r := range majorReleases {
			if strings.Contains(gi.Title, "backport") && strings.HasSuffix(gi.Title, "["+r+" backport]") {
				majorRel = r
			}
		}
		if majorRel == "" {
			return nil
		}
		if _, ok := nextMinor[majorRel]; !ok {
			return fmt.Errorf("internal error: fetchReleases returned majorReleases=%q nextMinor=%q, and nextMinor doesn't have %q", majorReleases, nextMinor, majorRel)
		}
		lowerTitle := "go" + nextMinor[majorRel]
		var nextMinorMilestone milestone
		if b.gorepo.ForeachMilestone(func(m *maintner.GitHubMilestone) error {
			if m.Closed || strings.ToLower(m.Title) != lowerTitle {
				return nil
			}
			nextMinorMilestone = milestone{
				Number: int(m.Number),
				Name:   m.Title,
			}
			return errStopIteration
		}); nextMinorMilestone == (milestone{}) {
			// Fail silently, the milestone might not exist yet.
			log.Printf("Failed to apply minor release milestone to issue %d", gi.Number)
			return nil
		}
		return b.setMilestone(ctx, b.gorepo.ID(), gi, nextMinorMilestone)
	})
}

// closeCherryPickIssues closes cherry-pick issues when CLs are merged to
// release branches, as GitHub only does that on merge to the main branch.
func (b *gopherbot) closeCherryPickIssues(ctx context.Context) error {
	cherryPickIssues := make(map[int32]*maintner.GitHubIssue) // by GitHub Issue Number
	b.foreachIssue(b.gorepo, open, func(gi *maintner.GitHubIssue) error {
		if gi.Milestone.IsNone() || gi.HasEvent("reopened") {
			return nil
		}
		if !strings.HasPrefix(gi.Milestone.Title, "Go") {
			return nil
		}
		cherryPickIssues[gi.Number] = gi
		return nil
	})
	monthAgo := time.Now().Add(-30 * 24 * time.Hour)
	return b.corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
			if cl.Commit.CommitTime.Before(monthAgo) {
				// If the CL was last updated over a month ago, assume (as an
				// optimization) that gopherbot already processed this CL.
				return nil
			}
			if cl.Status != "merged" || cl.Private || !strings.HasPrefix(cl.Branch(), "release-branch.") {
				return nil
			}
			clBranchVersion := cl.Branch()[len("release-branch."):] // "go1.11" or "go1.12".
			for _, ref := range cl.GitHubIssueRefs {
				if ref.Repo != b.gorepo {
					continue
				}
				gi, ok := cherryPickIssues[ref.Number]
				if !ok {
					continue
				}
				if !strutil.HasPrefixFold(gi.Milestone.Title, clBranchVersion) {
					// This issue's milestone (e.g., "Go1.11.6", "Go1.12", "Go1.12.1", etc.)
					// doesn't match the CL branch goX.Y version, so skip it.
					continue
				}
				printIssue("close-cherry-pick", ref.Repo.ID(), gi)
				if err := b.addGitHubComment(ctx, ref.Repo, gi.Number, fmt.Sprintf(
					"Closed by merging [CL %d](https://go.dev/cl/%d) (commit %s) to `%s`.", cl.Number, cl.Number, cl.Commit.Hash, cl.Branch())); err != nil {
					return err
				}
				return b.closeGitHubIssue(ctx, ref.Repo.ID(), gi.Number, completed)
			}
			return nil
		})
	})
}

// closeLUCIConfigIssues closes specified issues when CLs are merged to the
// luci-config branch, as GitHub only does that on merge to the main branch.
func (b *gopherbot) closeLUCIConfigIssues(ctx context.Context) error {
	buildProject := b.corpus.Gerrit().Project("go.googlesource.com", "build")
	if buildProject == nil {
		return fmt.Errorf("no go.googlesource.com/build Gerrit project in corpus")
	}
	monthAgo := time.Now().Add(-30 * 24 * time.Hour)
	return buildProject.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
		if cl.Commit.CommitTime.Before(monthAgo) {
			// If the CL was last updated over a month ago, assume (as an
			// optimization) that gopherbot already processed this CL.
			return nil
		}
		if cl.Status != "merged" || cl.Private || cl.Branch() != "luci-config" {
			return nil
		}
		for _, ref := range cl.GitHubIssueRefs {
			if ref.Repo != b.gorepo {
				continue
			}
			gi := b.gorepo.Issue(ref.Number)
			if gi == nil || gi.NotExist || gi.PullRequest || gi.Locked || b.deletedIssues[githubIssue{ref.Repo.ID(), gi.Number}] ||
				gi.Closed || gi.HasEvent("reopened") || !strings.Contains(cl.Commit.Msg, fmt.Sprintf("\nFixes golang/go#%d", gi.Number)) {
				continue
			}
			printIssue("close luci-config issues", ref.Repo.ID(), gi)
			if err := b.addGitHubComment(ctx, ref.Repo, gi.Number, fmt.Sprintf(
				"Closed by merging [CL %d](https://go.dev/cl/%d) (commit golang/%s@%s) to `%s`.", cl.Number, cl.Number, cl.Project.Project(), cl.Commit.Hash, cl.Branch())); err != nil {
				return err
			}
			return b.closeGitHubIssue(ctx, ref.Repo.ID(), gi.Number, completed)
		}
		return nil
	})
}

type labelCommand struct {
	action  string    // "add" or "remove"
	label   string    // the label name
	created time.Time // creation time of the comment containing the command
	noop    bool      // whether to apply the command or not
}

// applyLabelsFromComments looks within open GitHub issues for commands to add or
// remove labels. Anyone can use the /label <label> or /unlabel <label> commands.
func (b *gopherbot) applyLabelsFromComments(ctx context.Context) error {
	return b.corpus.GitHub().ForeachRepo(func(repo *maintner.GitHubRepo) error {
		if !gardenIssues(repo) {
			return nil
		}

		allLabels := make(map[string]string) // lowercase label name -> proper casing
		repo.ForeachLabel(func(gl *maintner.GitHubLabel) error {
			allLabels[strings.ToLower(gl.Name)] = gl.Name
			return nil
		})

		return b.foreachIssue(repo, open|includePRs, func(gi *maintner.GitHubIssue) error {
			var cmds []labelCommand

			cmds = append(cmds, labelCommandsFromBody(gi.Body, gi.Created)...)
			gi.ForeachComment(func(gc *maintner.GitHubComment) error {
				cmds = append(cmds, labelCommandsFromBody(gc.Body, gc.Created)...)
				return nil
			})

			for i, c := range cmds {
				// Does the label even exist? If so, use the proper capitalization.
				// If it doesn't exist, the command is a no-op.
				if l, ok := allLabels[c.label]; ok {
					cmds[i].label = l
				} else {
					cmds[i].noop = true
					continue
				}

				// If any action has been taken on the label since the comment containing
				// the command to add or remove it, then it should be a no-op.
				gi.ForeachEvent(func(ge *maintner.GitHubIssueEvent) error {
					if (ge.Type == "unlabeled" || ge.Type == "labeled") &&
						strings.ToLower(ge.Label) == c.label &&
						ge.Created.After(c.created) {
						cmds[i].noop = true
						return errStopIteration
					}
					return nil
				})
			}

			toAdd, toRemove := mutationsFromCommands(cmds)
			if err := b.addLabels(ctx, repo.ID(), gi, toAdd); err != nil {
				log.Printf("Unable to add labels (%v) to issue %d: %v", toAdd, gi.Number, err)
			}
			if err := b.removeLabels(ctx, repo.ID(), gi, toRemove); err != nil {
				log.Printf("Unable to remove labels (%v) from issue %d: %v", toRemove, gi.Number, err)
			}

			return nil
		})
	})
}

// labelCommandsFromBody returns a slice of commands inferred by the given body text.
// The format of commands is:
// @gopherbot[,] [please] [add|remove] <label>[{,|;} label... and remove <label>...]
// Omission of add or remove will default to adding a label.
func labelCommandsFromBody(body string, created time.Time) []labelCommand {
	if !strutil.ContainsFold(body, "@gopherbot") {
		return nil
	}
	var cmds []labelCommand
	lines := strings.Split(body, "\n")
	for _, l := range lines {
		if !strutil.ContainsFold(l, "@gopherbot") {
			continue
		}
		l = strings.ToLower(l)
		scanner := bufio.NewScanner(strings.NewReader(l))
		scanner.Split(bufio.ScanWords)
		var (
			add      strings.Builder
			remove   strings.Builder
			inRemove bool
		)
		for scanner.Scan() {
			switch scanner.Text() {
			case "@gopherbot", "@gopherbot,", "@gopherbot:", "please", "and", "label", "labels":
				continue
			case "add":
				inRemove = false
				continue
			case "remove", "unlabel":
				inRemove = true
				continue
			}

			if inRemove {
				remove.WriteString(scanner.Text())
				remove.WriteString(" ") // preserve whitespace within labels
			} else {
				add.WriteString(scanner.Text())
				add.WriteString(" ") // preserve whitespace within labels
			}
		}
		if add.Len() > 0 {
			cmds = append(cmds, labelCommands(add.String(), "add", created)...)
		}
		if remove.Len() > 0 {
			cmds = append(cmds, labelCommands(remove.String(), "remove", created)...)
		}
	}
	return cmds
}

// labelCommands returns a slice of commands for the given action and string of
// text following commands like @gopherbot add/remove.
func labelCommands(s, action string, created time.Time) []labelCommand {
	var cmds []labelCommand
	f := func(c rune) bool {
		return c != '-' && !unicode.IsLetter(c) && !unicode.IsNumber(c) && !unicode.IsSpace(c)
	}
	for _, label := range strings.FieldsFunc(s, f) {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		cmds = append(cmds, labelCommand{action: action, label: label, created: created})
	}
	return cmds
}

// mutationsFromCommands returns two sets of labels to add and remove based on
// the given cmds.
func mutationsFromCommands(cmds []labelCommand) (add, remove []string) {
	// Split the labels into what to add and what to remove.
	// Account for two opposing commands that have yet to be applied canceling
	// each other out.
	var (
		toAdd    map[string]bool
		toRemove map[string]bool
	)
	for _, c := range cmds {
		if c.noop {
			continue
		}
		switch c.action {
		case "add":
			if toRemove[c.label] {
				delete(toRemove, c.label)
				continue
			}
			if toAdd == nil {
				toAdd = make(map[string]bool)
			}
			toAdd[c.label] = true
		case "remove":
			if toAdd[c.label] {
				delete(toAdd, c.label)
				continue
			}
			if toRemove == nil {
				toRemove = make(map[string]bool)
			}
			toRemove[c.label] = true
		default:
			log.Printf("Invalid label action type: %q", c.action)
		}
	}

	for l := range toAdd {
		if toAdd[l] && !labelChangeDisallowed(l, "add") {
			add = append(add, l)
		}
	}

	for l := range toRemove {
		if toRemove[l] && !labelChangeDisallowed(l, "remove") {
			remove = append(remove, l)
		}
	}
	return add, remove
}

// labelChangeDisallowed reports whether an action on the given label is
// forbidden via gopherbot.
func labelChangeDisallowed(label, action string) bool {
	if action == "remove" && label == "Security" {
		return true
	}
	for _, prefix := range []string{
		"CherryPick",
		"cla:",
		"Proposal-",
	} {
		if strings.HasPrefix(label, prefix) {
			return true
		}
	}
	return false
}

// assignReviewersOptOut lists contributors who have opted out from
// having reviewers automatically added to their CLs.
var assignReviewersOptOut = map[string]bool{
	"matthew@go.dev": true,
}

// assignReviewersToCLs looks for CLs with no humans in the reviewer or CC fields
// that have been open for a short amount of time (enough of a signal that the
// author does not intend to add anyone to the review), then assigns reviewers/CCs
// using the go.dev/s/owners API.
func (b *gopherbot) assignReviewersToCLs(ctx context.Context) error {
	const tagNoOwners = "no-owners"
	return b.corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Project() == "scratch" || gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			if cl.Private || cl.WorkInProgress() || time.Since(cl.Created) < 10*time.Minute {
				return nil
			}
			if assignReviewersOptOut[cl.Owner().Email()] {
				return nil
			}

			// Don't auto-assign reviewers to CLs on shared branches;
			// the presumption is that developers there will know which
			// reviewers to assign.
			if strings.HasPrefix(cl.Branch(), "dev.") {
				return nil
			}

			tags := cl.Meta.Hashtags()
			if tags.Contains(tagNoOwners) {
				return nil
			}

			gc := gerritChange{gp.Project(), cl.Number}
			if b.deletedChanges[gc] {
				return nil
			}
			if strutil.ContainsFold(cl.Commit.Msg, "do not submit") || strutil.ContainsFold(cl.Commit.Msg, "do not review") {
				return nil
			}

			currentReviewers, ok := b.humanReviewersOnChange(ctx, gc, cl)
			if ok {
				return nil
			}
			log.Printf("humanReviewersOnChange reported insufficient reviewers or CC on CL %d, attempting to add some", cl.Number)

			changeURL := fmt.Sprintf("https://go-review.googlesource.com/c/%s/+/%d", gp.Project(), cl.Number)
			files, err := b.gerrit.ListFiles(ctx, gc.ID(), cl.Commit.Hash.String())
			if err != nil {
				log.Printf("Could not get change %+v: %v", gc, err)
				if httpErr, ok := err.(*gerrit.HTTPError); ok && httpErr.Res.StatusCode == http.StatusNotFound {
					b.deletedChanges[gc] = true
				}
				return nil
			}

			var paths []string
			for f := range files {
				if f == "/COMMIT_MSG" {
					continue
				}
				paths = append(paths, gp.Project()+"/"+f)
			}

			entries, err := getCodeOwners(ctx, paths)
			if err != nil {
				log.Printf("Could not get owners for change %s: %v", changeURL, err)
				return nil
			}

			// Remove owners that can't be reviewers.
			entries = filterGerritOwners(entries)

			authorEmail := cl.Commit.Author.Email()
			merged := mergeOwnersEntries(entries, authorEmail)
			if len(merged.Primary) == 0 && len(merged.Secondary) == 0 {
				// No owners found for the change. Add the #no-owners tag.
				log.Printf("Adding no-owners tag to change %s...", changeURL)
				if *dryRun {
					return nil
				}
				if _, err := b.gerrit.AddHashtags(ctx, gc.ID(), tagNoOwners); err != nil {
					log.Printf("Could not add hashtag to change %q: %v", gc.ID(), err)
					return nil
				}
				return nil
			}

			// Assign reviewers.
			var review gerrit.ReviewInput
			for _, owner := range merged.Primary {
				review.Reviewers = append(review.Reviewers, gerrit.ReviewerInput{Reviewer: owner.GerritEmail})
			}
			for _, owner := range merged.Secondary {
				review.Reviewers = append(review.Reviewers, gerrit.ReviewerInput{Reviewer: owner.GerritEmail, State: "CC"})
			}

			// If the reviewers that would be set are the same as the existing
			// reviewers (minus the bots), there is no work to be done.
			if sameReviewers(currentReviewers, review) {
				log.Printf("Setting review %+v on %s would have no effect, continuing", review, changeURL)
				return nil
			}
			if *dryRun {
				log.Printf("[dry run] Would set review on %s: %+v", changeURL, review)
				return nil
			}
			log.Printf("Setting review on %s: %+v", changeURL, review)
			if err := b.gerrit.SetReview(ctx, gc.ID(), "current", review); err != nil {
				log.Printf("Could not set review for change %q: %v", gc.ID(), err)
				return nil
			}
			return nil
		})
	})
}

func sameReviewers(reviewers []string, review gerrit.ReviewInput) bool {
	if len(reviewers) != len(review.Reviewers) {
		return false
	}
	sort.Strings(reviewers)
	var people []*gophers.Person
	for _, id := range reviewers {
		p := gophers.GetPerson(fmt.Sprintf("%s%s", id, gerritInstanceID))
		// If an existing reviewer is not known to us, we have no way of
		// checking if these reviewer lists are identical.
		if p == nil {
			return false
		}
		people = append(people, p)
	}
	sort.Slice(review.Reviewers, func(i, j int) bool {
		return review.Reviewers[i].Reviewer < review.Reviewers[j].Reviewer
	})
	// Check if any of the person's emails match the expected reviewer email.
outer:
	for i, p := range people {
		reviewerEmail := review.Reviewers[i].Reviewer
		for _, email := range p.Emails {
			if email == reviewerEmail {
				continue outer
			}
		}
		return false
	}
	return true
}

// abandonScratchReviews abandons Gerrit CLs in the "scratch" project if they've been open for over a week.
func (b *gopherbot) abandonScratchReviews(ctx context.Context) error {
	scratchProject := b.corpus.Gerrit().Project("go.googlesource.com", "scratch")
	if scratchProject == nil {
		return fmt.Errorf("no go.googlesource.com/scratch Gerrit project in corpus")
	}
	tooOld := time.Now().Add(-24 * time.Hour * 7)
	return scratchProject.ForeachOpenCL(func(cl *maintner.GerritCL) error {
		if b.deletedChanges[gerritChange{scratchProject.Project(), cl.Number}] || !cl.Meta.Commit.CommitTime.Before(tooOld) {
			return nil
		}
		if *dryRun {
			log.Printf("[dry-run] would've closed scratch CL https://go.dev/cl/%d ...", cl.Number)
			return nil
		}
		log.Printf("closing scratch CL https://go.dev/cl/%d ...", cl.Number)
		err := b.gerrit.AbandonChange(ctx, fmt.Sprint(cl.Number), "Auto-abandoning old scratch review.")
		if err != nil && strings.Contains(err.Error(), "404 Not Found") {
			return nil
		}
		return err
	})
}

func (b *gopherbot) whoNeedsAccess(ctx context.Context) error {
	// We only run this task if it was explicitly requested via
	// the --only-run flag.
	if *onlyRun == "" {
		return nil
	}
	level := map[int64]int{} // gerrit id -> 1 for try, 2 for submit
	ais, err := b.gerrit.GetGroupMembers(ctx, "may-start-trybots")
	if err != nil {
		return err
	}
	for _, ai := range ais {
		level[ai.NumericID] = 1
	}
	ais, err = b.gerrit.GetGroupMembers(ctx, "approvers")
	if err != nil {
		return err
	}
	for _, ai := range ais {
		level[ai.NumericID] = 2
	}

	quarterAgo := time.Now().Add(-90 * 24 * time.Hour)
	missing := map[string]int{} // "only level N: $WHO" -> number of CLs for that user
	err = b.corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
			if cl.Meta.Commit.AuthorTime.Before(quarterAgo) {
				return nil
			}
			authorID := int64(cl.OwnerID())
			if authorID == -1 {
				return nil
			}
			if level[authorID] == 2 {
				return nil
			}
			missing[fmt.Sprintf("only level %d: %v", level[authorID], cl.Commit.Author)]++
			return nil
		})
	})
	if err != nil {
		return err
	}
	var people []string
	for who := range missing {
		people = append(people, who)
	}
	sort.Slice(people, func(i, j int) bool { return missing[people[j]] < missing[people[i]] })
	fmt.Println("Number of CLs created in last 90 days | Access (0=none, 1=trybots) | Author")
	for i, who := range people {
		num := missing[who]
		if num < 3 {
			break
		}
		fmt.Printf("%3d: %s\n", num, who)
		if i == 20 {
			break
		}
	}
	return nil
}

// humanReviewersOnChange reports whether there is (or was) a sufficient
// number of human reviewers in the given change, and returns the IDs of
// the current human reviewers. It includes reviewers in REVIEWER and CC
// states.
//
// The given gerritChange works as a key for deletedChanges.
func (b *gopherbot) humanReviewersOnChange(ctx context.Context, change gerritChange, cl *maintner.GerritCL) ([]string, bool) {
	// The CL's owner will be GerritBot if it is imported from a PR.
	// In that case, if the CL's author has a Gerrit account, they will be
	// added as a reviewer (go.dev/issue/30265). Otherwise, no reviewers
	// will be added. Work around this by requiring 2 human reviewers on PRs.
	ownerID := strconv.Itoa(cl.OwnerID())
	isPR := ownerID == gerritbotGerritID
	minHumans := 1
	if isPR {
		minHumans = 2
	}
	reject := []string{gobotGerritID, gerritbotGerritID, kokoroGerritID, goLUCIGerritID, triciumGerritID, ownerID}
	ownerOrRobot := func(gerritID string) bool {
		for _, r := range reject {
			if gerritID == r {
				return true
			}
		}
		return false
	}

	ids := slices.DeleteFunc(reviewersInMetas(cl.Metas), ownerOrRobot)
	if len(ids) >= minHumans {
		return ids, true
	}

	reviewers, err := b.gerrit.ListReviewers(ctx, change.ID())
	if err != nil {
		if httpErr, ok := err.(*gerrit.HTTPError); ok && httpErr.Res.StatusCode == http.StatusNotFound {
			b.deletedChanges[change] = true
		}
		log.Printf("Could not list reviewers on change %q: %v", change.ID(), err)
		return nil, true
	}
	ids = []string{}
	for _, r := range reviewers {
		id := strconv.FormatInt(r.NumericID, 10)
		if hasServiceUserTag(r.AccountInfo) || ownerOrRobot(id) {
			// Skip bots and owner.
			continue
		}
		ids = append(ids, id)
	}
	return ids, len(ids) >= minHumans
}

// hasServiceUserTag reports whether the account has a SERVICE_USER tag.
func hasServiceUserTag(a gerrit.AccountInfo) bool {
	for _, t := range a.Tags {
		if t == "SERVICE_USER" {
			return true
		}
	}
	return false
}

// autoSubmitCLs submits CLs which are labelled "Auto-Submit",
// have all submit requirements satisfied according to Gerrit, and
// aren't waiting for a parent CL in the stack to be handled.
//
// See go.dev/issue/48021.
func (b *gopherbot) autoSubmitCLs(ctx context.Context) error {
	return b.corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			gc := gerritChange{gp.Project(), cl.Number}
			if b.deletedChanges[gc] {
				return nil
			}

			// Break out early (before making Gerrit API calls) if the Auto-Submit label
			// hasn't been used at all in this CL.
			var autosubmitPresent bool
			for _, meta := range cl.Metas {
				if strings.Contains(meta.Commit.Msg, "\nLabel: Auto-Submit") {
					autosubmitPresent = true
					break
				}
			}
			if !autosubmitPresent {
				return nil
			}

			// Skip this CL if Auto-Submit+1 isn't actively set on it.
			changeInfo, err := b.gerrit.GetChange(ctx, fmt.Sprint(cl.Number), gerrit.QueryChangesOpt{Fields: []string{"LABELS", "SUBMITTABLE"}})
			if err != nil {
				if httpErr, ok := err.(*gerrit.HTTPError); ok && httpErr.Res.StatusCode == http.StatusNotFound {
					b.deletedChanges[gc] = true
				}
				log.Printf("Could not retrieve change %q: %v", gc.ID(), err)
				return nil
			}
			if autosubmitActive := changeInfo.Labels["Auto-Submit"].Approved != nil; !autosubmitActive {
				return nil
			}
			// NOTE: we might be able to skip this as well, since the revision action
			// check will also cover this...
			if !changeInfo.Submittable {
				return nil
			}

			// We need to check the mergeability, as well as the submitability,
			// as the latter doesn't take into account merge conflicts, just
			// if the change satisfies the project submit rules.
			//
			// NOTE: this may now be redundant, since the revision action check
			// below will also inherently checks mergeability, since the change
			// cannot actually be submitted if there is a merge conflict. We
			// may be able to just skip this entirely.
			mi, err := b.gerrit.GetMergeable(ctx, fmt.Sprint(cl.Number), "current")
			if err != nil {
				return err
			}
			if !mi.Mergeable || mi.CommitMerged {
				return nil
			}

			ra, err := b.gerrit.GetRevisionActions(ctx, fmt.Sprint(cl.Number), "current")
			if err != nil {
				return err
			}
			if ra["submit"] == nil || !ra["submit"].Enabled {
				return nil
			}

			// If this change is part of a stack, we'd like to merge the stack
			// in the correct order (i.e. from the bottom of the stack to the
			// top), so we'll only merge the current change if every change
			// below it in the stack is either merged, or abandoned.
			// GetRelatedChanges gives us the stack from top to bottom (the
			// order of the git commits, from newest to oldest, see Gerrit
			// documentation for RelatedChangesInfo), so first we find our
			// change in the stack, then  check everything below it.
			relatedChanges, err := b.gerrit.GetRelatedChanges(ctx, fmt.Sprint(cl.Number), "current")
			if err != nil {
				return err
			}
			if len(relatedChanges.Changes) > 0 {
				var parentChanges bool
				for _, ci := range relatedChanges.Changes {
					if !parentChanges {
						// Skip everything before the change we are checking, as
						// they are the children of this change, and we only care
						// about the parents.
						parentChanges = ci.ChangeNumber == cl.Number
						continue
					}
					if ci.Status != gerrit.ChangeStatusAbandoned &&
						ci.Status != gerrit.ChangeStatusMerged {
						return nil
					}
					// We do not check the revision number of merged/abandoned
					// parents since, even if they are not current according to
					// gerrit, if there were any merge conflicts, caused by the
					// diffs between the revision this change was based on and
					// the current revision, the change would not be considered
					// submittable anyway.
				}
			}

			if *dryRun {
				log.Printf("[dry-run] would've submitted CL https://golang.org/cl/%d ...", cl.Number)
				return nil
			}
			log.Printf("submitting CL https://golang.org/cl/%d ...", cl.Number)

			// TODO: if maintner isn't fast enough (or is too fast) and it re-runs this
			// before the submission is noticed, we may run this more than once. This
			// could be handled with a local cache of "recently submitted" changes to
			// be ignored.
			_, err = b.gerrit.SubmitChange(ctx, fmt.Sprint(cl.Number))
			return err
		})
	})
}

type issueFlags uint8

const (
	open        issueFlags = 1 << iota // Include open issues.
	closed                             // Include closed issues.
	includePRs                         // Include issues that are Pull Requests.
	includeGone                        // Include issues that are gone (e.g., deleted or transferred).
)

// foreachIssue calls fn for each issue in repo gr as controlled by flags.
//
// If fn returns an error, iteration ends and foreachIssue returns
// with that error.
//
// The fn function is called serially, with increasingly numbered
// issues.
func (b *gopherbot) foreachIssue(gr *maintner.GitHubRepo, flags issueFlags, fn func(*maintner.GitHubIssue) error) error {
	return gr.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		switch {
		case (flags&open == 0) && !gi.Closed,
			(flags&closed == 0) && gi.Closed,
			(flags&includePRs == 0) && gi.PullRequest,
			(flags&includeGone == 0) && (gi.NotExist || b.deletedIssues[githubIssue{gr.ID(), gi.Number}]):
			// Skip issue.
			return nil
		default:
			return fn(gi)
		}
	})
}

// reviewerRe extracts the reviewer's Gerrit ID from a line that looks like:
//
//	Reviewer: Rebecca Stambler <16140@62eb7196-b449-3ce5-99f1-c037f21e1705>
var reviewerRe = regexp.MustCompile(`.* <(?P<id>\d+)@.*>`)

const gerritInstanceID = "@62eb7196-b449-3ce5-99f1-c037f21e1705"

// reviewersInMetas returns the unique Gerrit IDs of reviewers
// (in REVIEWER and CC states) that were at some point added
// to the given Gerrit CL, even if they've been since removed.
func reviewersInMetas(metas []*maintner.GerritMeta) []string {
	var ids []string
	for _, m := range metas {
		if !strings.Contains(m.Commit.Msg, "Reviewer:") && !strings.Contains(m.Commit.Msg, "CC:") {
			continue
		}

		err := foreach.LineStr(m.Commit.Msg, func(ln string) error {
			if !strings.HasPrefix(ln, "Reviewer:") && !strings.HasPrefix(ln, "CC:") {
				return nil
			}
			match := reviewerRe.FindStringSubmatch(ln)
			if match == nil {
				return nil
			}
			// Extract the reviewer's Gerrit ID.
			for i, name := range reviewerRe.SubexpNames() {
				if name != "id" {
					continue
				}
				if i < 0 || i > len(match) {
					continue
				}
				ids = append(ids, match[i])
			}
			return nil
		})
		if err != nil {
			log.Printf("reviewersInMetas: got unexpected error from foreach.LineStr: %v", err)
		}
	}
	// Remove duplicates.
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return ids
}

func getCodeOwners(ctx context.Context, paths []string) ([]*owners.Entry, error) {
	oReq := owners.Request{Version: 1}
	oReq.Payload.Paths = paths

	oResp, err := fetchCodeOwners(ctx, &oReq)
	if err != nil {
		return nil, err
	}

	var entries []*owners.Entry
	for _, entry := range oResp.Payload.Entries {
		if entry == nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func getAllCodeOwners(ctx context.Context) (map[string]*owners.Entry, error) {
	oReq := owners.Request{Version: 1}
	oReq.Payload.All = true
	oResp, err := fetchCodeOwners(ctx, &oReq)
	if err != nil {
		return nil, err
	}
	return oResp.Payload.Entries, nil
}

func fetchCodeOwners(ctx context.Context, oReq *owners.Request) (*owners.Response, error) {
	b, err := json.Marshal(oReq)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", "https://dev.golang.org/owners/", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var oResp owners.Response
	if err := json.NewDecoder(resp.Body).Decode(&oResp); err != nil {
		return nil, fmt.Errorf("could not decode owners response: %v", err)
	}
	if oResp.Error != "" {
		return nil, fmt.Errorf("error from dev.golang.org/owners endpoint: %v", oResp.Error)
	}
	return &oResp, nil
}

// mergeOwnersEntries takes multiple owners.Entry structs and aggregates all
// primary and secondary users into a single entry.
// If a user is a primary in one entry but secondary on another, they are
// primary in the returned entry.
// If a users email matches the authorEmail, the user is omitted from the
// result.
// The resulting order of the entries is non-deterministic.
func mergeOwnersEntries(entries []*owners.Entry, authorEmail string) *owners.Entry {
	var result owners.Entry
	pm := make(map[owners.Owner]int)
	for _, e := range entries {
		for _, o := range e.Primary {
			pm[o]++
		}
	}
	sm := make(map[owners.Owner]int)
	for _, e := range entries {
		for _, o := range e.Secondary {
			if pm[o] > 0 {
				pm[o]++
			} else {
				sm[o]++
			}
		}
	}

	const maxReviewers = 3
	if len(pm) > maxReviewers {
		// Spamming many reviewers.
		// Cut to three most common reviewers
		// and drop all the secondaries.
		var keep []owners.Owner
		for o := range pm {
			keep = append(keep, o)
		}
		sort.Slice(keep, func(i, j int) bool {
			return pm[keep[i]] > pm[keep[j]]
		})
		keep = keep[:maxReviewers]
		sort.Slice(keep, func(i, j int) bool {
			return keep[i].GerritEmail < keep[j].GerritEmail
		})
		return &owners.Entry{Primary: keep}
	}

	for o := range pm {
		if o.GerritEmail != authorEmail {
			result.Primary = append(result.Primary, o)
		}
	}
	for o := range sm {
		if o.GerritEmail != authorEmail {
			result.Secondary = append(result.Secondary, o)
		}
	}
	return &result
}

// filterGerritOwners removes all primary and secondary owners from entries
// that are missing GerritEmail, and thus cannot be Gerrit reviewers (e.g.,
// GitHub Teams).
//
// If an Entry's primary reviewers is empty after this process, the secondary
// owners are upgraded to primary.
func filterGerritOwners(entries []*owners.Entry) []*owners.Entry {
	result := make([]*owners.Entry, 0, len(entries))
	for _, e := range entries {
		var clean owners.Entry
		for _, owner := range e.Primary {
			if owner.GerritEmail != "" {
				clean.Primary = append(clean.Primary, owner)
			}
		}
		for _, owner := range e.Secondary {
			if owner.GerritEmail != "" {
				clean.Secondary = append(clean.Secondary, owner)
			}
		}
		if len(clean.Primary) == 0 {
			clean.Primary = clean.Secondary
			clean.Secondary = nil
		}
		result = append(result, &clean)
	}
	return result
}

func blockqoute(s string) string {
	s = strings.TrimSpace(s)
	s = "> " + s
	s = strings.Replace(s, "\n", "\n> ", -1)
	return s
}

// errStopIteration is used to stop iteration over issues or comments.
// It has no special meaning.
var errStopIteration = errors.New("stop iteration")

func isDocumentationTitle(t string) bool {
	if !strings.Contains(t, "doc") && !strings.Contains(t, "Doc") {
		return false
	}
	t = strings.ToLower(t)
	if strings.HasPrefix(t, "x/pkgsite:") {
		// Don't label pkgsite issues with the Documentation label.
		return false
	}
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

func isGoplsTitle(t string) bool {
	// If the prefix doesn't contain "gopls" or "lsp",
	// then it may not be a gopls issue.
	i := strings.Index(t, ":")
	if i > -1 {
		t = t[:i]
	}
	return strings.Contains(t, "gopls") || strings.Contains(t, "lsp")
}

var lastTask string

func printIssue(task string, repoID maintner.GitHubRepoID, gi *maintner.GitHubIssue) {
	if *dryRun {
		task = task + " [dry-run]"
	}
	if task != lastTask {
		fmt.Println(task)
		lastTask = task
	}
	if repoID.Owner != "golang" || repoID.Repo != "go" {
		fmt.Printf("\thttps://github.com/%s/issues/%v  %s\n", repoID, gi.Number, gi.Title)
	} else {
		fmt.Printf("\thttps://go.dev/issue/%v  %s\n", gi.Number, gi.Title)
	}
}

func repoHasLabel(repo *maintner.GitHubRepo, name string) bool {
	has := false
	repo.ForeachLabel(func(label *maintner.GitHubLabel) error {
		if label.Name == name {
			has = true
		}
		return nil
	})
	return has
}
