// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Releasebot manages the process of defining,
// packaging, and publishing Go releases.
package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/envutil"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/build/maintner"
)

// A Target is a release target.
type Target struct {
	Name      string // Target name as accepted by cmd/release. For example, "linux-amd64".
	SkipTests bool   // Skip tests.
}

var releaseModes = map[string]bool{
	"prepare": true,
	"release": true,

	"mail-dl-cl": true,

	"tweet-minor": true,
	"tweet-beta":  true,
	"tweet-rc":    true,
	"tweet-major": true,
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: releasebot -mode {prepare|release|mail-dl-cl|tweet-{minor,beta,rc,major}} [-security] [-dry-run] {go1.8.5|go1.10beta2|go1.11rc1}")
	flag.PrintDefaults()
	os.Exit(2)
}

var (
	skipTestFlag     = flag.String("skip-test", "", "space-separated list of targets for which to skip tests (only use if sufficient testing was done elsewhere)")
	skipTargetFlag   = flag.String("skip-target", "", "space-separated list of targets to skip. This will require manual intervention to create artifacts for a target after releasing.")
	skipAllTestsFlag = flag.Bool("skip-all-tests", false, "skip all tests for all targets (only use if tests were verified elsewhere)")
)

var (
	dryRun bool // only perform pre-flight checks, only log to terminal
)

func main() {
	modeFlag := flag.String("mode", "", "release mode (prepare, release)")
	flag.BoolVar(&dryRun, "dry-run", false, "only perform pre-flight checks, only log to terminal")
	flag.Usage = usage
	flag.Parse()
	if !releaseModes[*modeFlag] {
		fmt.Fprintln(os.Stderr, "need to provide a valid mode")
		usage()
	} else if *modeFlag == "mail-dl-cl" {
		mailDLCL()
		return
	} else if strings.HasPrefix(*modeFlag, "tweet-") {
		kind := (*modeFlag)[len("tweet-"):]
		postTweet(kind)
		return
	} else if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "need to provide a release name")
		usage()
	}
	releaseVersion := flag.Arg(0)
	releaseTargets, ok := releasetargets.TargetsForVersion(releaseVersion)
	if !ok {
		fmt.Fprintf(os.Stderr, "could not parse release name %q\n", releaseVersion)
		usage()
	}
	for _, target := range strings.Fields(*skipTestFlag) {
		t, ok := releaseTargets[target]
		if !ok {
			fmt.Fprintf(os.Stderr, "target %q in -skip-test=%q is not a known target\n", target, *skipTestFlag)
			usage()
		}
		t.LongTestBuilder = ""
		t.BuildOnly = true
	}
	for _, target := range strings.Fields(*skipTargetFlag) {
		if _, ok := releaseTargets[target]; !ok {
			fmt.Fprintf(os.Stderr, "target %q in -skip-target=%q is not a known target\n", target, *skipTargetFlag)
			usage()
		}
		delete(releaseTargets, target)
	}

	http.DefaultTransport = newLogger(http.DefaultTransport)

	buildenv.CheckUserCredentials()
	checkForGitCodereview()
	loadMaintner()
	loadGomoteUser()
	loadGithubAuth()
	loadGCSAuth()

	w := &Work{
		Prepare:     *modeFlag == "prepare",
		Version:     releaseVersion,
		BetaRelease: strings.Contains(releaseVersion, "beta"),
		RCRelease:   strings.Contains(releaseVersion, "rc"),
	}

	// Validate release version types.
	if w.BetaRelease {
		w.ReleaseBranch = "master"
	} else if w.RCRelease {
		shortRel := strings.Split(w.Version, "rc")[0]
		w.ReleaseBranch = "release-branch." + shortRel
	} else if strings.Count(w.Version, ".") == 1 {
		// Major release like "go1.X".
		w.ReleaseBranch = "release-branch." + w.Version
	} else if strings.Count(w.Version, ".") == 2 {
		// Minor release or security release like "go1.X.Y".
		shortRel := w.Version[:strings.LastIndex(w.Version, ".")]
		w.ReleaseBranch = "release-branch." + shortRel
	} else {
		log.Fatalf("cannot understand version %q", w.Version)
	}

	w.ReleaseTargets = []Target{{Name: "src"}}
	for name, release := range releaseTargets {
		w.ReleaseTargets = append(w.ReleaseTargets, Target{Name: name, SkipTests: release.BuildOnly || *skipAllTestsFlag})
	}

	// Find milestone.
	var err error
	w.Milestone, err = findMilestone(w.Version)
	if err != nil {
		log.Fatalf("cannot find the GitHub milestone for release %s: %v", w.Version, err)
	}

	w.doRelease()
}

// mailDLCL parses command-line arguments for the mail-dl-cl mode,
// and runs it.
func mailDLCL() {
	if flag.NArg() != 1 && flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "need to provide 1 or 2 versions")
		usage()
	}
	versions := flag.Args()

	versionTasks := &task.VersionTasks{}
	if !dryRun {
		auth, err := loadGerritAuth()
		if err != nil {
			log.Fatalln("error loading Gerrit API credentials:", err)
		}
		versionTasks.Gerrit = &task.RealGerritClient{Client: gerrit.NewClient(gerritAPIURL, auth)}
	}

	fmt.Printf("About to create a golang.org/dl CL for the following Go versions:\n\n\t• %s\n\nOk? (Y/n) ", strings.Join(versions, "\n\t• "))
	var resp string
	if _, err := fmt.Scanln(&resp); err != nil {
		log.Fatalln(err)
	} else if resp != "Y" && resp != "y" {
		log.Fatalln("stopped as requested")
	}
	changeID, err := versionTasks.MailDLCL(&workflow.TaskContext{Context: context.Background(), Logger: log.Default()}, versions, dryRun)
	if err != nil {
		log.Fatalf(`task.MailDLCL(ctx, %#v, extCfg) failed:

	%v

If it's necessary to perform it manually as a workaround,
consider the following steps:

	git clone https://go.googlesource.com/dl && cd dl
	# create files displayed in the log above
	git add .
	git commit -m "dl: add goX.Y.Z and goX.A.B"
	git codereview mail -trybot

Discuss with the secondary release coordinator as needed.`, versions, err)
	}
	fmt.Printf("\nPlease review and submit %s\nand then refer to the playbook for the next steps.\n\n", task.ChangeLink(changeID))
}

// postTweet parses command-line arguments for the tweet-* modes,
// and runs it.
// kind must be one of "minor", "beta", "rc", or "major".
func postTweet(kind string) {
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "need to provide 1 release tweet JSON object")
		usage()
	}
	var tweet task.ReleaseTweet
	err := json.Unmarshal([]byte(flag.Arg(0)), &tweet)
	if err != nil {
		log.Fatalln("error parsing release tweet JSON object:", err)
	}

	extCfg := task.ExternalConfig{
		DryRun: dryRun,
	}
	if !dryRun {
		var err error
		extCfg.TwitterAPI, err = loadTwitterAuth()
		if err != nil {
			log.Fatalln("error loading Twitter API credentials:", err)
		}
	}

	versions := []string{tweet.Version}
	if tweet.SecondaryVersion != "" {
		versions = append(versions, tweet.SecondaryVersion+" (secondary)")
	}
	fmt.Printf("About to tweet about the release of the following Go versions:\n\n\t• %s\n\n", strings.Join(versions, "\n\t• "))
	if tweet.Security != "" {
		fmt.Printf("with the following security sentence (%d characters long):\n\n\t%s\n\n", len([]rune(tweet.Security)), tweet.Security)
	} else {
		fmt.Print("with no security fixes being mentioned,\n\n")
	}
	if tweet.Announcement != "" {
		fmt.Printf("and with the following announcement URL:\n\n\t%s\n\n", tweet.Announcement)
	}
	fmt.Print("Ok? (Y/n) ")
	var resp string
	if _, err = fmt.Scanln(&resp); err != nil {
		log.Fatalln(err)
	} else if resp != "Y" && resp != "y" {
		log.Fatalln("stopped as requested")
	}
	tweetRelease := map[string]func(*workflow.TaskContext, task.ReleaseTweet, task.ExternalConfig) (string, error){
		"minor": task.TweetMinorRelease,
		"beta":  task.TweetBetaRelease,
		"rc":    task.TweetRCRelease,
		"major": task.TweetMajorRelease,
	}[kind]
	tweetURL, err := tweetRelease(&workflow.TaskContext{Context: context.Background(), Logger: log.Default()}, tweet, extCfg)
	if errors.Is(err, task.ErrTweetTooLong) && len([]rune(tweet.Security)) > 120 {
		log.Fatalf(`A tweet was not created because it's too long.

The provided security sentence is somewhat long (%d characters),
so try making it shorter to avoid exceeding Twitter's limits.`, len([]rune(tweet.Security)))
	} else if err != nil {
		log.Fatalf(`tweetRelease(ctx, %#v, extCfg) failed:

	%v

If it's necessary to perform it manually as a workaround,
consider the following options:

	• use the template displayed in the log above (if any)
	• use the same format as the last tweet for the release
	  of the same kind

Discuss with the secondary release coordinator as needed.`, tweet, err)
	}
	fmt.Printf("\nPlease check that %s looks okay\nand then refer to the playbook for the next steps.\n\n", tweetURL)
}

// checkForGitCodereview exits the program if git-codereview is not installed
// in the user's path.
func checkForGitCodereview() {
	cmd := exec.Command("which", "git-codereview")
	if err := cmd.Run(); err != nil {
		log.Fatal("could not find git-codereivew: ", cmd.Args, ": ", err, "\n\n"+
			"Please install it via go install golang.org/x/review/git-codereview@latest\n"+
			"to use this program.")
	}
}

var gomoteUser string

func loadGomoteUser() {
	tokenPath := filepath.Join(os.Getenv("HOME"), ".config/gomote")
	files, _ := ioutil.ReadDir(tokenPath)
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if strings.HasSuffix(name, ".token") && strings.HasPrefix(name, "user-") {
			gomoteUser = strings.TrimPrefix(strings.TrimSuffix(name, ".token"), "user-")
			return
		}
	}
	log.Fatal("missing gomote token - cannot build releases.\n**FIX**: Download https://build-dot-golang-org.appspot.com/key?builder=user-YOURNAME\nand store in ~/.config/gomote/user-YOURNAME.token")
}

// findMilestone finds the GitHub milestone corresponding to the specified Go version.
// If there isn't exactly one open GitHub milestone that matches, an error is returned.
func findMilestone(version string) (*maintner.GitHubMilestone, error) {
	// Pre-release versions of Go share the same milestone as the
	// release version, so trim the pre-release suffix, if any.
	if i := strings.Index(version, "beta"); i != -1 {
		version = version[:i]
	} else if i := strings.Index(version, "rc"); i != -1 {
		version = version[:i]
	}

	var open, closed []*maintner.GitHubMilestone
	goRepo.ForeachMilestone(func(m *maintner.GitHubMilestone) error {
		if strings.ToLower(m.Title) != version {
			return nil
		}
		if !m.Closed {
			open = append(open, m)
		} else {
			closed = append(closed, m)
		}
		return nil
	})
	if len(open) == 1 {
		// Happy path: found exactly one open matching milestone.
		return open[0], nil
	} else if len(open) == 0 && len(closed) == 0 {
		return nil, errors.New("no milestone found")
	}
	// Something's really unexpected.
	// Include all relevant information to help the human who'll need to sort it out.
	var buf strings.Builder
	buf.WriteString("found duplicate or closed milestones:\n")
	for _, m := range open {
		fmt.Fprintf(&buf, "\t• open milestone %q (https://github.com/golang/go/milestone/%d)\n", m.Title, m.Number)
	}
	for _, m := range closed {
		fmt.Fprintf(&buf, "\t• closed milestone %q (https://github.com/golang/go/milestone/%d)\n", m.Title, m.Number)
	}
	return nil, errors.New(buf.String())
}

func nextVersion(version string) (string, error) {
	parts := strings.Split(version, ".")
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return "", err
	}
	parts[len(parts)-1] = strconv.Itoa(n + 1)
	return strings.Join(parts, "."), nil
}

// Work collects all the work state for managing a particular release.
// The intent is that the code could be used in a setting where one program
// is managing multiple releases, although the current releasebot command line
// only accepts a single release.
type Work struct {
	logBuf *bytes.Buffer
	log    *log.Logger

	Prepare     bool // create the release commit and submit it for review
	BetaRelease bool
	RCRelease   bool

	ReleaseIssue   int    // Release status issue number
	ReleaseBranch  string // "master" for beta releases
	Dir            string // work directory ($HOME/go-releasebot-work/<release>)
	StagingDir     string // staging directory (a temporary directory inside <work>/release-staging)
	Errors         []string
	ReleaseBinary  string
	ReleaseTargets []Target // Selected release targets for this release.
	Version        string
	VersionCommit  string

	releaseMu   sync.Mutex
	ReleaseInfo map[string]*ReleaseInfo // map and info protected by releaseMu

	Milestone *maintner.GitHubMilestone // Milestone for the current release.
}

// ReleaseInfo describes a release build for a specific target.
type ReleaseInfo struct {
	Outputs []*ReleaseOutput
	Msg     string
}

// ReleaseOutput describes a single release file.
type ReleaseOutput struct {
	File   string
	Suffix string
	Link   string
	Error  string
}

// logError records an error.
// The error is always shown in the "PROBLEMS WITH RELEASE"
// section at the top of the status page.
// If cl is not nil, the error is also shown in that CL's summary.
func (w *Work) logError(msg string, a ...interface{}) {
	w.Errors = append(w.Errors, fmt.Sprintf(msg, a...))
}

// finally should be deferred at the top of each goroutine using a Work
// (as in "defer w.finally()"). It catches and logs panics and posts
// the log.
func (w *Work) finally() {
	if err := recover(); err != nil {
		w.log.Printf("\n\nPANIC: %v\n\n%s", err, debug.Stack())
	}
	w.postSummary()
}

type runner struct {
	w        *Work
	dir      string
	extraEnv []string
}

func (w *Work) runner(dir string, env ...string) *runner {
	return &runner{
		w:        w,
		dir:      dir,
		extraEnv: env,
	}
}

// run runs the command and requires that it succeeds.
// If not, it logs the failure and aborts the work.
// It logs the command line.
func (r *runner) run(args ...string) {
	out, err := r.runErr(args...)
	if err != nil {
		r.w.log.Printf("command failed: %s\n%s", err, out)
		panic("command failed")
	}
}

// runOut runs the command, requires that it succeeds,
// and returns the command's output.
// It does not log the command line except in case of failure.
// Not logging these commands avoids filling the log with
// runs of side-effect-free commands like "git cat-file commit HEAD".
func (r *runner) runOut(args ...string) []byte {
	cmd := exec.Command(args[0], args[1:]...)
	envutil.SetDir(cmd, r.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.w.log.Printf("$ %s\n", strings.Join(args, " "))
		r.w.log.Printf("command failed: %s\n%s", err, out)
		panic("command failed")
	}
	return out
}

// runErr runs the given command and returns the output and status (error).
// It logs the command line.
func (r *runner) runErr(args ...string) ([]byte, error) {
	r.w.log.Printf("$ %s\n", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	envutil.SetDir(cmd, r.dir)
	envutil.SetEnv(cmd, r.extraEnv...)
	return cmd.CombinedOutput()
}

func (w *Work) doRelease() {
	w.logBuf = new(bytes.Buffer)
	w.log = log.New(io.MultiWriter(os.Stdout, w.logBuf), "", log.LstdFlags)
	defer w.finally()

	w.log.Printf("starting")

	w.checkSpelling()
	w.gitCheckout()

	// In release mode we carry on even if the tag exists, in case we
	// need to resume a failed build.
	if w.Prepare && w.gitTagExists() {
		w.logError("%s tag already exists in Go repository!", w.Version)
		w.logError("**Found errors during release. Stopping!**")
		return
	}
	if w.BetaRelease || w.RCRelease {
		// TODO: go tool api -allow_new=false
		if strings.HasSuffix(w.Version, "beta1") {
			w.checkBeta1ReleaseBlockers()
		}
	} else {
		w.checkReleaseBlockers()
	}
	w.findOrCreateReleaseIssue()
	if len(w.Errors) > 0 && !dryRun {
		w.logError("**Found errors during release. Stopping!**")
		return
	}

	if w.Prepare {
		var changeID string
		if !w.BetaRelease {
			changeID = w.writeVersion()
		}

		// Create release archives and run all.bash tests on the builders.
		w.VersionCommit = w.gitHeadCommit()
		w.buildReleases()
		if len(w.Errors) > 0 {
			w.logError("**Found errors during release. Stopping!**")
			return
		}

		if w.BetaRelease {
			w.nextStepsBeta()
		} else {
			w.nextStepsPrepare(changeID)
		}
	} else {
		if !w.BetaRelease {
			w.checkVersion()
		}
		if len(w.Errors) > 0 {
			w.logError("**Found errors during release. Stopping!**")
			return
		}

		// Create and push the Git tag for the release, then create or reuse release archives.
		// (Tests are skipped here since they ran during the prepare mode.)
		w.gitTagVersion()
		w.buildReleases()
		if len(w.Errors) > 0 {
			w.logError("**Found errors during release. Stopping!**")
			return
		}

		switch {
		case !w.BetaRelease && !w.RCRelease:
			w.pushIssues()
			w.closeMilestone()
		case w.BetaRelease && strings.HasSuffix(w.Version, "beta1"):
			w.removeOkayAfterBeta1()
		}
		w.nextStepsRelease()
	}
}

func (w *Work) checkSpelling() {
	if w.Version != strings.ToLower(w.Version) {
		w.logError("release name should be lowercase: %q", w.Version)
	}
	if strings.Contains(w.Version, " ") {
		w.logError("release name should not contain any spaces: %q", w.Version)
	}
	if !strings.HasPrefix(w.Version, "go") {
		w.logError("release name should have 'go' prefix: %q", w.Version)
	}
}

func (w *Work) checkReleaseBlockers() {
	if err := goRepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Milestone == nil || gi.Milestone.ID != w.Milestone.ID {
			return nil
		}
		if !gi.Closed && gi.HasLabel("release-blocker") {
			w.logError("open issue #%d is tagged release-blocker", gi.Number)
		}
		return nil
	}); err != nil {
		w.logError("error checking release-blockers: %v", err.Error())
		return
	}
}

func (w *Work) checkBeta1ReleaseBlockers() {
	if err := goRepo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Milestone == nil || gi.Milestone.ID != w.Milestone.ID {
			return nil
		}
		if !gi.Closed && gi.HasLabel("release-blocker") && !gi.HasLabel("okay-after-beta1") {
			w.logError("open issue #%d is tagged release-blocker and not okay after beta1", gi.Number)
		}
		return nil
	}); err != nil {
		w.logError("error checking release-blockers: %v", err.Error())
		return
	}
}

func (w *Work) nextStepsPrepare(changeID string) {
	w.log.Printf(`

The prepare stage has completed.

Please review and submit https://go-review.googlesource.com/q/%s
and then run the release stage.

`, changeID)
}

func (w *Work) nextStepsBeta() {
	w.log.Printf(`

The prepare stage has completed.

Please run the release stage next.

`)
}

func (w *Work) nextStepsRelease() {
	w.log.Printf(`

The release stage has completed. Thanks for riding with releasebot today!

Please refer to the playbook for the next steps.

`)
}

func (w *Work) postSummary() {
	var md bytes.Buffer

	if len(w.Errors) > 0 {
		fmt.Fprintf(&md, "## PROBLEMS WITH RELEASE\n\n")
		for _, e := range w.Errors {
			fmt.Fprintf(&md, "  - ")
			fmt.Fprintf(&md, "%s\n", strings.Replace(strings.TrimRight(e, "\n"), "\n", "\n    ", -1))
		}
	}

	if !w.Prepare {
		fmt.Fprintf(&md, "\n## Latest build: %s\n\n", mdEscape(w.Version))
		w.printReleaseTable(&md)
	}

	fmt.Fprintf(&md, "\n## Log\n\n    ")
	md.WriteString(strings.Replace(w.logBuf.String(), "\n", "\n    ", -1))
	fmt.Fprintf(&md, "\n\n")

	if len(w.Errors) > 0 {
		fmt.Fprintf(&md, "There were problems with the release, see above for details.\n")
	}

	body := md.String()
	fmt.Printf("%s", body)
	if dryRun {
		return
	}

	// Ensure that the entire body can be posted to the issue by splitting it into multiple
	// GitHub comments if necessary. See golang.org/issue/45998.
	bodyParts := splitLogMessage(body, githubCommentCharacterLimit)
	for _, b := range bodyParts {
		err := postGithubComment(w.ReleaseIssue, b)
		if err != nil {
			fmt.Printf("error posting update comment: %v\n", err)
		}
	}
}

func (w *Work) printReleaseTable(md *bytes.Buffer) {
	// TODO: print sha256
	w.releaseMu.Lock()
	defer w.releaseMu.Unlock()
	for _, target := range w.ReleaseTargets {
		fmt.Fprintf(md, "- %s", mdEscape(target.Name))
		info := w.ReleaseInfo[target.Name]
		if info == nil {
			fmt.Fprintf(md, " - not started\n")
			continue
		}
		if len(info.Outputs) == 0 {
			fmt.Fprintf(md, " - not built")
		}
		for _, out := range info.Outputs {
			if out.Link != "" {
				fmt.Fprintf(md, " ([%s](%s))", mdEscape(out.Suffix), out.Link)
			} else {
				fmt.Fprintf(md, " (~~%s~~)", mdEscape(out.Suffix))
			}
		}
		fmt.Fprintf(md, "\n")
		if info.Msg != "" {
			fmt.Fprintf(md, "  - %s\n", strings.Replace(strings.TrimRight(info.Msg, "\n"), "\n", "\n    ", -1))
		}
	}
}

func (w *Work) writeVersion() (changeID string) {
	changeID = fmt.Sprintf("I%x", sha1.Sum([]byte(fmt.Sprintf("cmd/release-version-%s", w.Version))))

	err := ioutil.WriteFile(filepath.Join(w.Dir, "gitwork", "VERSION"), []byte(w.Version), 0666)
	if err != nil {
		w.log.Panic(err)
	}

	desc := w.Version + "\n\n"
	desc += "Change-Id: " + changeID + "\n"

	r := w.runner(filepath.Join(w.Dir, "gitwork"))
	r.run("git", "add", "VERSION")
	r.run("git", "commit", "-m", desc, "VERSION")
	if dryRun {
		fmt.Printf("\n### VERSION commit\n\n%s\n", r.runOut("git", "show", "HEAD"))
	} else {
		r.run("git", "codereview", "mail", "-trybot")
	}
	return
}

// checkVersion makes sure that the version commit has been submitted.
func (w *Work) checkVersion() {
	ver, err := ioutil.ReadFile(filepath.Join(w.Dir, "gitwork", "VERSION"))
	if err != nil {
		w.log.Panic(err)
	}
	if string(ver) != w.Version {
		w.logError("VERSION is %q; want %q. Did you run prepare and submit the CL?", string(ver), w.Version)
	}
}

func (w *Work) buildReleaseBinary() {
	gopath := filepath.Join(w.Dir, "gopath")
	r := w.runner(w.Dir, "GOPATH="+gopath, "GOBIN="+filepath.Join(gopath, "bin"))
	r.run("go", "clean", "-modcache")
	if err := os.RemoveAll(gopath); err != nil {
		w.log.Panic(err)
	}
	if err := os.MkdirAll(gopath, 0777); err != nil {
		w.log.Panic(err)
	}
	r.run("go", "install", "golang.org/x/build/cmd/release@latest")
	w.ReleaseBinary = filepath.Join(gopath, "bin/release")
}

func (w *Work) buildReleases() {
	w.buildReleaseBinary()
	if err := os.MkdirAll(filepath.Join(w.Dir, "release", w.VersionCommit), 0777); err != nil {
		w.log.Panic(err)
	}
	if err := os.MkdirAll(filepath.Join(w.Dir, "release-staging"), 0777); err != nil {
		w.log.Panic(err)
	}
	stagingDir, err := ioutil.TempDir(filepath.Join(w.Dir, "release-staging"), w.VersionCommit+"_")
	if err != nil {
		w.log.Panic(err)
	}
	w.StagingDir = stagingDir
	w.ReleaseInfo = make(map[string]*ReleaseInfo)

	var wg sync.WaitGroup
	for _, target := range w.ReleaseTargets {
		w.releaseMu.Lock()
		w.ReleaseInfo[target.Name] = new(ReleaseInfo)
		w.releaseMu.Unlock()

		wg.Add(1)
		target := target
		go func() {
			defer wg.Done()
			defer func() {
				if err := recover(); err != nil {
					stk := strings.TrimSpace(string(debug.Stack()))
					msg := fmt.Sprintf("PANIC: %v\n\n    %s\n", mdEscape(fmt.Sprint(err)), strings.Replace(stk, "\n", "\n    ", -1))
					w.logError(msg)
					w.log.Printf("\n\nBuilding %s: PANIC: %v\n\n%s", target.Name, err, debug.Stack())
					w.releaseMu.Lock()
					w.ReleaseInfo[target.Name].Msg = msg
					w.releaseMu.Unlock()
				}
			}()
			w.buildRelease(target)
		}()
	}
	wg.Wait()

	// Check for release errors and stop if any.
	w.releaseMu.Lock()
	for _, target := range w.ReleaseTargets {
		for _, out := range w.ReleaseInfo[target.Name].Outputs {
			if out.Error != "" || len(w.Errors) > 0 {
				w.logError("RELEASE BUILD FAILED\n")
				w.releaseMu.Unlock()
				return
			}
		}
	}
	w.releaseMu.Unlock()
}

// buildRelease builds the release packaging for a given target. Because the
// "release" program can be flaky, it tries multiple times before stopping.
// The release files are first written to a staging directory specified in w.StagingDir
// (a temporary directory inside $HOME/go-releasebot-work/go1.2.3/release-staging),
// then after the all.bash tests complete successfully (or get skipped),
// they get moved to the final release directory
// ($HOME/go-releasebot-work/go1.2.3/release/COMMIT_HASH).
//
// If files for the current version commit are already present in the release directory,
// they are reused instead of being rebuilt. In release mode, buildRelease then uploads
// the release packaging to the gs://golang-release-staging bucket, along with files
// containing the SHA256 hash of the releases, for eventual use by the download page.
func (w *Work) buildRelease(target Target) {
	log.Printf("BUILDRELEASE %s %s\n", w.Version, target.Name)
	defer log.Printf("DONE BUILDRELEASE %s %s\n", w.Version, target.Name)
	releaseDir := filepath.Join(w.Dir, "release", w.VersionCommit)
	prefix := fmt.Sprintf("%s.%s.", w.Version, target.Name)
	var files []string
	switch {
	case strings.HasPrefix(target.Name, "windows-"):
		files = []string{prefix + "zip", prefix + "msi"}
	default:
		files = []string{prefix + "tar.gz"}
	}
	var outs []*ReleaseOutput
	haveFiles := true
	for _, file := range files {
		out := &ReleaseOutput{
			File:   file,
			Suffix: strings.TrimPrefix(file, prefix),
		}
		outs = append(outs, out)
		_, err := os.Stat(filepath.Join(releaseDir, file))
		if err != nil {
			haveFiles = false
		}
	}
	w.releaseMu.Lock()
	w.ReleaseInfo[target.Name].Outputs = outs
	w.releaseMu.Unlock()

	if haveFiles {
		w.log.Printf("release -target=%q: already have %v; not rebuilding files", target.Name, files)
	} else {
		failures := 0
		for {
			args := []string{w.ReleaseBinary, "-target", target.Name, "-user", gomoteUser,
				"-version", w.Version, "-staging_dir", w.StagingDir, "-rev", w.VersionCommit}
			// The prepare step will run the tests on a commit that has the same
			// tree (but maybe different message) as the one that the release
			// step will process, so we can skip tests the second time.
			if !w.Prepare || target.SkipTests {
				args = append(args, "-skip_tests")
			}
			releaseOutput, releaseError := w.runner(releaseDir, "GOPATH="+filepath.Join(w.Dir, "gopath")).runErr(args...)
			// Exit code from release binary is apparently unreliable.
			// Look to see if the files we expected were created instead.
			failed := false
			w.releaseMu.Lock()
			for _, out := range outs {
				if _, err := os.Stat(filepath.Join(releaseDir, out.File)); err != nil {
					failed = true
				}
			}
			w.releaseMu.Unlock()
			if !failed {
				w.log.Printf("release -target=%q: build succeeded (after %d retries)\n", target.Name, failures)
				break
			}
			w.log.Printf("release -target=%q did not produce expected output files %v:\nerror from cmd/release binary = %v\noutput from cmd/release binary:\n%s", target.Name, files, releaseError, releaseOutput)
			if failures++; failures >= 3 {
				w.log.Printf("release -target=%q: too many failed attempts, stopping\n", target.Name)
				for _, out := range outs {
					w.releaseMu.Lock()
					out.Error = fmt.Sprintf("release -target=%q: build failed", target.Name)
					w.releaseMu.Unlock()
				}
				return
			}
			w.log.Printf("release -target=%q: waiting a bit and trying again\n", target.Name)
			time.Sleep(1 * time.Minute)
		}
	}

	if dryRun || w.Prepare {
		return
	}

	for _, out := range outs {
		if err := w.uploadStagingRelease(target, out); err != nil {
			w.log.Printf("error uploading release %s to staging bucket: %s", target.Name, err)
			w.releaseMu.Lock()
			out.Error = err.Error()
			w.releaseMu.Unlock()
		}
	}
}

// uploadStagingRelease uploads target to the release staging bucket.
// If successful, it records the corresponding URL in out.Link.
// In addition to uploading target, it creates and uploads a file
// named "<target>.sha256" containing the hex sha256 hash
// of the target file. This is needed for the release signing process
// and also displayed on the eventual download page.
func (w *Work) uploadStagingRelease(target Target, out *ReleaseOutput) error {
	if dryRun {
		return errors.New("attempted write operation in dry-run mode")
	}

	src := filepath.Join(w.Dir, "release", w.VersionCommit, out.File)
	h := sha256.New()
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(src+".sha256", []byte(fmt.Sprintf("%x", h.Sum(nil))), 0666); err != nil {
		return err
	}

	dst := w.Version + "/" + out.File
	if err := gcsUpload(src, dst); err != nil {
		return err
	}
	if err := gcsUpload(src+".sha256", dst+".sha256"); err != nil {
		return err
	}

	w.releaseMu.Lock()
	out.Link = "https://" + releaseBucket + ".storage.googleapis.com/" + dst
	w.releaseMu.Unlock()
	return nil
}

// splitLogMessage splits a string into n number of strings of maximum size maxStrLen.
// It naively attempts to split the string along the boundaries of new line characters in order
// to make each individual string as readable as possible.
func splitLogMessage(s string, maxStrLen int) []string {
	sl := []string{}
	for len(s) > maxStrLen {
		end := strings.LastIndex(s[:maxStrLen], "\n")
		if end == -1 {
			end = maxStrLen
		}
		sl = append(sl, s[:end])

		if string(s[end]) == "\n" {
			s = s[end+1:]
		} else {
			s = s[end:]
		}
	}
	sl = append(sl, s)
	return sl
}
