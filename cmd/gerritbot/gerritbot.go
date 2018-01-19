// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gerritbot binary converts GitHub Pull Requests to Gerrit Changes,
// updating the PR and Gerrit Change as appropriate.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"github.com/google/go-github/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/oauth2"
)

var (
	listen          = flag.String("listen", "localhost:6343", "listen address")
	autocertBucket  = flag.String("autocert-bucket", "", "if non-empty, listen on port 443 and serve a LetsEncrypt TLS cert using this Google Cloud Storage bucket as a cache")
	workdir         = flag.String("workdir", defaultWorkdir(), "where git repos and temporary worktrees are created")
	githubTokenFile = flag.String("github-token-file", filepath.Join(defaultWorkdir(), "github-token"), "file to load GitHub token from; should only contain the token text")
	gerritTokenFile = flag.String("gerrit-token-file", filepath.Join(defaultWorkdir(), "gerrit-token"), "file to load Gerrit token from; should be of form <git-email>:<token>")
	gitcookiesFile  = flag.String("gitcookies-file", "", "if non-empty, write a git http cookiefile to this location using compute metadata")
)

func main() {
	flag.Parse()
	if err := writeCookiesFile(); err != nil {
		log.Fatalf("writeCookiesFile(): %v", err)
	}
	ghc, err := githubClient()
	if err != nil {
		log.Fatalf("githubClient(): %v", err)
	}
	gc, err := gerritClient()
	if err != nil {
		log.Fatalf("gerritClient(): %v", err)
	}
	b := newBot(ghc, gc)

	ctx := context.Background()
	b.initCorpus(ctx)
	go b.corpusUpdateLoop(ctx)

	https.ListenAndServe(http.HandlerFunc(handleIndex), &https.Options{
		Addr:                *listen,
		AutocertCacheBucket: *autocertBucket,
	})
}

func defaultWorkdir() string {
	// TODO(andybons): Use os.UserCacheDir (issue 22536) when it's available.
	return filepath.Join(home(), ".gerritbot")
}

func home() string {
	h := os.Getenv("HOME")
	if h != "" {
		return h
	}
	u, err := user.Current()
	if err != nil {
		log.Fatalf("user.Current(): %v", err)
	}
	return u.HomeDir
}

func writeCookiesFile() error {
	if *gitcookiesFile == "" {
		return nil
	}
	log.Printf("Writing git http cookies file %q ...", *gitcookiesFile)
	if !metadata.OnGCE() {
		return fmt.Errorf("cannot write git http cookies file %q from metadata: not on GCE", *gitcookiesFile)
	}
	k := "gerritbot-gitcookies"
	cookies, err := metadata.ProjectAttributeValue(k)
	if cookies == "" {
		return fmt.Errorf("metadata.ProjectAttribtueValue(%q) returned an empty value", k)
	}
	if err != nil {
		return fmt.Errorf("metadata.ProjectAttribtueValue(%q): %v", k, err)
	}
	return ioutil.WriteFile(*gitcookiesFile, []byte(cookies), 0600)
}

func githubClient() (*github.Client, error) {
	token, err := githubToken()
	if err != nil {
		return nil, err
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return github.NewClient(tc), nil
}

func githubToken() (string, error) {
	if metadata.OnGCE() {
		token, err := metadata.ProjectAttributeValue("maintner-github-token")
		if err == nil {
			return token, nil
		}
	}
	slurp, err := ioutil.ReadFile(*githubTokenFile)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(slurp))
	if len(tok) == 0 {
		return "", fmt.Errorf("token from file %q cannot be empty", *githubTokenFile)
	}
	return tok, nil
}

func gerritClient() (*gerrit.Client, error) {
	username, token, err := gerritAuth()
	if err != nil {
		return nil, err
	}
	c := gerrit.NewClient("https://go-review.googlesource.com", gerrit.BasicAuth(username, token))
	return c, nil
}

func gerritAuth() (string, string, error) {
	var slurp string
	if metadata.OnGCE() {
		var err error
		slurp, err = metadata.ProjectAttributeValue("gobot-password")
		if err != nil {
			log.Printf(`Error retrieving Project Metadata "gobot-password": %v`, err)
		}
	}
	if len(slurp) == 0 {
		slurpBytes, err := ioutil.ReadFile(*gerritTokenFile)
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
)

var (
	// GitHub repos we accept PRs for, mirroring them into Gerrit CLs.
	githubRepoWhitelist = map[string]bool{
		"golang/scratch": true,
	}
	// Gerrit projects we accept PRs for.
	gerritProjectWhitelist = map[string]bool{
		"scratch": true,
	}
)

type bot struct {
	githubClient *github.Client
	gerritClient *gerrit.Client

	sync.RWMutex // Protects all fields below
	corpus       *maintner.Corpus
	importedPRs  map[string]*maintner.GerritCL // GitHub owner/repo#n -> Gerrit CL

	// CLs that have been created/updated on Gerrit for GitHub PRs but are not yet
	// reflected in the maintner corpus yet.
	pendingCLs map[string]string // GitHub owner/repo#n -> latest GitHub SHA
}

func newBot(githubClient *github.Client, gerritClient *gerrit.Client) *bot {
	return &bot{
		githubClient: githubClient,
		gerritClient: gerritClient,
		importedPRs:  map[string]*maintner.GerritCL{},
		pendingCLs:   map[string]string{},
	}
}

// initCorpus fetches a full maintner corpus, overwriting any existing data.
func (b *bot) initCorpus(ctx context.Context) error {
	b.Lock()
	defer b.Unlock()
	var err error
	b.corpus, err = godata.Get(ctx)
	if err != nil {
		return fmt.Errorf("godata.Get: %v", err)
	}
	return nil
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
		if !gerritProjectWhitelist[pname] {
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

	if err := b.corpus.GitHub().ForeachRepo(func(ghr *maintner.GitHubRepo) error {
		id := ghr.ID()
		ownerRepo := id.Owner + "/" + id.Repo
		if !githubRepoWhitelist[ownerRepo] {
			return nil
		}
		return ghr.ForeachIssue(func(issue *maintner.GitHubIssue) error {
			if issue.PullRequest && issue.Closed {
				// Clean up any reference of closed CLs within pendingCLs.
				shortLink := fmt.Sprintf("%s#%d", ownerRepo, issue.Number)
				delete(b.pendingCLs, shortLink)
				return nil
			}
			if issue.Closed || !issue.PullRequest || !issue.HasLabel("cla: yes") {
				return nil
			}
			ctx := context.Background()
			pr, err := b.getFullPR(ctx, id.Owner, id.Repo, int(issue.Number))
			if err != nil {
				return fmt.Errorf("getFullPR(ctx, %q, %q, %d): %v", id.Owner, id.Repo, issue.Number, err)
			}
			return b.processPullRequest(ctx, pr)
		})
	}); err != nil {
		log.Printf("corpus.GitHub().ForeachRepo(...): %v", err)
	}
}

// prShortLink returns text referencing a Pull Request that will be automatically
// converted into a link by GitHub.
func prShortLink(pr *github.PullRequest) string {
	repo := pr.GetBase().GetRepo()
	return fmt.Sprintf("%s/%s#%d", repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber())
}

// processPullRequest is the entry point to the state machine of mirroring a PR
// with Gerrit. PRs that are up to date with their respective Gerrit changes are
// skipped, and any with a HEAD commit SHA unequal to its Gerrit equivalent are
// imported. If the Gerrit change associated with a PR has been merged, the PR
// is closed. Those that have no associated open or merged Gerrit changes will
// result in one being created.
// b's RWMutex read-write lock must be held.
func (b *bot) processPullRequest(ctx context.Context, pr *github.PullRequest) error {
	log.Printf("Processing PR %s ...", pr.GetHTMLURL())
	shortLink := prShortLink(pr)
	cl := b.importedPRs[shortLink]
	var lastRev string
	if cl != nil {
		lastRev = cl.Footer(prefixGitFooterLastRev)
	}
	if cl != nil && b.pendingCLs[shortLink] == lastRev {
		delete(b.pendingCLs, shortLink)
	}
	if b.pendingCLs[shortLink] != "" {
		log.Printf("Changes for PR %s have yet to be mirrored in the maintner corpus. Skipping for now.", shortLink)
		return nil
	}
	if pr.GetCommits() == 0 {
		// Um. Wat?
		return fmt.Errorf("pr has 0 commits")
	}

	prHeadSHA := pr.Head.GetSHA()
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
			b.pendingCLs[shortLink] = prHeadSHA
			return nil
		}
		if err := b.importGerritChangeFromPR(ctx, pr, nil); err != nil {
			return fmt.Errorf("importGerritChangeFromPR(%v, nil): %v", shortLink, err)
		}
		b.pendingCLs[shortLink] = prHeadSHA
		return nil
	}

	if lastRev == "" {
		log.Printf("Imported CL https://go-review.googlesource.com/q/%s does not have %s footer; skipping",
			cl.ChangeID(), prefixGitFooterLastRev)
		return nil
	}

	repo := pr.GetBase().GetRepo()
	for _, m := range cl.Messages {
		if m.Author.Email() == cl.Owner().Email() {
			continue
		}
		msg := fmt.Sprintf("%s has posted review comments at [golang.org/cl/%d](https://go-review.googlesource.com/c/%s/+/%d#message-%s).",
			m.Author.Name(), cl.Number, cl.Project.Project(), cl.Number, m.Meta.Hash.String())
		b.postGitHubMessageNoDup(ctx, repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), msg)
	}

	if pr.Head.GetSHA() == lastRev {
		log.Printf("Change https://go-review.googlesource.com/q/%s is up to date; nothing to do.",
			cl.ChangeID())
		return nil
	}
	// Import PR to existing Gerrit Change.
	if err := b.importGerritChangeFromPR(ctx, pr, cl); err != nil {
		return fmt.Errorf("importGerritChangeFromPR(%v, %v): %v", shortLink, cl, err)
	}
	b.pendingCLs[shortLink] = prHeadSHA
	return nil
}

// gerritChangeForPR returns the Gerrit Change info associated with the given PR.
// If no change exists for pr, it returns nil (with a nil error). If multiple
// changes exist it will return the first open change, and if no open changes
// are available, the first closed change is returned.
func (b *bot) gerritChangeForPR(pr *github.PullRequest) (*gerrit.ChangeInfo, error) {
	q := fmt.Sprintf(`"%s %s"`, prefixGitFooterPR, prShortLink(pr))
	cs, err := b.gerritClient.QueryChanges(context.Background(), q)
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
	msg := fmt.Sprintf(`This PR is being closed because [golang.org/cl/%d](https://go-review.googlesource.com/c/%s/+/%d) has been %s.`,
		ch.ChangeNumber, ch.Project, ch.ChangeNumber, strings.ToLower(ch.Status))
	if ch.Status != gerrit.ChangeStatusAbandoned && ch.Status != gerrit.ChangeStatusMerged {
		return fmt.Errorf("invalid status for closed Gerrit change: %q", ch.Status)
	}

	repo := pr.GetBase().GetRepo()
	if err := b.postGitHubMessageNoDup(ctx, repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), msg); err != nil {
		return fmt.Errorf("postGitHubMessageNoDup: %v", err)
	}

	req := &github.IssueRequest{
		State: github.String("closed"),
	}
	_, _, err := b.githubClient.Issues.Edit(ctx, repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), req)
	if err != nil {
		return fmt.Errorf("b.githubClient.Issues.Edit(ctx, %q, %q, %d, %+v): %v",
			repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), req, err)
	}
	return nil
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

const gerritHostBase = "https://go.googlesource.com/"

var gerritChangeRE = regexp.MustCompile(`https:\/\/go-review\.googlesource\.com\/#\/c\/\w+\/\+\/\d+`)

func runCmd(c *exec.Cmd) error {
	log.Printf("Executing %v", c.Args)
	if b, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("running %v: output: %s; err: %v", c.Args, b, err)
	}
	return nil
}

// importGerritChangeFromPR mirrors the latest state of pr to cl. If cl is nil,
// then a new Gerrit Change is created.
func (b *bot) importGerritChangeFromPR(ctx context.Context, pr *github.PullRequest, cl *maintner.GerritCL) error {
	githubRepo := pr.GetBase().GetRepo()
	gerritRepo := gerritHostBase + githubRepo.GetName() // GitHub repo name should match Gerrit repo name.
	repoDir := filepath.Join(reposRoot(), url.PathEscape(gerritRepo))

	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		commitHook := filepath.Join(repoDir, "/hooks/commit-msg")

		cmds := []*exec.Cmd{
			exec.Command("git", "clone", "--bare", gerritRepo, repoDir),
			exec.Command("curl", "-Lo", commitHook, "https://gerrit-review.googlesource.com/tools/hooks/commit-msg"),
			exec.Command("chmod", "+x", commitHook),
			exec.Command("git", "-C", repoDir, "remote", "add", "github", githubRepo.GetGitURL()),
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
	for _, c := range []*exec.Cmd{
		exec.Command("rm", "-rf", worktreeDir),
		exec.Command("git", "-C", repoDir, "worktree", "prune"),
		exec.Command("git", "-C", repoDir, "worktree", "add", worktreeDir),
		exec.Command("git", "-C", worktreeDir, "pull", "origin", "master"),
	} {
		if err := runCmd(c); err != nil {
			return err
		}
	}

	commitMsg := fmt.Sprintf("%s\n\n%s\n\n%s %s\n%s %s\n",
		pr.GetTitle(),
		pr.GetBody(),
		prefixGitFooterLastRev, pr.Head.GetSHA(),
		prefixGitFooterPR, prShortLink(pr))
	if cl != nil {
		commitMsg += fmt.Sprintf("%s %s\n", prefixGitFooterChangeID, cl.ChangeID())
	}
	for _, c := range []*exec.Cmd{
		exec.Command("git", "-C", worktreeDir, "fetch", "github", fmt.Sprintf("pull/%d/head", pr.GetNumber())),
		exec.Command("git", "-C", worktreeDir, "checkout", pr.Base.GetSHA(), "-b", prShortLink(pr)),
		exec.Command("git", "-C", worktreeDir, "merge", "FETCH_HEAD"),
	} {
		if err := runCmd(c); err != nil {
			return err
		}
	}

	cmd := exec.Command("git", "-C", worktreeDir, "show", "--no-patch", "--format='%an <%ae>'", fmt.Sprintf("HEAD~%d", pr.GetCommits()-1))
	log.Printf("Executing %v", cmd.Args)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("running %v: output: %s; err: %v", cmd.Args, out, err)
	}
	author := strings.TrimSpace(string(out))

	for _, c := range []*exec.Cmd{
		exec.Command("git", "-C", worktreeDir, "reset", "--soft", fmt.Sprintf("HEAD~%d", pr.GetCommits())),
		exec.Command("git", "-C", worktreeDir, "commit", "--author", author, "-m", commitMsg),
	} {
		if err := runCmd(c); err != nil {
			return err
		}
	}

	cmd = exec.Command("git", "-C", worktreeDir, "push", "origin", "HEAD:refs/for/"+pr.GetBase().GetRef())
	log.Printf("Executing %v", cmd.Args)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("running %v: output: %s; err: %v", cmd.Args, out, err)
	}
	changeURL := gerritChangeRE.Find(out)
	if changeURL == nil {
		return fmt.Errorf("could not find change URL in command output: %q", out)
	}
	repo := pr.GetBase().GetRepo()
	msg := fmt.Sprintf(`This PR (HEAD: %v) has been imported to Gerrit for code review.

Please visit %s to see it`, pr.Head.GetSHA(), changeURL)
	return b.postGitHubMessageNoDup(ctx, repo.GetOwner().GetLogin(), repo.GetName(), pr.GetNumber(), msg)
}

func reposRoot() string {
	return filepath.Join(*workdir, "repos")
}

func (b *bot) getFullPR(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	pr, _, err := b.githubClient.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("ghc.PullRequests().Get(ctc, %q, %q, %d): %v", owner, repo, number, err)
	}
	return pr, nil
}

// postGitHubMessage posts the given message to the given issue. This is likely not the method
// you are looking to use. To ensure that duplicate messages are not posted, use postGitHubMessageNoDup.
func (b *bot) postGitHubMessage(ctx context.Context, owner, repo string, issueNum int, msg string) error {
	cmt := &github.IssueComment{Body: github.String(msg)}
	_, _, err := b.githubClient.Issues.CreateComment(ctx, owner, repo, issueNum, cmt)
	if err != nil {
		return fmt.Errorf("b.githubClient.Issues.CreateComment(ctx, %q, %q, %d, %+v): %v", owner, repo, issueNum, cmt, err)
	}
	return nil
}

// postGitHubMessageNoDup ensures that the message being posted on an issue does not already have the
// same exact content.
func (b *bot) postGitHubMessageNoDup(ctx context.Context, owner, repo string, issueNum int, msg string) error {
	opts := &github.IssueListCommentsOptions{
		ListOptions: github.ListOptions{
			PerPage: 1000,
		},
	}
	comments, _, err := b.githubClient.Issues.ListComments(ctx, owner, repo, issueNum, opts)
	if err != nil {
		return fmt.Errorf("b.githubClient.Issues.ListComments(%q, %q, %d, %+v): %v", owner, repo, issueNum, opts, err)
	}
	for _, ic := range comments {
		if ic.GetBody() == msg {
			return nil
		}
	}
	return b.postGitHubMessage(ctx, owner, repo, issueNum, msg)
}
