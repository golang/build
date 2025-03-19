// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Watchflakes is a program that triages apparent test flakes
// on the build.golang.org dashboards. See https://go.dev/wiki/Watchflakes.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	bbpb "go.chromium.org/luci/buildbucket/proto"
	rdbpb "go.chromium.org/luci/resultdb/proto/v1"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/cmd/watchflakes/internal/script"
	"golang.org/x/build/devapp/owners"
	"golang.org/x/build/internal/secret"
	"rsc.io/github"
)

// TODO:
// - subrepos by go commit
// - handle INFRA_FAILURE and CANCELED

var _ = fmt.Print

// Query failures within most recent timeLimit.
const timeLimit = 45 * 24 * time.Hour

const maxFailPerBuild = 3

const tooManyToBeFlakes = 4

var (
	build   = flag.String("build", "", "a particular build ID or URL to analyze (mainly for debugging)")
	md      = flag.Bool("md", false, "print Markdown output suitable for GitHub issues")
	post    = flag.Bool("post", false, "post updates to GitHub issues")
	repeat  = flag.Duration("repeat", 0, "keep running with specified `period`; zero means to run once and exit")
	verbose = flag.Bool("v", false, "print verbose posting decisions")

	useSecretManager = flag.Bool("use-secret-manager", false, "fetch GitHub token from Secret Manager instead of $HOME/.netrc")
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: watchflakes [options] [script]\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	log.SetPrefix("watchflakes: ")
	flag.Usage = usage
	buildenv.RegisterStagingFlag()
	flag.Parse()
	if flag.NArg() > 1 {
		usage()
	}

	var query *Issue
	if flag.NArg() == 1 {
		s, err := script.Parse("script", flag.Arg(0), fields)
		if err != nil {
			log.Fatalf("parsing query:\n%s", err)
		}
		query = &Issue{Issue: new(github.Issue), Script: s, ScriptText: flag.Arg(0)}
	}

	// Create an authenticated GitHub client.
	if *useSecretManager {
		// Fetch credentials from Secret Manager.
		secretCl, err := secret.NewClientInProject(buildenv.FromFlags().ProjectName)
		if err != nil {
			log.Fatalln("failed to create a Secret Manager client:", err)
		}
		ghToken, err := secretCl.Retrieve(context.Background(), secret.NameWatchflakesGitHubToken)
		if err != nil {
			log.Fatalln("failed to retrieve GitHub token from Secret Manager:", err)
		}
		gh = github.NewClient(ghToken)
	} else {
		// Use credentials in $HOME/.netrc.
		var err error
		gh, err = github.Dial("")
		if err != nil {
			log.Fatalln("github.Dial:", err)
		}
	}

	// Load LUCI dashboards
	c := NewLUCIClient(runtime.GOMAXPROCS(0) * 4)
	c.TraceSteps = true

	var ticker *time.Ticker
	timeout := 30 * time.Minute // default timeout for one-off run
	if *repeat != 0 {
		ticker = time.NewTicker(*repeat)
		timeout = *repeat * 2 // expected to finish in one repeat cycle, give some extra room
	}
Repeat:
	startTime := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	reportBrokenBots(ctx, c)
	var boards []*Dashboard
	if *build == "" {
		// fetch the dashboard
		var err error
		boards, err = c.ListBoards(ctx)
		if err != nil {
			log.Fatalln("ListBoards:", err)
		}
		err = c.ReadBoards(ctx, boards, startTime.Add(-timeLimit))
		if err != nil {
			log.Fatalln("ReadBoards:", err)
		}
		skipBrokenCommits(boards)
		skipBrokenBuilders(boards)
	} else {
		id, err := strconv.ParseInt(strings.TrimPrefix(*build, "https://ci.chromium.org/b/"), 10, 64)
		if err != nil {
			log.Fatalf("invalid build ID for -build flag: %q\n\texpect a build ID or https://ci.chromium.org/b/ URL", *build)
		}
		r, err := c.GetBuild(ctx, id)
		if err != nil {
			log.Fatalf("GetBuild %d: %v", id, err)
		}
		// make a one-entry board
		board := &Dashboard{
			Builders: []Builder{{r.Builder, r.BuilderConfigProperties}},
			Commits:  []Commit{{Hash: r.Commit}},
			Results:  [][]*BuildResult{{r}},
		}
		boards = []*Dashboard{board}
	}

	failRes := c.FindFailures(ctx, boards)
	c.FetchLogs(failRes)

	if *verbose {
		for _, r := range failRes {
			fmt.Printf("failure %s %s %s\n", r.Builder, shortHash(r.Commit), buildURL(r.ID))
		}
	}

	// Load GitHub issues
	var issues []*Issue
	issues, err := readIssues(issues)
	if err != nil {
		log.Fatal(err)
	}
	findScripts(issues)
	if query == nil {
		postIssueErrors(issues)
	}
	if query != nil {
		issues = []*Issue{query}
	}

	for _, r := range failRes {
		newIssue := 0
		fs := r.Failures
		fs = coalesceFailures(fs)
		if len(fs) == 0 {
			// No test failure, Probably a build failure.
			// E.g. https://ci.chromium.org/ui/b/8759448820419452721
			// Make a dummy failure.
			f := &Failure{
				Status:  rdbpb.TestStatus_FAIL,
				LogText: r.StepLogText,
			}
			fs = []*Failure{f}
		}
		for _, f := range fs {
			fp := NewFailurePost(r, f)
			record := fp.Record()
			action, targets := run(issues, record)
			if *verbose {
				printRecord(record, false)
				fmt.Printf("\t%s %v\n", action, targets)
			}
			switch action {
			case "skip":
				// do nothing
				if *verbose {
					fmt.Printf("%s: skipped by #%d\n", fp.URL, targets[0].Number)
				}

			case "":
				if newIssue > 0 {
					// If we already opened a new issue for a build, don't open another one.
					// It could be that the build is just broken.
					// One can look at the issue and split if necessary.
					break
				}

				// create a new issue
				if query == nil {
					if *verbose {
						fmt.Printf("%s: new issue\n", fp.URL)
					}
					issue, err := prepareNew(fp)
					if err != nil {
						log.Fatal(err)
					}
					issues = append(issues, issue)
					newIssue++
				}

			case "default", "post", "take":
				for _, issue := range targets {
					if !issue.Mentions[fp.URL] && issue.Stale {
						readComments(issue)
					}
					if *verbose {
						mentioned := "un"
						if issue.Mentions[fp.URL] {
							mentioned = "already "
						}
						fmt.Printf("%s: %s #%d, %smentioned\n", fp.URL, action, issue.Number, mentioned)
					}
					if !issue.Mentions[fp.URL] {
						issue.Post = append(issue.Post, fp)
					}
				}
			}
		}
	}
	for _, issue := range issues {
		if issue.Number == 0 && len(issue.Post) >= tooManyToBeFlakes && issue.Post[0].Top {
			// New issue. Check if it is failing consistently at top.
			top := 0
			for _, fp := range issue.Post {
				if fp.Top {
					top++
				}
			}
			if top >= tooManyToBeFlakes {
				issue.Title += " [consistent failure]"
			}
		}
	}

	if query != nil {
		format := (*FailurePost).Text
		if *md {
			format = (*FailurePost).Markdown
		}
		for i, fp := range query.Post {
			if i > 0 {
				fmt.Printf("\n")
			}
			os.Stdout.WriteString(format(fp))
		}
		if *md {
			os.Stdout.WriteString(signature)
		}
		return
	}

	// Check if we're about to post too many new issues.
	//
	// This shouldn't happen normally, but might happen if the GitHub API were to
	// misbehave and return an incomplete list of issues and no error, as it might
	// have done in go.dev/issue/72731.
	if *post {
		const tooManyNewIssues = 100
		newIssues := 0
		for _, issue := range issues {
			if issue.Number == 0 {
				newIssues++
			}
		}
		if newIssues >= tooManyNewIssues {
			err := fmt.Errorf("need to create %d new issues, which might be a recurrence of go.dev/issue/72731; %d boards, %d failures, %d issues, in %v", newIssues, len(boards), len(failRes), len(issues), time.Since(startTime))
			log.Println("Backing off and then crashing out of abundance of caution:", err)
			time.Sleep(30 * time.Minute)
			log.Fatalln("Now crashing out of abundance of caution:", err)
		}
	}

	posts := 0
	for _, issue := range issues {
		if len(issue.Post) > 0 {
			if *post && issue.Number == 0 {
				issue.Issue = postNew(issue.Title, issue.Body)
			}
			fmt.Printf(" - new for #%d %s\n", issue.Number, issue.Title)
			for _, fp := range issue.Post {
				fmt.Printf("    - %s\n      %s\n", fp, fp.URL)
			}
			msg := updateText(issue)
			if *verbose {
				fmt.Printf("\n%s\n", indent(spaces[:3], msg))
			}
			if *post {
				if err := postComment(issue, msg); err != nil {
					log.Print(err)
					continue
				}
				if issue.Mentions == nil {
					issue.Mentions = make(map[string]bool)
				}
				for _, fp := range issue.Post {
					issue.Mentions[fp.URL] = true
				}
			}
			posts++
		}
	}

	cancel()

	log.Printf("Done. %d boards, %d failures, %d issues, %d posts, in %v\n", len(boards), len(failRes), len(issues), posts, time.Since(startTime))

	if *repeat != 0 {
		<-ticker.C
		goto Repeat
	}
}

func reportBrokenBots(ctx context.Context, c *LUCIClient) {
	// query for broken bots
	brokenBots, err := c.ListBrokenBots(ctx, filterOutDarwin)
	if err != nil {
		log.Printf("failed to query for bots: %s", err)
		return
	}
	// query for existing broken bot issues
	existingIssues, err := readBuilderIssues()
	if err != nil {
		log.Printf("failed querying for existing builder issues: %s", err)
		return
	}
	// map used as set to check for existing issues for a bot ID.
	// botID -> issue number
	botIssues := make(map[string]int)
	for _, issue := range existingIssues {
		if botID, ok := botIDFromIssueBody(issue.Body); ok {
			fmt.Printf("found existing issue: %+v for %s\n", issue, botID)
			botIssues[botID] = issue.Number
		}
	}
	po, err := getPlatformOwners()
	if err != nil {
		log.Printf("failed to query for platform owners: %s", err)
	}

	// lastSeenThreshold is the minimum amount of time a dead bot has not been
	// seen in before an issue is created for it.
	lastSeenThreshold := 24 * time.Hour

	// for each broken bot, is there an existing open issue?
	for _, bot := range brokenBots {
		// Do not open to an issue when a dead bot has not been dead for at least the lastSeenThreshold amount.
		if bot.Dead && bot.LastSeen.After(time.Now().Add(-lastSeenThreshold)) {
			fmt.Printf("broken bot %q has not been dead for %s. Skipping issue creation/updates.", bot.ID, lastSeenThreshold.String())
			continue
		}
		if issueID, ok := botIssues[bot.ID]; ok {
			fmt.Printf("issue #%d found for broken bot %s\n", issueID, bot.ID)
			continue
		}
		title := brokenBotIssueTitle(bot.ID)
		var botOwners []string
		if v, ok := po[bot.Goos]; ok {
			for _, bo := range v {
				botOwners = append(botOwners, "@"+bo)
			}
		}
		if v, ok := po[bot.Goarch]; ok {
			for _, bo := range v {
				botOwners = append(botOwners, "@"+bo)
			}
		}
		if !*post {
			fmt.Printf("dry-run: skipped posting a new broken bot issue for %s\n", bot.ID)
			continue
		}
		i, err := postNewBrokenBot(title, brokenBotIssueBody(bot, botOwners))
		if err != nil {
			log.Printf("failed to post broken bot issue: %s", err)
			continue
		}
		fmt.Printf("Posted new broken bot issue for %s, issue: %d\n", bot.ID, i.Number)
	}
}

func getPlatformOwners() (map[string][]string, error) {
	url := "https://dev.golang.org/owners"
	var o owners.Request
	o.Payload.Platform = true

	body, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal json: %s", err)
	}
	r, err := http.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to query for platform owners: %s", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to query for platform owners, response code=%d", r.StatusCode)
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read http response body: %w", err)
	}
	var response owners.Response
	err = json.Unmarshal(b, &response)
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal owners response: %s", err)
	}
	m := map[string][]string{}
	for k, v := range response.Payload.Platforms {
		if len(v.Primary) == 0 {
			continue
		}
		var primaries []string
		for _, p := range v.Primary {
			primaries = append(primaries, p.GitHubUsername)
		}
		m[k] = primaries
	}
	return m, nil
}

var issueFooter = regexp.MustCompile(`<!-- DO NOT EDIT: (.*?) -->`)

func botIDFromIssueBody(body string) (string, bool) {
	matches := issueFooter.FindStringSubmatch(body)
	if len(matches) != 2 {
		return "", false
	}
	return matches[1], true
}

func brokenBotIssueTitle(botID string) string {
	return fmt.Sprintf("x/build: bot %s reported as broken", botID)
}

func brokenBotIssueBody(bot Bot, owners []string) string {
	var state string
	if bot.Dead {
		state = "dead"
	} else if bot.Quarantined {
		state = "quarantined"
	} else {
		state = "unknown"
	}
	botURL := fmt.Sprintf("https://chromium-swarm.appspot.com/bot?id=%s", bot.ID)
	body := "The bot [%s](%s) has been reported as broken. It is currently in %q state. Please work to resolve the issue.\n\n%s\n%s"
	footer := fmt.Sprintf("<!-- DO NOT EDIT: %s -->", bot.ID)
	return fmt.Sprintf(body, bot.ID, botURL, state, strings.Join(owners, "\n"), footer)
}

const SKIP = bbpb.Status_STATUS_UNSPECIFIED // for smashing the status to skip a non-flake failure

// skipBrokenCommits identifies broken commits,
// which are the ones that failed on at least 1/4 of builders,
// and then changes all results for those commits to SKIP.
func skipBrokenCommits(boards []*Dashboard) {
	for _, dash := range boards {
		builderThreshold := len(dash.Builders) / 4
		for i := 0; i < len(dash.Commits); i++ {
			bad := 0
			good := 0
			for _, rs := range dash.Results {
				if rs[i] == nil {
					continue
				}
				switch rs[i].Status {
				case bbpb.Status_SUCCESS:
					good++
				case bbpb.Status_FAILURE:
					bad++
					// ignore other status
				}
			}
			if bad > builderThreshold || good < builderThreshold {
				fmt.Printf("skip: commit %s (%s %s) is broken (good=%d bad=%d)\n", shortHash(dash.Commits[i].Hash), dash.Repo, dash.GoBranch, good, bad)
				for _, rs := range dash.Results {
					if rs[i] != nil {
						rs[i].Status = SKIP
					}
				}
			}
		}
	}
}

// skipBrokenBuilders identifies builders that were consistently broken
// (at least tooManyToBeFlakes failures in a row) and then turned ok.
// It changes those consistent failures to SKIP.
//
// It does not skip consistent failures at the top (latest few commits).
// Instead, it sets Top to true on them.
func skipBrokenBuilders(boards []*Dashboard) {
	for _, dash := range boards {
		for _, rs := range dash.Results {
			bad := 0
			badStart := 0
			top := true
			skip := func(i int) { // skip the i-th result
				if rs[i] != nil {
					fmt.Printf("skip: builder %s was broken at %s (%s %s)\n", rs[i].Builder, shortHash(rs[i].Commit), dash.Repo, dash.GoBranch)
					rs[i].Status = SKIP
				}
			}
			for i, r := range rs {
				if rs[i] == nil {
					continue
				}
				switch r.Status {
				case bbpb.Status_SUCCESS:
					if top && bad < tooManyToBeFlakes {
						// Skip the run at the top.
						// Too few to tell if it is flaky or consistent.
						// It may also get fixed soon.
						for j := 0; j < i; j++ {
							skip(j)
						}
					}
					top = false
					bad = 0
					continue
				case bbpb.Status_FAILURE:
					bad++
					if top {
						// Set Top to true, but don't skip.
						r.Top = true
						continue
					}
				default: // ignore other status
					continue
				}
				switch bad {
				case 1:
					badStart = i
				case tooManyToBeFlakes:
					// Skip the run so far.
					for j := badStart; j < i; j++ {
						skip(j)
					}
				}
				if bad >= tooManyToBeFlakes {
					skip(i)
				}
			}

			// Bad entries ending just before the cutoff are not flakes
			// even if there are just a few of them. Otherwise we get
			// spurious flakes when there's one bad entry before the
			// cutoff and lots after the cutoff.
			if bad > 0 {
				for j := badStart; j < len(rs); j++ {
					skip(j)
				}
			}
		}
	}
}

// run runs the scripts in issues on record.
// It returns the desired action (skip, post, default)
// as well as the list of target issues (for post or default).
func run(issues []*Issue, record script.Record) (action string, targets []*Issue) {
	var def, post []*Issue

	for _, issue := range issues {
		if issue.Script != nil {
			switch issue.Script.Action(record) {
			case "skip":
				return "skip", []*Issue{issue}
			case "take":
				println("TAKE", issue.Number)
			case "default":
				def = append(def, issue)
			case "post":
				post = append(post, issue)
			}
		}
	}

	if len(post) > 0 {
		return "post", post
	}
	if len(def) > 0 {
		return "default", def[:1]
	}
	return "", nil
}

// FailurePost is a failure to be posted on an issue.
type FailurePost struct {
	*BuildResult
	*Failure
	URL     string // LUCI build page
	Pkg     string
	Test    string
	Snippet string
}

func NewFailurePost(r *BuildResult, f *Failure) *FailurePost {
	pkg, test := splitTestID(f.TestID)
	snip := snippet(f.LogText)
	if snip == "" {
		snip = snippet(r.LogText)
	}
	fp := &FailurePost{
		BuildResult: r,
		Failure:     f,
		URL:         buildURL(r.ID),
		Pkg:         pkg,
		Test:        test,
		Snippet:     snip,
	}
	return fp
}

// fields is the list of known fields for use by script patterns.
// It must be in sync with the Record method below.
var fields = []string{
	"",
	"section", // not used, keep for compatibility with old watchflakes
	"pkg",
	"test",
	"mode",
	"output",
	"snippet",
	"date",
	"builder",
	"repo",
	"goos",
	"goarch",
	"log",
	"status",
}

func (fp *FailurePost) Record() script.Record {
	// Note: update fields above if any new fields are added to this record.
	m := script.Record{
		"pkg":     fp.Pkg,
		"test":    fp.Test,
		"output":  fp.Failure.LogText,
		"snippet": fp.Snippet,
		"date":    fp.Time.Format(time.RFC3339),
		"builder": fp.Builder,
		"repo":    fp.Repo,
		"goos":    fp.Target.GOOS,
		"goarch":  fp.Target.GOARCH,
		"log":     fp.BuildResult.LogText,
		"status":  fp.Failure.Status.String(),
	}
	m[""] = m["output"] // default field for `regexp` search (as opposed to field ~ `regexp`)
	if fp.IsBuildFailure() {
		m["mode"] = "build"
	}
	return m
}

func printRecord(r script.Record, verbose bool) {
	fmt.Printf("%s %s %s %s %s %s\n", r["date"], r["builder"], r["goos"], r["goarch"],
		r["pkg"], r["test"])
	if verbose {
		fmt.Printf("%s\n", indent(spaces[:4], r["snippet"]))
	}
}

func (fp *FailurePost) IsBuildFailure() bool {
	// no test ID. dummy for build failure.
	return fp.Failure.TestID == ""
}

// String returns a header to identify the log and failure.
func (fp *FailurePost) String() string {
	repo := fp.Repo
	sep := ""
	if fp.GoCommit != "" {
		sep = " go@"
	}
	if fp.GoBranch != "" && fp.GoBranch != "master" {
		b := strings.TrimPrefix(fp.GoBranch, " release-branch.")
		if repo == "go" {
			repo = b
		}
		if sep == " go@" {
			sep = " " + b + "@"
		}
	}
	s := fmt.Sprintf("%s %s %s@%s%s%s",
		fp.Time.Format("2006-01-02 15:04"),
		fp.Builder, repo, shortHash(fp.Commit),
		sep, shortHash(fp.GoCommit))

	if fp.Pkg != "" || fp.Test != "" {
		s += " " + shortPkg(fp.Pkg)
		if fp.Pkg != "" && fp.Test != "" {
			s += "."
		}
		s += fp.Test
	}
	if fp.IsBuildFailure() {
		s += " [build]"
	}
	if fp.Failure.Status != rdbpb.TestStatus_FAIL {
		s += fmt.Sprintf(" [%s]", fp.Failure.Status)
	}
	return s
}

// Markdown returns Markdown suitable for posting to GitHub.
func (fp *FailurePost) Markdown() string {
	return fmt.Sprintf("<details><summary>%s (<a href=\"%s\">log</a>)</summary>\n\n%s</details>\n",
		fp.String(), fp.URL, indent(spaces[:4], fp.Snippet))
}

// Text returns text suitable for reading in interactive use or debug logging.
func (fp *FailurePost) Text() string {
	return fmt.Sprintf("%s\n%s\n%s\n", fp, fp.URL, strings.TrimRight(fp.Snippet, "\n"))
}

var spaces = strings.Repeat(" ", 100)

// indent returns a copy of text in which every line has been indented by prefix.
// It also ensures that, except when text is empty, text ends in a \n character.
func indent(prefix, text string) string {
	if text == "" {
		return ""
	}
	text = strings.TrimRight(text, "\n")
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix) + "\n"
}

// shortPkg shortens pkg by removing any leading
// golang.org/ (for packages like golang.org/x/sys/windows).
func shortPkg(pkg string) string {
	pkg = strings.TrimPrefix(pkg, "golang.org/")
	return pkg
}

// shorten the output lines to form a snippet
func snippet(log string) string {
	lines := strings.SplitAfter(log, "\n")

	// Remove beginning and trailing blank lines.
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	// If we have more than 30 lines, make the snippet by taking the first 10,
	// the last 10, and possibly a middle 10. The middle 10 is included when
	// the interior lines (between the first and last 10) contain an important-looking
	// message like "panic:" or "--- FAIL:". The middle 10 start at the important-looking line.
	// such as
	if len(lines) > 30 {
		var keep []string
		keep = append(keep, lines[:10]...)
		dots := true
		for i := 10; i < len(lines)-10; i++ {
			s := strings.TrimSpace(lines[i])
			if strings.HasPrefix(s, "panic:") || strings.HasPrefix(s, "fatal error:") || strings.HasPrefix(s, "--- FAIL:") || strings.Contains(s, ": internal compiler error:") {
				if i > 10 {
					keep = append(keep, "...\n")
				}
				end := i + 10
				if end >= len(lines)-10 {
					dots = false
					end = len(lines) - 10
				}
				keep = append(keep, lines[i:end]...)
				break
			}
		}
		if dots {
			keep = append(keep, "...\n")
		}
		keep = append(keep, lines[len(lines)-10:]...)
		lines = keep
	}

	return strings.Join(lines, "")
}

// If a build that has too many failures, the build is probably broken
// (e.g. timeout, crash). Coalesce the failures and report maxFailPerBuild
// of them.
func coalesceFailures(fs []*Failure) []*Failure {
	var res []*Failure
	// A subtest fail may cause the parent test to fail, combine them.
	// So is a test failure causing package-level failure.
	var cur *Failure
	for _, f := range fs {
		if cur != nil {
			cpkg, ctst := splitTestID(cur.TestID)
			if (ctst == "" && strings.HasPrefix(f.TestID, cpkg+".")) ||
				strings.HasPrefix(f.TestID, cur.TestID+"/") {
				// 1. cur is a package-level failure, and f is a test in that package
				// 2. f is a subtest of cur.
				// In either case, consume cur, replace with f.
				res[len(res)-1] = f
				cur = f
				continue
			}
		}
		cur = f
		res = append(res, f)
	}
	if len(res) <= maxFailPerBuild {
		return res
	}

	// If multiple subtests fail under the same parent, pick one that is
	// more likely to be helpful. Prefer the one containing "FAIL", then
	// the longer log message.
	moreLikelyUseful := func(f, last *Failure) bool {
		return strings.Contains(f.LogText, "--- FAIL") &&
			(!strings.Contains(last.LogText, "--- FAIL") || len(f.LogText) > len(last.LogText))
	}
	siblingSubtests := func(f, last *Failure) bool {
		pkg, tst := splitTestID(f.TestID)
		pkg2, tst2 := splitTestID(last.TestID)
		if pkg != pkg2 {
			return false
		}
		par, _, ok := strings.Cut(tst, "/")
		par2, _, ok2 := strings.Cut(tst2, "/")
		return ok && ok2 && par == par2
	}
	cur = nil
	fs = res
	res = fs[:0]
	for _, f := range fs {
		if cur != nil && siblingSubtests(f, cur) {
			if moreLikelyUseful(f, res[len(res)-1]) {
				res[len(res)-1] = f
			}
			continue
		}
		cur = f
		res = append(res, f)
	}
	if len(res) <= maxFailPerBuild {
		return res
	}

	// If there are still too many failures, coalesce by package (pick one with longest log).
	fs = res
	res = fs[:0]
	curpkg := ""
	for _, f := range fs {
		pkg, _ := splitTestID(f.TestID)
		if curpkg != "" && curpkg == pkg {
			if moreLikelyUseful(f, res[len(res)-1]) {
				res[len(res)-1] = f
			}
			continue
		}
		curpkg = pkg
		res = append(res, f)
	}
	if len(res) > maxFailPerBuild {
		res = res[:maxFailPerBuild]
	}
	return res
}
