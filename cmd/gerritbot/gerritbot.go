// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gerritbot binary converts GitHub Pull Requests to Gerrit Changes,
// updating the PR and Gerrit Change as appropriate.
package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"github.com/google/go-github/v48/github"
	"github.com/gregjones/httpcache"
	"golang.org/x/build/cmd/gerritbot/internal/rules"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/build/repos"
	"golang.org/x/oauth2"
)

var (
	workdir         = flag.String("workdir", cacheDir(), "where git repos and temporary worktrees are created")
	githubTokenFile = flag.String("github-token-file", filepath.Join(configDir(), "github-token"), "file to load GitHub token from; should only contain the token text")
	gerritTokenFile = flag.String("gerrit-token-file", filepath.Join(configDir(), "gerrit-token"), "file to load Gerrit token from; should be of form <git-email>:<token>")
	gitcookiesFile  = flag.String("gitcookies-file", "", "if non-empty, write a git http cookiefile to this location using secret manager")
	dryRun          = flag.Bool("dry-run", false, "print out mutating actions but don’t perform any")
	singlePR        = flag.String("single-pr", "", "process only this PR, specified in GitHub shortlink format, e.g. golang/go#1")
)

// TODO(amedee): set to this value until the SLO numbers are published
const secretClientTimeout = 10 * time.Second

func main() {
	https.RegisterFlags(flag.CommandLine)
	flag.Parse()

	var secretClient *secret.Client
	if metadata.OnGCE() {
		secretClient = secret.MustNewClient()
	}
	if err := writeCookiesFile(secretClient); err != nil {
		log.Fatalf("writeCookiesFile(): %v", err)
	}
	ghc, err := githubClient(secretClient)
	if err != nil {
		log.Fatalf("githubClient(): %v", err)
	}
	gc, err := gerritClient(secretClient)
	if err != nil {
		log.Fatalf("gerritClient(): %v", err)
	}
	b := newBot(ghc, gc)

	ctx := context.Background()
	b.initCorpus(ctx)
	go b.corpusUpdateLoop(ctx)

	log.Fatalln(https.ListenAndServe(ctx, http.HandlerFunc(handleIndex)))
}

func configDir() string {
	cd, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf("UserConfigDir: %v", err)
	}
	return filepath.Join(cd, "gerritbot")
}

func cacheDir() string {
	cd, err := os.UserCacheDir()
	if err != nil {
		log.Fatalf("UserCacheDir: %v", err)
	}
	return filepath.Join(cd, "gerritbot")
}

func writeCookiesFile(sc *secret.Client) error {
	if *gitcookiesFile == "" {
		return nil
	}
	log.Printf("Writing git http cookies file %q ...", *gitcookiesFile)
	if !metadata.OnGCE() {
		return fmt.Errorf("cannot write git http cookies file %q from secret manager: not on GCE", *gitcookiesFile)
	}

	ctx, cancel := context.WithTimeout(context.Background(), secretClientTimeout)
	defer cancel()

	cookies, err := sc.Retrieve(ctx, secret.NameGerritbotGitCookies)
	if err != nil {
		return fmt.Errorf("secret.Retrieve(ctx, %q): %q, %w", secret.NameGerritbotGitCookies, cookies, err)
	}
	return os.WriteFile(*gitcookiesFile, []byte(cookies), 0600)
}

func githubClient(sc *secret.Client) (*github.Client, error) {
	token, err := githubToken(sc)
	if err != nil {
		return nil, err
	}
	oauthTransport := &oauth2.Transport{
		Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
	}
	cachingTransport := &httpcache.Transport{
		Transport:           oauthTransport,
		Cache:               httpcache.NewMemoryCache(),
		MarkCachedResponses: true,
	}
	httpClient := &http.Client{
		Transport: cachingTransport,
	}
	return github.NewClient(httpClient), nil
}

func githubToken(sc *secret.Client) (string, error) {
	if metadata.OnGCE() {
		ctx, cancel := context.WithTimeout(context.Background(), secretClientTimeout)
		defer cancel()

		token, err := sc.Retrieve(ctx, secret.NameMaintnerGitHubToken)
		if err != nil {
			log.Printf("secret.Retrieve(ctx, %q): %q, %v", secret.NameMaintnerGitHubToken, token, err)
		} else {
			return token, nil
		}
	}
	slurp, err := os.ReadFile(*githubTokenFile)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(slurp))
	if len(tok) == 0 {
		return "", fmt.Errorf("token from file %q cannot be empty", *githubTokenFile)
	}
	return tok, nil
}

func gerritClient(sc *secret.Client) (*gerrit.Client, error) {
	username, token, err := gerritAuth(sc)
	if err != nil {
		return nil, err
	}
	c := gerrit.NewClient("https://go-review.googlesource.com", gerrit.BasicAuth(username, token))
	return c, nil
}

func gerritAuth(sc *secret.Client) (string, string, error) {
	var slurp string
	if metadata.OnGCE() {
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), secretClientTimeout)
		defer cancel()
		slurp, err = sc.Retrieve(ctx, secret.NameGobotPassword)
		if err != nil {
			log.Printf("secret.Retrieve(ctx, %q): %q, %v", secret.NameGobotPassword, slurp, err)
		}
	}
	if len(slurp) == 0 {
		slurpBytes, err := os.ReadFile(*gerritTokenFile)
		if err != nil {
			return "", "", err
		}
		slurp = string(slurpBytes)
	}
	f := strings.SplitN(strings.TrimSpace(slurp), ":", 2)
	if len(f) == 1 {
		// Assume the whole thing is the token.
		return "git-gobot.golang.org", f[0], nil
	}
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", "", fmt.Errorf("expected Gerrit token to be of form <git-email>:<token>")
	}
	return f[0], f[1], nil
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintln(w, "Hello, GerritBot! 🤖")
}

const (
	// Footer that contains the last revision from GitHub that was successfully
	// imported to Gerrit.
	prefixGitFooterLastRev = "GitHub-Last-Rev:"

	// Footer containing the GitHub PR associated with the Gerrit Change.
	prefixGitFooterPR = "GitHub-Pull-Request:"

	// Footer containing the Gerrit Change ID.
	prefixGitFooterChangeID = "Change-Id:"

	// Footer containing the LUCI SlowBots to run.
	prefixGitFooterCQIncludeTrybots = "Cq-Include-Trybots:"
)

// Gerrit projects we accept PRs for.
var gerritProjectAllowlist = genProjectAllowlist()

func genProjectAllowlist() map[string]bool {
	m := make(map[string]bool)
	for p, r := range repos.ByGerritProject {
		if r.MirrorToGitHub {
			m[p] = true
		}
	}
	return m
}

type bot struct {
	githubClient *github.Client
	gerritClient *gerrit.Client

	sync.RWMutex // Protects all fields below
	corpus       *maintner.Corpus

	// PRs and their corresponding Gerrit CLs.
	importedPRs map[string]*maintner.GerritCL // GitHub owner/repo#n -> Gerrit CL

	// CLs that have been created/updated on Gerrit for GitHub PRs but are not yet
	// reflected in the maintner corpus yet.
	pendingCLs map[string]string // GitHub owner/repo#n -> Commit message from PR

	// Cache of Gerrit Account IDs to AccountInfo structs.
	cachedGerritAccounts map[int]*gerrit.AccountInfo // 1234 -> Detailed Account Info
}

func newBot(githubClient *github.Client, gerritClient *gerrit.Client) *bot {
	return &bot{
		githubClient:         githubClient,
		gerritClient:         gerritClient,
		importedPRs:          map[string]*maintner.GerritCL{},
		pendingCLs:           map[string]string{},
		cachedGerritAccounts: map[int]*gerrit.AccountInfo{},
	}
}

// initCorpus fetches a full maintner corpus, overwriting any existing data.
func (b *bot) initCorpus(ctx context.Context) {
	b.Lock()
	defer b.Unlock()
	var err error
	b.corpus, err = godata.Get(ctx)
	if err != nil {
		log.Fatalf("godata.Get: %v", err)
	}
}

// corpusUpdateLoop continuously updates the server’s corpus until ctx’s Done
// channel is closed.
func (b *bot) corpusUpdateLoop(ctx context.Context) {
	log.Println("Starting corpus update loop ...")
	for {
		b.checkPullRequests()
		err := b.corpus.UpdateWithLocker(ctx, &b.RWMutex)
		if err != nil {
			if err == maintner.ErrSplit {
				log.Println("Corpus out of sync. Re-fetching corpus.")
				b.initCorpus(ctx)
			} else {
				log.Printf("corpus.Update: %v; sleeping 15s", err)
				time.Sleep(15 * time.Second)
				continue
			}
		}

		select {
		case <-ctx.Done():
			return
		default:
			continue
		}
	}
}

func (b *bot) checkPullRequests() {
	b.Lock()
	defer b.Unlock()
	b.importedPRs = map[string]*maintner.GerritCL{}
	b.corpus.Gerrit().ForeachProjectUnsorted(func(p *maintner.GerritProject) error {
		pname := p.Project()
		if !gerritProjectAllowlist[pname] {
			return nil
		}
		return p.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			prv := cl.Footer(prefixGitFooterPR)
			if prv == "" {
				return nil
			}
			b.importedPRs[prv] = cl
			return nil
		})
	})

	b.corpus.GitHub().ForeachRepo(func(ghr *maintner.GitHubRepo) error {
		id := ghr.ID()
		if id.Owner != "golang" || !gerritProjectAllowlist[id.Repo] {
			return nil
		}
		return ghr.ForeachIssue(func(issue *maintner.GitHubIssue) error {
			ctx := context.Background()
			shortLink := githubShortLink(id.Owner, id.Repo, int(issue.Number))
			if *singlePR != "" && shortLink != *singlePR {
				return nil
			}
			if issue.PullRequest && issue.Closed {
				// Clean up any reference of closed CLs within pendingCLs.
				delete(b.pendingCLs, shortLink)
				if cl, ok := b.importedPRs[shortLink]; ok {
					// The CL associated with the PR is still open since it's
					// present in importedPRs, so abandon it.
					if err := b.abandonCL(ctx, cl, shortLink); err != nil {
						log.Printf("abandonCL(ctx, https://golang.org/cl/%v, %q): %v", cl.Number, shortLink, err)
					}
				}
				return nil
			}
			if issue.Closed || !issue.PullRequest {
				return nil
			}
			pr, err := b.getFullPR(ctx, id.Owner, id.Repo, int(issue.Number))
			if err != nil {
				log.Printf("getFullPR(ctx, %q, %q, %d): %v", id.Owner, id.Repo, issue.Number, err)
				return nil
			}
			approved, err := b.claApproved(ctx, id, pr)
			if err != nil {
				log.Printf("checking CLA approval: %v", err)
				return nil
			}
			if !approved {
				return nil
			}
			if err := b.processPullRequest(ctx, pr); err != nil {
				log.Printf("processPullRequest: %v", err)
				return nil
			}
			return nil
		})
	})
}

// claApproved reports whether the latest head commit of the given PR in repo
// has been approved by the Google CLA checker.
func (b *bot) claApproved(ctx context.Context, repo maintner.GitHubRepoID, pr *github.PullRequest) (bool, error) {
	if pr.GetHead().GetSHA() == "" {
		// Paranoia check. This should never happen.
		return false, fmt.Errorf("no head SHA for PR %v %v", repo, pr.GetNumber())
	}
	runs, _, err := b.githubClient.Checks.ListCheckRunsForRef(ctx, repo.Owner, repo.Repo, pr.GetHead().GetSHA(), &github.ListCheckRunsOptions{
		CheckName: github.String("cla/google"),
		Status:    github.String("completed"),
		Filter:    github.String("latest"),
		// TODO(heschi): filter for App ID once supported by go-github
	})
	if err != nil {
		return false, err
	}
	for _, run := range runs.CheckRuns {
		if run.GetApp().GetID() != 42202 {
			continue
		}
		return run.GetConclusion() == "success", nil
	}
	return false, nil
}

// githubShortLink returns text referencing an Issue or Pull Request that will be
// automatically converted into a link by GitHub.
func githubShortLink(owner, repo string, number int) string {
	return fmt.Sprintf("%s#%d", owner+"/"+repo, number)
}

// prShortLink returns text referencing the given Pull Request that will be
// automatically converted into a link by GitHub.
func prShortLink(pr *github.PullRequest) string {
	repo := pr.GetBase().GetRepo()
	return githubShortLink(repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber())
}

// processPullRequest is the entry point to the state machine of mirroring a PR
// with Gerrit. PRs that are up to date with their respective Gerrit changes are
// skipped, and any with a HEAD commit SHA unequal to its Gerrit equivalent are
// imported. If the Gerrit change associated with a PR has been merged, the PR
// is closed. Those that have no associated open or merged Gerrit changes will
// result in one being created.
// b.RWMutex must be Lock'ed.
func (b *bot) processPullRequest(ctx context.Context, pr *github.PullRequest) error {
	log.Printf("Processing PR %s ...", pr.GetHTMLURL())
	shortLink := prShortLink(pr)
	cl := b.importedPRs[shortLink]

	if cl != nil && b.pendingCLs[shortLink] == cl.Commit.Msg {
		delete(b.pendingCLs, shortLink)
	}
	if b.pendingCLs[shortLink] != "" {
		log.Printf("Changes for PR %s have yet to be mirrored in the maintner corpus. Skipping for now.", shortLink)
		return nil
	}

	cmsg, err := commitMessage(pr, cl)
	if err != nil {
		return fmt.Errorf("commitMessage: %v", err)
	}

	if cl == nil {
		gcl, err := b.gerritChangeForPR(pr)
		if err != nil {
			return fmt.Errorf("gerritChangeForPR(%+v): %v", pr, err)
		}
		if gcl != nil && gcl.Status != "NEW" {
			if err := b.closePR(ctx, pr, gcl); err != nil {
				return fmt.Errorf("b.closePR(ctx, %+v, %+v): %v", pr, gcl, err)
			}
		}
		if gcl != nil {
			b.pendingCLs[shortLink] = cmsg
			return nil
		}
		if err := b.importGerritChangeFromPR(ctx, pr, nil); err != nil {
			return fmt.Errorf("importGerritChangeFromPR(%v, nil): %v", shortLink, err)
		}
		b.pendingCLs[shortLink] = cmsg
		return nil
	}

	if err := b.syncGerritCommentsToGitHub(ctx, pr, cl); err != nil {
		return fmt.Errorf("syncGerritCommentsToGitHub: %v", err)
	}

	if cmsg == cl.Commit.Msg && pr.GetDraft() == cl.WorkInProgress() {
		log.Printf("Change https://go-review.googlesource.com/q/%s is up to date; nothing to do.",
			cl.ChangeID())
		return nil
	}
	// Import PR to existing Gerrit Change.
	if err := b.importGerritChangeFromPR(ctx, pr, cl); err != nil {
		return fmt.Errorf("importGerritChangeFromPR(%v, %v): %v", shortLink, cl, err)
	}
	b.pendingCLs[shortLink] = cmsg
	return nil
}

// gerritMessageAuthorID returns the Gerrit Account ID of the author of m.
func gerritMessageAuthorID(m *maintner.GerritMessage) (int, error) {
	email := m.Author.Email()
	if !strings.Contains(email, "@") {
		return -1, fmt.Errorf("message author email %q does not contain '@' character", email)
	}
	i, err := strconv.Atoi(strings.Split(email, "@")[0])
	if err != nil {
		return -1, fmt.Errorf("strconv.Atoi: %v (email: %q)", err, email)
	}
	return i, nil
}

// gerritMessageAuthorName returns a message author's display name. To prevent a
// thundering herd of redundant comments created by posting a different message
// via postGitHubMessageNoDup in syncGerritCommentsToGitHub, it will only return
// the correct display name for messages posted after a hard-coded date.
// b.RWMutex must be Lock'ed.
func (b *bot) gerritMessageAuthorName(ctx context.Context, m *maintner.GerritMessage) (string, error) {
	t := time.Date(2018, time.November, 9, 0, 0, 0, 0, time.UTC)
	if m.Date.Before(t) {
		return m.Author.Name(), nil
	}
	id, err := gerritMessageAuthorID(m)
	if err != nil {
		return "", fmt.Errorf("gerritMessageAuthorID: %v", err)
	}
	account := b.cachedGerritAccounts[id]
	if account != nil {
		return account.Name, nil
	}
	ai, err := b.gerritClient.GetAccountInfo(ctx, strconv.Itoa(id))
	if err != nil {
		return "", fmt.Errorf("b.gerritClient.GetAccountInfo: %v", err)
	}
	b.cachedGerritAccounts[id] = &ai
	return ai.Name, nil
}

// b.RWMutex must be Lock'ed.
func (b *bot) syncGerritCommentsToGitHub(ctx context.Context, pr *github.PullRequest, cl *maintner.GerritCL) error {
	repo := pr.GetBase().GetRepo()
	for _, m := range cl.Messages {
		id, err := gerritMessageAuthorID(m)
		if err != nil {
			return fmt.Errorf("gerritMessageAuthorID: %v", err)
		}
		if id == cl.OwnerID() {
			continue
		}
		authorName, err := b.gerritMessageAuthorName(ctx, m)
		if err != nil {
			return fmt.Errorf("b.gerritMessageAuthorName: %v", err)
		}

		// NOTE: care is required to update this message.
		// GerritBot needs to avoid duplicating old messages,
		// which it does by checking whether it is about
		// to insert a duplicate. Any change to the message
		// text requires also passing the equivalent old version
		// of the text to postGitHubMessageNoDup.

		header := fmt.Sprintf("Message from %s:\n", authorName)
		msg := fmt.Sprintf(`
%s

---
Please don’t reply on this GitHub thread. Visit [golang.org/cl/%d](https://go-review.googlesource.com/c/%s/+/%d#message-%s).
After addressing review feedback, remember to [publish your drafts](https://go.dev/wiki/GerritBot#i-left-a-reply-to-a-comment-in-gerrit-but-no-one-but-me-can-see-it)!`,
			m.Message, cl.Number, cl.Project.Project(), cl.Number, m.Meta.Hash.String())

		// We used to link to the wiki on GitHub.
		// That no longer works for contextual links
		// after issue #61940.
		oldmsg := strings.Replace(msg, "https://go.dev/wiki/", "https://github.com/golang/go/wiki/", 1)

		if err := b.postGitHubMessageNoDup(ctx, repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), header, msg, []string{oldmsg}); err != nil {
			return fmt.Errorf("postGitHubMessageNoDup: %v", err)
		}
	}
	return nil
}

// gerritChangeForPR returns the Gerrit Change info associated with the given PR.
// If no change exists for pr, it returns nil (with a nil error). If multiple
// changes exist it will return the first open change, and if no open changes
// are available, the first closed change is returned.
func (b *bot) gerritChangeForPR(pr *github.PullRequest) (*gerrit.ChangeInfo, error) {
	q := fmt.Sprintf(`"%s %s"`, prefixGitFooterPR, prShortLink(pr))
	o := gerrit.QueryChangesOpt{Fields: []string{"MESSAGES"}}
	cs, err := b.gerritClient.QueryChanges(context.Background(), q, o)
	if err != nil {
		return nil, fmt.Errorf("c.QueryChanges(ctx, %q): %v", q, err)
	}
	if len(cs) == 0 {
		return nil, nil
	}
	for _, c := range cs {
		if c.Status == gerrit.ChangeStatusNew {
			return c, nil
		}
	}
	// All associated changes are closed. It doesn’t matter which one is returned.
	return cs[0], nil
}

// closePR closes pr using the information from the given Gerrit change.
func (b *bot) closePR(ctx context.Context, pr *github.PullRequest, ch *gerrit.ChangeInfo) error {
	if *dryRun {
		log.Printf("[dry run] would close PR %v", prShortLink(pr))
		return nil
	}
	msg := fmt.Sprintf(`This PR is being closed because [golang.org/cl/%d](https://go-review.googlesource.com/c/%s/+/%d) has been %s.`,
		ch.ChangeNumber, ch.Project, ch.ChangeNumber, strings.ToLower(ch.Status))
	if ch.Status != gerrit.ChangeStatusAbandoned && ch.Status != gerrit.ChangeStatusMerged {
		return fmt.Errorf("invalid status for closed Gerrit change: %q", ch.Status)
	}

	if ch.Status == gerrit.ChangeStatusAbandoned {
		if reason := getAbandonReason(ch); reason != "" {
			msg += "\n\n" + reason
		}
	}

	repo := pr.GetBase().GetRepo()
	if err := b.postGitHubMessageNoDup(ctx, repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), "", msg, nil); err != nil {
		return fmt.Errorf("postGitHubMessageNoDup: %v", err)
	}

	req := &github.IssueRequest{
		State: github.String("closed"),
	}
	_, resp, err := b.githubClient.Issues.Edit(ctx, repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), req)
	if err != nil {
		return fmt.Errorf("b.githubClient.Issues.Edit(ctx, %q, %q, %d, %+v): %v",
			repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), req, err)
	}
	logGitHubRateLimits(resp)
	return nil
}

func (b *bot) abandonCL(ctx context.Context, cl *maintner.GerritCL, shortLink string) error {
	// Don't abandon any CLs to branches other than master, as they might be
	// cherrypicks. See golang.org/issue/40151.
	if cl.Branch() != "master" {
		return nil
	}
	if *dryRun {
		log.Printf("[dry run] would abandon https://golang.org/cl/%v", cl.Number)
		return nil
	}
	// Due to issues like https://golang.org/issue/28226, Gerrit may take time
	// to catch up on the fact that a CL has been abandoned. We may have to
	// make sure that we do not attempt to abandon the same CL multiple times.
	msg := fmt.Sprintf("GitHub PR %s has been closed.", shortLink)
	return b.gerritClient.AbandonChange(ctx, cl.ChangeID(), msg)
}

// getAbandonReason returns the last abandon reason in ch,
// or the empty string if a reason doesn't exist.
func getAbandonReason(ch *gerrit.ChangeInfo) string {
	for i := len(ch.Messages) - 1; i >= 0; i-- {
		msg := ch.Messages[i]
		if msg.Tag != "autogenerated:gerrit:abandon" {
			continue
		}
		if msg.Message == "Abandoned" {
			// An abandon reason wasn't provided.
			return ""
		}
		return strings.TrimPrefix(msg.Message, "Abandoned\n\n")
	}
	return ""
}

// downloadRef calls the Gerrit API to retrieve the ref (such as refs/changes/16/81116/1)
// of the most recent patch set of the change with changeID.
func (b *bot) downloadRef(ctx context.Context, changeID string) (string, error) {
	opt := gerrit.QueryChangesOpt{Fields: []string{"CURRENT_REVISION"}}
	ch, err := b.gerritClient.GetChange(ctx, changeID, opt)
	if err != nil {
		return "", fmt.Errorf("c.GetChange(ctx, %q, %+v): %v", changeID, opt, err)
	}
	rev, ok := ch.Revisions[ch.CurrentRevision]
	if !ok {
		return "", fmt.Errorf("revisions[current_revision] is not present in %+v", ch)
	}
	return rev.Ref, nil
}

func runCmd(c *exec.Cmd) error {
	log.Printf("Executing %v", c.Args)
	if b, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("running %v: output: %s; err: %v", c.Args, b, err)
	}
	return nil
}

const gerritHostBase = "https://go.googlesource.com/"

// gerritChangeRE matches the URL to the Change within the git output when pushing to Gerrit.
var gerritChangeRE = regexp.MustCompile(`https:\/\/go-review\.googlesource\.com\/c\/[a-zA-Z0-9_\-]+\/\+\/\d+`)

// importGerritChangeFromPR mirrors the latest state of pr to cl. If cl is nil,
// then a new Gerrit Change is created.
func (b *bot) importGerritChangeFromPR(ctx context.Context, pr *github.PullRequest, cl *maintner.GerritCL) error {
	if *dryRun {
		log.Printf("[dry run] import Gerrit Change from PR %v", prShortLink(pr))
		return nil
	}
	githubRepo := pr.GetBase().GetRepo()
	gerritRepo := gerritHostBase + githubRepo.GetName() // GitHub repo name should match Gerrit repo name.
	repoDir := filepath.Join(reposRoot(), url.PathEscape(gerritRepo))

	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		cmds := []*exec.Cmd{
			exec.Command("git", "clone", "--bare", gerritRepo, repoDir),
			exec.Command("git", "-C", repoDir, "remote", "add", "github", githubRepo.GetCloneURL()),
		}
		for _, c := range cmds {
			if err := runCmd(c); err != nil {
				return err
			}
		}
	}

	worktree := fmt.Sprintf("worktree_%s_%s_%d", githubRepo.GetOwner().GetLogin(), githubRepo.GetName(), pr.GetNumber())
	worktreeDir := filepath.Join(*workdir, "tmp", worktree)
	// workTreeDir is created by the `git worktree add` command.
	defer func() {
		log.Println("Cleaning up...")
		for _, c := range []*exec.Cmd{
			exec.Command("git", "-C", worktreeDir, "checkout", "master"),
			exec.Command("git", "-C", worktreeDir, "branch", "-D", prShortLink(pr)),
			exec.Command("rm", "-rf", worktreeDir),
			exec.Command("git", "-C", repoDir, "worktree", "prune"),
			exec.Command("git", "-C", repoDir, "branch", "-D", worktree),
		} {
			if err := runCmd(c); err != nil {
				log.Print(err)
			}
		}
	}()
	prBaseRef := pr.GetBase().GetRef()
	for _, c := range []*exec.Cmd{
		exec.Command("rm", "-rf", worktreeDir),
		exec.Command("git", "-C", repoDir, "worktree", "prune"),
		exec.Command("git", "-C", repoDir, "worktree", "add", worktreeDir),
		exec.Command("git", "-C", worktreeDir, "fetch", "origin", fmt.Sprintf("+%s:%s", prBaseRef, prBaseRef)),
		exec.Command("git", "-C", worktreeDir, "fetch", "github", fmt.Sprintf("pull/%d/head", pr.GetNumber())),
	} {
		if err := runCmd(c); err != nil {
			return err
		}
	}

	mergeBaseSHA, err := cmdOut(exec.Command("git", "-C", worktreeDir, "merge-base", prBaseRef, "FETCH_HEAD"))
	if err != nil {
		return err
	}

	author, err := cmdOut(exec.Command("git", "-C", worktreeDir, "diff-tree", "--always", "--no-patch", "--format=%an <%ae>", "FETCH_HEAD"))
	if err != nil {
		return err
	}

	cmsg, err := commitMessage(pr, cl)
	if err != nil {
		return fmt.Errorf("commitMessage: %v", err)
	}
	for _, c := range []*exec.Cmd{
		exec.Command("git", "-C", worktreeDir, "checkout", "-B", prShortLink(pr), mergeBaseSHA),
		exec.Command("git", "-C", worktreeDir, "merge", "--squash", "--no-commit", "FETCH_HEAD"),
		exec.Command("git", "-C", worktreeDir, "commit", "--author", author, "-m", cmsg),
	} {
		if err := runCmd(c); err != nil {
			return err
		}
	}

	var pushOpts string
	if pr.GetDraft() {
		pushOpts = "%wip"
	} else {
		pushOpts = "%ready"
	}

	newCL := cl == nil
	if newCL {
		// Add this informational message only on CL creation.
		msg := fmt.Sprintf("This Gerrit CL corresponds to GitHub PR %s.\n\nAuthor: %s", prShortLink(pr), author)
		pushOpts += ",m=" + url.QueryEscape(msg)
	}

	// nokeycheck is specified to avoid failing silently when a review is created
	// with what appears to be a private key. Since there are cases where a user
	// would want a private key checked in (tests).
	out, err := cmdOut(exec.Command("git", "-C", worktreeDir, "push", "-o", "nokeycheck", "origin", "HEAD:refs/for/"+prBaseRef+pushOpts))
	if err != nil {
		return fmt.Errorf("could not create change: %v", err)
	}
	changeURL := gerritChangeRE.FindString(out)
	if changeURL == "" {
		return fmt.Errorf("could not find change URL in command output: %q", out)
	}
	repo := pr.GetBase().GetRepo()
	msg := fmt.Sprintf(`This PR (HEAD: %v) has been imported to Gerrit for code review.

Please visit Gerrit at %s.

**Important tips**:

* Don't comment on this PR. All discussion takes place in Gerrit.
* You need a Gmail or other Google account to [log in to Gerrit](https://go-review.googlesource.com/login/).
* To change your code in response to feedback:
  * Push a new commit to the branch used by your GitHub PR.
  * A new "patch set" will then appear in Gerrit.
  * Respond to each comment by marking as **Done** in Gerrit if implemented as suggested. You can alternatively write a reply.
  * **Critical**: you must click the [blue **Reply** button](https://go.dev/wiki/GerritBot#i-left-a-reply-to-a-comment-in-gerrit-but-no-one-but-me-can-see-it) near the top to publish your Gerrit responses.
  * Multiple commits in the PR will be squashed by GerritBot.
* The title and description of the GitHub PR are used to construct the final commit message.
  * Edit these as needed via the GitHub web interface (not via Gerrit or git).
  * You should word wrap the PR description at ~76 characters unless you need longer lines (e.g., for tables or URLs).
* See the [Sending a change via GitHub](https://go.dev/doc/contribute#sending_a_change_github) and [Reviews](https://go.dev/doc/contribute#reviews) sections of the Contribution Guide as well as the [FAQ](https://go.dev/wiki/GerritBot/#frequently-asked-questions) for details.`,
		pr.Head.GetSHA(), changeURL)
	err = b.postGitHubMessageNoDup(ctx, repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), "", msg, nil)
	if err != nil {
		return err
	}

	checkCommonMistakes := newCL
	if releaseOrInternalBranch := strings.HasPrefix(pr.Base.GetRef(), "release-branch.") ||
		strings.HasPrefix(pr.Base.GetRef(), "internal-branch."); releaseOrInternalBranch {
		// The rules for commit messages on release and internal branches
		// are different, so don't use the same rules for checking for
		// common mistakes.
		checkCommonMistakes = false
	}
	if checkCommonMistakes {
		// Check if we spot any problems with the CL according to our internal
		// set of rules, and if so, add an unresolved comment to Gerrit.
		// If the author responds to this, it also helps a reviewer see the author has
		// registered for a Gerrit account and knows how to reply in Gerrit.
		// TODO: see CL 509135 for possible follow-ups, including possibly always
		// asking explicitly if the CL is ready for review even if there are no problems,
		// and possibly reminder comments followed by ultimately automatically
		// abandoning the CL if the author never replies.
		change, err := rules.ParseCommitMessage(repo.GetName(), cmsg)
		if err != nil {
			return fmt.Errorf("failed to parse commit message for %s: %v", prShortLink(pr), err)
		}
		problems := rules.Check(change)
		if len(problems) > 0 {
			summary := rules.FormatResults(problems)
			// If needed, summary contains advice for how to edit the commit message.
			msg := fmt.Sprintf("I spotted some possible problems.\n\n"+
				"These findings are based on simple heuristics. If a finding appears wrong, briefly reply here saying so. "+
				"Otherwise, please address any problems and update the GitHub PR. "+
				"When complete, mark this comment as 'Done' and click the [blue 'Reply' button](https://go.dev/wiki/GerritBot#i-left-a-reply-to-a-comment-in-gerrit-but-no-one-but-me-can-see-it) above.\n\n"+
				"%s\n\n"+
				"(In general for Gerrit code reviews, the change author is expected to [log in to Gerrit](https://go-review.googlesource.com/login/) "+
				"with a Gmail or other Google account and then close out each piece of feedback by "+
				"marking it as 'Done' if implemented as suggested or otherwise reply to each review comment. "+
				"See the [Review](https://go.dev/doc/contribute#review) section of the Contributing Guide for details.)",
				summary)

			gcl, err := b.gerritChangeForPR(pr)
			if err != nil {
				return fmt.Errorf("could not look up CL after creation for %s: %v", prShortLink(pr), err)
			}
			unresolved := true
			ri := gerrit.ReviewInput{
				Comments: map[string][]gerrit.CommentInput{
					"/PATCHSET_LEVEL": {{Message: msg, Unresolved: &unresolved}},
				},
			}
			changeID := fmt.Sprintf("%s~%d", url.PathEscape(gcl.Project), gcl.ChangeNumber)
			err = b.gerritClient.SetReview(ctx, changeID, "1", ri)
			if err != nil {
				return fmt.Errorf("could not add findings comment to CL for %s: %v", prShortLink(pr), err)
			}
		}
	}

	return nil
}

var (
	changeIdentRE      = regexp.MustCompile(`(?m)^Change-Id: (I[0-9a-fA-F]{40})\n?`)
	CqIncludeTrybotsRE = regexp.MustCompile(`(?m)^Cq-Include-Trybots: (\S+)\n?`)
)

// commitMessage returns the text used when creating the squashed commit for pr.
// A non-nil cl indicates that pr is associated with an existing Gerrit Change.
func commitMessage(pr *github.PullRequest, cl *maintner.GerritCL) (string, error) {
	prBody := pr.GetBody()
	var changeID string
	if cl != nil {
		changeID = cl.ChangeID()
	} else {
		sms := changeIdentRE.FindStringSubmatch(prBody)
		if sms != nil {
			changeID = sms[1]
			prBody = strings.Replace(prBody, sms[0], "", -1)
		}
	}
	if changeID == "" {
		changeID = genChangeID(pr)
	}

	// LUCI requires this in the footer (hence why we do so below), but we
	// are intentionally more lenient here and allow the line to appear
	// anywhere in an attempt to catch simple mistakes.
	tryBots := CqIncludeTrybotsRE.FindStringSubmatch(prBody)
	if tryBots != nil {
		prBody = strings.Replace(prBody, tryBots[0], "", -1)
	}

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "%s\n\n%s\n\n", cleanTitle(pr.GetTitle()), prBody)
	fmt.Fprintf(&msg, "%s %s\n", prefixGitFooterChangeID, changeID)
	fmt.Fprintf(&msg, "%s %s\n", prefixGitFooterLastRev, pr.Head.GetSHA())
	fmt.Fprintf(&msg, "%s %s\n", prefixGitFooterPR, prShortLink(pr))
	if tryBots != nil {
		fmt.Fprintf(&msg, "%s %s\n", prefixGitFooterCQIncludeTrybots, tryBots[1])
	}

	// Clean the commit message up.
	cmd := exec.Command("git", "stripspace")
	cmd.Stdin = &msg
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("could not execute command %v: %v", cmd.Args, err)
	}
	return string(out), nil
}

var xRemove = regexp.MustCompile(`^x/\w+/`)

// cleanTitle removes "x/foo/" from the beginning of t.
// It's a common mistake that people make in their PR titles (since we
// use that convention for issues, but not PRs) and it's better to just fix
// it here rather than ask everybody to fix it manually.
func cleanTitle(t string) string {
	if strings.HasPrefix(t, "x/") {
		return xRemove.ReplaceAllString(t, "")
	}
	return t
}

// genChangeID returns a new Gerrit Change ID using the Pull Request’s ID.
// Change IDs are SHA-1 hashes prefixed by an "I" character.
func genChangeID(pr *github.PullRequest) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "golang_github_pull_request_id_%d", pr.GetID())
	return fmt.Sprintf("I%x", sha1.Sum(buf.Bytes()))
}

func cmdOut(cmd *exec.Cmd) (string, error) {
	log.Printf("Executing %v", cmd.Args)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running %v: output: %s; err: %v", cmd.Args, out, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func reposRoot() string {
	return filepath.Join(*workdir, "repos")
}

// getFullPR retrieves a Pull Request via GitHub’s API.
func (b *bot) getFullPR(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	pr, resp, err := b.githubClient.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("b.githubClient.Do: %v", err)
	}
	logGitHubRateLimits(resp)
	return pr, nil
}

func logGitHubRateLimits(resp *github.Response) {
	if resp == nil {
		return
	}
	log.Printf("GitHub: %d/%d calls remaining; Reset in %v", resp.Rate.Remaining, resp.Rate.Limit, time.Until(resp.Rate.Reset.Time))
}

// postGitHubMessageNoDup ensures that the message being posted on an issue does not already have the
// same exact content, except for a header which is ignored. These comments can be toggled by the user
// via a slash command /comments {on|off} at the beginning of a message.
// The oldMsgs parameter holds a list of older versions of this message;
// if one of those appears the new message is considered a dup.
// TODO(andybons): This logic is shared by gopherbot. Consolidate it somewhere.
func (b *bot) postGitHubMessageNoDup(ctx context.Context, org, repo string, issueNum int, header, msg string, oldMsgs []string) error {
	isDup := func(s string) bool {
		// TODO: check for exact match?
		if strings.Contains(s, msg) {
			return true
		}
		for _, m := range oldMsgs {
			if strings.Contains(s, m) {
				return true
			}
		}
		return false
	}

	gr := b.corpus.GitHub().Repo(org, repo)
	if gr == nil {
		return fmt.Errorf("unknown github repo %s/%s", org, repo)
	}
	var since time.Time
	var noComment bool
	var ownerID int64
	if gi := gr.Issue(int32(issueNum)); gi != nil {
		ownerID = gi.User.ID
		var dup bool
		gi.ForeachComment(func(c *maintner.GitHubComment) error {
			since = c.Updated
			if isDup(c.Body) {
				dup = true
				return nil
			}
			if c.User.ID == ownerID && strings.HasPrefix(c.Body, "/comments ") {
				if strings.HasPrefix(c.Body, "/comments off") {
					noComment = true
				} else if strings.HasPrefix(c.Body, "/comments on") {
					noComment = false
				}
			}
			return nil
		})
		if dup {
			// Comment's already been posted. Nothing to do.
			return nil
		}
	}
	// See if there is a dup comment from when GerritBot last got
	// its data from maintner.
	opt := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 1000}}
	if !since.IsZero() {
		opt.Since = &since
	}
	ics, resp, err := b.githubClient.Issues.ListComments(ctx, org, repo, issueNum, opt)
	if err != nil {
		return err
	}
	logGitHubRateLimits(resp)
	for _, ic := range ics {
		if isDup(ic.GetBody()) {
			return nil
		}
	}

	if ownerID == 0 {
		issue, resp, err := b.githubClient.Issues.Get(ctx, org, repo, issueNum)
		if err != nil {
			return err
		}
		logGitHubRateLimits(resp)
		ownerID = issue.GetUser().GetID()
	}
	for _, ic := range ics {
		if isDup(ic.GetBody()) {
			return nil
		}
		body := ic.GetBody()
		if ic.GetUser().GetID() == ownerID && strings.HasPrefix(body, "/comments ") {
			if strings.HasPrefix(body, "/comments off") {
				noComment = true
			} else if strings.HasPrefix(body, "/comments on") {
				noComment = false
			}
		}
	}
	if noComment {
		return nil
	}
	if *dryRun {
		log.Printf("[dry run] would post comment to %v/%v#%v: %q", org, repo, issueNum, msg)
		return nil
	}
	_, resp, err = b.githubClient.Issues.CreateComment(ctx, org, repo, issueNum, &github.IssueComment{
		Body: github.String(header + msg),
	})
	if err != nil {
		return err
	}
	logGitHubRateLimits(resp)
	return nil
}
