// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Releasebot manages the process of defining, packaging, and publishing Go
// releases. It is a work in progress; right now it only handles beta, rc and
// minor (point) releases, but eventually we want it to handle major releases too.
package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
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
	"golang.org/x/build/maintner"
)

// A Target is a release target.
type Target struct {
	Name     string // Target name as accepted by cmd/release. For example, "linux-amd64".
	TestOnly bool   // Run tests only; don't produce a release artifact.
}

var releaseTargets = []Target{
	// Source-only target.
	{Name: "src"},

	// Binary targets.
	{Name: "linux-386"},
	{Name: "linux-armv6l"},
	{Name: "linux-amd64"},
	{Name: "linux-arm64"},
	{Name: "freebsd-386"},
	{Name: "freebsd-amd64"},
	{Name: "windows-386"},
	{Name: "windows-amd64"},
	{Name: "darwin-amd64"},
	{Name: "linux-s390x"},
	{Name: "linux-ppc64le"},

	// Test-only targets.
	{Name: "linux-amd64-longtest", TestOnly: true},
	{Name: "windows-amd64-longtest", TestOnly: true},
}

var releaseModes = map[string]bool{
	"prepare": true,
	"release": true,
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: releasebot -mode {prepare|release} [-security] [-dry-run] {go1.8.5|go1.10beta2|go1.11rc1}")
	flag.PrintDefaults()
	os.Exit(2)
}

var (
	skipTestFlag = flag.String("skip-test", "linux-amd64-longtest windows-amd64-longtest", "space-separated list of test-only targets to skip (only use if sufficient testing was done elsewhere)")
)

var (
	dryRun   bool                    // only perform pre-flight checks, only log to terminal
	skipTest = make(map[string]bool) // test-only targets that should be skipped
)

func main() {
	modeFlag := flag.String("mode", "", "release mode (prepare, release)")
	flag.BoolVar(&dryRun, "dry-run", false, "only perform pre-flight checks, only log to terminal")
	security := flag.Bool("security", false, "cut a security release from the internal Gerrit")
	flag.Usage = usage
	flag.Parse()
	if *modeFlag == "" || !releaseModes[*modeFlag] || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "need to provide a valid mode and a release name")
		usage()
	}
	for _, target := range strings.Fields(*skipTestFlag) {
		if t, ok := releaseTarget(target); !ok {
			fmt.Fprintf(os.Stderr, "target %q in -skip-test=%q is not a known target\n", target, *skipTestFlag)
			usage()
		} else if !t.TestOnly {
			fmt.Fprintf(os.Stderr, "%s is not a test-only target\n", target)
			usage()
		}
		skipTest[target] = true
	}

	http.DefaultTransport = newLogger(http.DefaultTransport)

	buildenv.CheckUserCredentials()
	checkForGitCodereview()
	loadMaintner()
	loadGomoteUser()
	loadGithubAuth()
	loadGCSAuth()

	release := flag.Arg(0)

	w := &Work{
		Prepare:     *modeFlag == "prepare",
		Version:     release,
		BetaRelease: strings.Contains(release, "beta"),
		RCRelease:   strings.Contains(release, "rc"),
		Security:    *security,
	}

	// Validate release version types.
	if w.BetaRelease {
		if w.Security {
			log.Fatalf("%s is a beta version, it cannot be a security release", w.Version)
		}
		w.ReleaseBranch = "master"
	} else if w.RCRelease {
		if w.Security {
			log.Fatalf("%s is a release candidate version, it cannot be a security release", w.Version)
		}
		shortRel := strings.Split(w.Version, "rc")[0]
		w.ReleaseBranch = "release-branch." + shortRel
	} else if strings.Count(w.Version, ".") == 1 {
		// Major release like "go1.X".
		if w.Security {
			log.Fatalf("%s is a major version, it cannot be a security release", w.Version)
		}
		w.ReleaseBranch = "release-branch." + w.Version
	} else if strings.Count(w.Version, ".") == 2 {
		// Minor release or security release like "go1.X.Y".
		shortRel := w.Version[:strings.LastIndex(w.Version, ".")]
		w.ReleaseBranch = "release-branch." + shortRel
		if w.Security {
			w.ReleaseBranch += "-security"
		}
	} else {
		log.Fatalf("cannot understand version %q", w.Version)
	}

	// Find milestones.
	var err error
	w.Milestone, err = getMilestone(w.Version)
	if err != nil {
		log.Fatalf("cannot find the GitHub milestone for release %s: %v", w.Version, err)
	}
	if !w.BetaRelease && !w.RCRelease {
		nextV, err := nextVersion(w.Version)
		if err != nil {
			log.Fatalln("nextVersion:", err)
		}
		w.NextMilestone, err = getMilestone(nextV)
		if err != nil {
			log.Fatalf("cannot find the next GitHub milestone after release %s: %v", w.Version, err)
		}
	}

	w.doRelease()
}

// checkForGitCodereview exits the program if git-codereview is not installed
// in the user's path.
func checkForGitCodereview() {
	cmd := exec.Command("which", "git-codereview")
	if err := cmd.Run(); err != nil {
		log.Fatal("could not find git-codereivew: ", cmd.Args, ": ", err, "\n\n"+
			"Please install it via go get golang.org/x/review/git-codereview\n"+
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

// getMilestone returns the GitHub milestone corresponding to the specified version,
// or an error if it cannot be found.
func getMilestone(version string) (*maintner.GitHubMilestone, error) {
	// Pre-release versions of Go share the same milestone as the
	// release version, so trim the pre-release suffix, if any.
	if i := strings.Index(version, "beta"); i != -1 {
		version = version[:i]
	} else if i := strings.Index(version, "rc"); i != -1 {
		version = version[:i]
	}

	var found *maintner.GitHubMilestone
	goRepo.ForeachMilestone(func(m *maintner.GitHubMilestone) error {
		if strings.ToLower(m.Title) != version {
			return nil
		}
		found = m
		return errors.New("stop iteration")
	})
	if found == nil {
		return nil, fmt.Errorf("no milestone found for version %q", version)
	}
	return found, nil
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
	Security    bool // cut a security release from the internal Gerrit

	ReleaseIssue  int    // Release status issue number
	ReleaseBranch string // "master" for beta releases
	Dir           string // work directory ($HOME/go-releasebot-work/<release>)
	StagingDir    string // staging directory (a temporary directory inside <work>/release-staging)
	Errors        []string
	ReleaseBinary string
	Version       string
	VersionCommit string

	releaseMu   sync.Mutex
	ReleaseInfo map[string]*ReleaseInfo // map and info protected by releaseMu

	Milestone *maintner.GitHubMilestone // Milestone for the current release.
	// NextMilestone is the milestone of the next release of the same kind.
	// For major releases, it's the milestone of the next major release (e.g., 1.14 → 1.15).
	// For minor releases, it's the milestone of the next minor release (e.g., 1.14.1 → 1.14.2).
	// For other release types, it's unset.
	NextMilestone *maintner.GitHubMilestone
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
	cmd.Dir = r.dir
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
	cmd.Dir = r.dir
	if len(r.extraEnv) > 0 {
		cmd.Env = append(os.Environ(), r.extraEnv...)
	}
	return cmd.CombinedOutput()
}

func (w *Work) doRelease() {
	w.logBuf = new(bytes.Buffer)
	w.log = log.New(io.MultiWriter(os.Stdout, w.logBuf), "", log.LstdFlags)
	defer w.finally()

	w.log.Printf("starting")

	w.checkSpelling()
	w.gitCheckout()

	if !w.Security {
		w.mustIncludeSecurityBranch()
	}

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
		if !w.Security {
			w.checkReleaseBlockers()
		}
		w.checkDocs()
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
	if w.Security {
		w.log.Printf(`

The release is ready.

Please review and submit https://team-review.git.corp.google.com/q/%s
and then run the release stage.

`, changeID)
		return
	}

	w.log.Printf(`

The release is ready.

Please review and submit https://go-review.googlesource.com/q/%s
and then run the release stage.

`, changeID)
}

func (w *Work) nextStepsBeta() {
	w.log.Printf(`

The release is ready. Run with mode=release to execute it.

`)
}

func (w *Work) nextStepsRelease() {
	w.log.Printf(`

The release run is complete! Refer to the playbook for the next steps.

Thanks for riding with releasebot today.

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
	// Avoid the risk of leaking sensitive test failures on security releases.
	if dryRun || w.Security {
		return
	}
	err := postGithubComment(w.ReleaseIssue, body)
	if err != nil {
		fmt.Printf("error posting update comment: %v\n", err)
	}
}

func (w *Work) printReleaseTable(md *bytes.Buffer) {
	// TODO: print sha256
	w.releaseMu.Lock()
	defer w.releaseMu.Unlock()
	for _, target := range releaseTargets {
		fmt.Fprintf(md, "%s", mdEscape(target.Name))
		info := w.ReleaseInfo[target.Name]
		if info == nil {
			fmt.Fprintf(md, " not started\n")
			continue
		}
		for _, out := range info.Outputs {
			if out.Link == "" {
				fmt.Fprintf(md, " (~~%s~~)", mdEscape(out.Suffix))
			} else {
				fmt.Fprintf(md, " ([%s](%s))", mdEscape(out.Suffix), out.Link)
			}
		}
		if len(info.Outputs) == 0 {
			fmt.Fprintf(md, " not built")
		}
		fmt.Fprintf(md, "\n")
		if info.Msg != "" {
			fmt.Fprintf(md, "  - %s\n", strings.Replace(strings.TrimRight(info.Msg, "\n"), "\n", "\n    ", -1))
		}
	}
}

func (w *Work) checkDocs() {
	// Check that the major version is listed on the project page.
	data, err := ioutil.ReadFile(filepath.Join(w.Dir, "gitwork", "doc/contrib.html"))
	if err != nil {
		w.log.Panic(err)
	}
	major := major(w.Version)
	if !strings.Contains(string(data), major) {
		w.logError("doc/contrib.html does not list major version %s", major)
	}
}

// major takes a go version like "go1.5", "go1.5.1", "go1.5.2", etc.,
// and returns the corresponding major version like "go1.5".
func major(v string) string {
	if strings.Count(v, ".") != 2 {
		// No minor component to drop, return as is.
		return v
	}
	return v[:strings.LastIndex(v, ".")]
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
	} else if w.Security {
		r.run("git", "codereview", "mail")
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
	if err := os.RemoveAll(gopath); err != nil {
		w.log.Panic(err)
	}
	if err := os.MkdirAll(gopath, 0777); err != nil {
		w.log.Panic(err)
	}
	r := w.runner(w.Dir, "GO111MODULE=off", "GOPATH="+gopath, "GOBIN="+filepath.Join(gopath, "bin"))
	r.run("go", "get", "golang.org/x/build/cmd/release")
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

	if w.Security {
		fmt.Printf(`

Please download

	https://team.git.corp.google.com/golang/go-private/+archive/%s.tar.gz

to %s and press enter.
`, w.VersionCommit, filepath.Join(w.Dir, w.VersionCommit+".tar.gz"))

		_, err := fmt.Scanln()
		if err != nil {
			w.log.Panic(err)
		}
	}

	var wg sync.WaitGroup
	for _, target := range releaseTargets {
		w.releaseMu.Lock()
		w.ReleaseInfo[target.Name] = new(ReleaseInfo)
		w.releaseMu.Unlock()

		if target.TestOnly && skipTest[target.Name] {
			w.log.Printf("skipping test-only target %s because of -skip-test=%q flag", target.Name, *skipTestFlag)
			w.releaseMu.Lock()
			w.ReleaseInfo[target.Name].Msg = fmt.Sprintf("skipped because of -skip-test=%q flag", *skipTestFlag)
			w.releaseMu.Unlock()
			continue
		}

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
	for _, target := range releaseTargets {
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
// "release" program can be flaky, it tries up to five times. The release files
// are first written to a staging directory specified in w.StagingDir
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
	case target.TestOnly:
		files = []string{prefix + "test-only"}
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
				"-version", w.Version, "-staging_dir", w.StagingDir}
			if w.Security {
				args = append(args, "-tarball", filepath.Join(w.Dir, w.VersionCommit+".tar.gz"))
			} else {
				args = append(args, "-rev", w.VersionCommit)
			}
			// The prepare step will run the tests on a commit that has the same
			// tree (but maybe different message) as the one that the release
			// step will process, so we can skip tests the second time.
			if !w.Prepare {
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
				break
			}
			w.log.Printf("release -target=%q did not produce expected output files %v:\nerror from cmd/release binary = %v\noutput from cmd/release binary:\n%s", target.Name, files, releaseError, releaseOutput)
			if failures++; failures >= 3 {
				w.log.Printf("release -target=%q: too many failures\n", target.Name)
				for _, out := range outs {
					w.releaseMu.Lock()
					out.Error = fmt.Sprintf("release -target=%q: build failed", target.Name)
					w.releaseMu.Unlock()
				}
				return
			}
			time.Sleep(1 * time.Minute)
		}
	}

	if dryRun || w.Prepare {
		return
	}

	if target.TestOnly {
		// This was a test-only target, nothing to upload.
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
	} else if target.TestOnly {
		return errors.New("attempted to upload a test-only target")
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

// mustIncludeSecurityBranch remotely checks if there is an associated release branch
// for the current release. If one exists, it ensures that the HEAD commit in the latest
// security release branch exists within the current release branch. If the latest security
// branch has changes which have not been merged into the proposed release, it will exit
// fatally. If an asssociated security release branch does not exist, the function will
// return without doing the check. It assumes that if the security branch doesn't exist,
// it's because it was already merged everywhere and deleted.
func (w *Work) mustIncludeSecurityBranch() {
	securityReleaseBranch := fmt.Sprintf("%s-security", w.ReleaseBranch)

	sha, ok := w.gitRemoteBranchCommit(privateGoRepoURL, securityReleaseBranch)
	if !ok {
		w.log.Printf("an associated security release branch %q does not exist; assuming it has been merged and deleted, so proceeding as usual", securityReleaseBranch)
		return
	}

	if !w.gitCommitExistsInBranch(sha) {
		log.Fatalf("release branch does not contain security release HEAD commit %q; aborting", sha)
	}
}

// releaseTarget returns a release target with the specified name.
func releaseTarget(name string) (_ Target, ok bool) {
	for _, t := range releaseTargets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}
