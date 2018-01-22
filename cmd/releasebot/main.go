// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Releasebot manages the process of defining, packaging, and publishing Go releases.
// It is a work in progress; right now it only handles beta and minor (point) releases,
// but eventually we want it to handle major releases too.
//
// Release process
//
//
package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
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

	"github.com/google/go-github/github"
	"golang.org/x/build/gerrit"
)

var releaseTargets = []string{
	"src",
	"linux-386",
	"linux-armv6l",
	"linux-amd64",
	"linux-arm64",
	"freebsd-386",
	"freebsd-amd64",
	"windows-386",
	"windows-amd64",
	"darwin-amd64",
	"linux-s390x",
	"linux-ppc64le",
}

var githubCherryPickApprovers = map[string]bool{
	"aclements":      true,
	"andybons":       true,
	"bradfitz":       true,
	"broady":         true,
	"ianlancetaylor": true,
	"rsc":            true,
}

var releaseModes = map[string]bool{
	"beta":              true,
	"release-candidate": true,
	"final":             true,
	"close-milestone":   true,
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: releasebot -mode [release mode] go1.8.5 go1.9.2 go1.10")
	fmt.Fprintln(os.Stderr, "Release modes:")
	fmt.Fprintln(os.Stderr)
	for m := range releaseModes {
		fmt.Fprintln(os.Stderr, m)
	}
	os.Exit(2)
}

func main() {
	modeFlag := flag.String("mode", "", "release mode (beta, release-candidate, final, close-milestone)")
	flag.Usage = usage
	flag.Parse()
	if *modeFlag == "" || !releaseModes[*modeFlag] || flag.NArg() == 0 {
		usage()
	}

	http.DefaultTransport = newLogger(http.DefaultTransport)

	checkForGitCodereview()
	loadGithubAuth()
	loadGerritAuth()
	loadGCSAuth()

	miles, err := loadMilestones()
	if err != nil {
		log.Fatal(err)
	}

	var wg sync.WaitGroup
Args:
	for _, release := range flag.Args() {
		for _, m := range miles {
			if strings.ToLower(m.GetTitle()) == release {
				w := &Work{Milestone: m}
				w.BetaRelease = *modeFlag == "beta"
				w.FinalRelease = *modeFlag == "final"
				w.CloseMilestone = *modeFlag == "close-milestone"
				w.setVersion(release)
				wg.Add(1)
				go func() {
					defer wg.Done()
					w.doRelease()
				}()
				continue Args
			}
		}
		log.Printf("cannot find release %s", release)
	}
	wg.Wait()
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

// Work collects all the work state for managing a particular release.
// The intent is that the code could be used in a setting where one program
// is managing multiple releases, although the current releasebot command line
// only accepts a single release.
type Work struct {
	logBuf   *bytes.Buffer
	log      *log.Logger
	runDir   string
	extraEnv []string

	BetaRelease    bool // this is the beta release; always cut from master
	FinalRelease   bool // this is the actual release
	CloseMilestone bool // release is done; close the issues and milestone

	Milestone     *github.Milestone // Github milestone
	ReleaseIssue  *github.Issue     // Release status issue
	ReleaseBranch string
	Picks         []*github.Issue // Issues marked cherry-pick-approved
	OtherIssues   []*github.Issue // Other issues
	Dir           string          // work directory
	CLs           []*CL
	Errors        []*Error
	ReleaseBinary string
	Version       string
	VersionCommit string
	VersionChange *gerrit.ChangeInfo

	summary     sync.Mutex
	releaseMu   sync.Mutex
	ReleaseInfo map[string]*ReleaseInfo // map and info protected by releaseMu
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

// A CL holds the state for a single CL that is to be copied into the release.
type CL struct {
	Num                 int
	Approver            string
	Gerrit              *gerrit.ChangeInfo
	Error               string
	Ref                 string
	Commit              string
	Title               string
	Order               int
	Issues              []int
	Prereq              []int
	ReleaseBranchCL     int
	ReleaseBranchGerrit *gerrit.ChangeInfo
	Errors              []*Error
}

// An Error is a problem to highlight on the status page.
type Error struct {
	CL  *CL
	Msg string
}

// logError records an error.
// The error is always shown in the "PROBLEMS WITH RELEASE"
// section at the top of the status page.
// If cl is not nil, the error is also shown in that CL's summary.
func (w *Work) logError(cl *CL, msg string) {
	e := &Error{cl, msg}
	w.Errors = append(w.Errors, e)
	if cl != nil {
		cl.Errors = append(cl.Errors, e)
	}
}

// recover should be deferred at the top of each goroutine using a Work
// (as in "defer w.recover()"). It catches and logs panics and lets the
// overall work continue executing.
func (w *Work) recover() {
	if err := recover(); err != nil {
		w.log.Printf("\n\nPANIC: %v\n\n%s", err, debug.Stack())
	}
	w.updateSummary()
}

// run runs the command and requires that it succeeds.
// If not, it logs the failure and aborts the work.
// It logs the command line.
func (w *Work) run(args ...string) {
	out, err := w.runErr(args...)
	if err != nil {
		w.log.Printf("command failed: %s\n%s", err, out)
		panic("cmd")
	}
}

// runOut runs the command, requires that it succeeds,
// and returns the command's output.
// It does not log the command line except in case of failure.
// Not logging these commands avoids filling the log with
// runs of side-effect-free commands like "git cat-file commit HEAD".
func (w *Work) runOut(args ...string) []byte {
	cmd := exec.Command(args[0], args[1:]...)
	// Make Git editor a no-op so that git codereview submit -i does not pop up
	// an editor.
	cmd.Env = []string{"EDITOR=true"}
	cmd.Dir = w.runDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.log.Printf("$ %s\n", strings.Join(args, " "))
		w.log.Printf("command failed: %s\n%s", err, out)
		panic("cmd")
	}
	return out
}

// runErr runs the given command and returns the output and status (error).
// It logs the command line.
// It retries certain known-flaky commands automatically.
func (w *Work) runErr(args ...string) ([]byte, error) {
	maxTry := 1
	try := 0
	// Gerrit sometimes returns 502 errors from git fetch
	if len(args) >= 2 && args[0] == "git" && args[1] == "fetch" {
		maxTry = 3
	}
Again:
	try++
	w.log.Printf("$ %s\n", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	// Make Git editor a no-op so that git codereview submit -i does not pop up
	// an editor.
	cmd.Env = append(os.Environ(), "EDITOR=true")
	cmd.Dir = w.runDir
	if len(w.extraEnv) > 0 {
		cmd.Env = append(cmd.Env, w.extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil && try < maxTry {
		goto Again
	}
	return out, err
}

func (w *Work) doRelease() {
	w.logBuf = new(bytes.Buffer)
	w.log = log.New(io.MultiWriter(os.Stdout, w.logBuf), "", log.LstdFlags)
	defer w.recover()

	if w.Milestone.GetClosedIssues() > 0 {
		w.logError(nil, fmt.Sprintf("%s milestone has closed issues", w.Milestone.GetTitle()))
	}

	w.log.Printf("starting")

	w.gitCheckout()
	if w.BetaRelease {
		w.buildReleases()
		return
	}

	w.findIssues()
	w.findCLs()
	w.queryGerritCLs()

	if w.CloseMilestone {
		_, err := w.runErr("git", "rev-parse", w.Version)
		if err != nil {
			w.logError(nil, fmt.Sprintf("cannot close milestone: did not find %s tag in Git repo", w.Version))
			return
		}
		w.closeIssues()
		w.closeMilestone()
		w.updateSummary()
		return
	}

	w.gitFetchCLs()
	w.orderCLs()
	w.updateSummary()
	w.cherryPickCLs()
	w.checkDocs()
	w.updateSummary()
	if w.FinalRelease && len(w.Errors)+len(w.OtherIssues) > 0 {
		w.logError(nil, "**Found errors during final release. Stopping!**")
		return
	}
	w.writeVersion()
	if w.FinalRelease && len(w.Errors)+len(w.OtherIssues) > 0 {
		w.logError(nil, "**Found errors during final release. Stopping!**")
		return
	}
	w.updateSummary()
	w.buildReleases()
	w.updateSummary()
}

func (w *Work) updateSummary() {
	if w.BetaRelease {
		return
	}

	w.summary.Lock()
	defer w.summary.Unlock()

	// TODO: Show relevant issue labels.
	var md bytes.Buffer

	if len(w.Errors) > 0 {
		fmt.Fprintf(&md, "## PROBLEMS WITH RELEASE\n\n")
		for _, e := range w.Errors {
			fmt.Fprintf(&md, "  - ")
			if e.CL != nil {
				fmt.Fprintf(&md, "%s: ", mdChangeLink(e.CL.Num))
			}
			fmt.Fprintf(&md, "%s\n", strings.Replace(strings.TrimRight(e.Msg, "\n"), "\n", "\n    ", -1))
		}
	}

	if len(w.OtherIssues) > 0 {
		fmt.Fprintf(&md, "## ISSUES MISSING FIXES\n\n")
		for _, issue := range w.OtherIssues {
			fmt.Fprintf(&md, "  - #%d %s\n", issue.GetNumber(), mdEscape(issue.GetTitle()))
		}
	}

	fmt.Fprintf(&md, "\n## Issues with fixes\n\n")
	for _, issue := range w.Picks {
		fmt.Fprintf(&md, "  - #%d %s\n", issue.GetNumber(), mdEscape(issue.GetTitle()))
		for _, cl := range w.CLs {
			for _, n := range cl.Issues {
				if n == issue.GetNumber() {
					fmt.Fprintf(&md, "    - %s per %s; %s\n", mdChangeLink(cl.Num), cl.Approver, mdEscape(cl.Title))
				}
			}
		}
	}

	fmt.Fprintf(&md, "\n## Changes on release branch\n\n")
	for _, cl := range w.CLs {
		desc := ""
		if rcl := cl.ReleaseBranchCL; rcl == cl.Num {
			desc = mdChangeLink(rcl) + " (new for release-branch)"
		} else if rcl != 0 {
			desc = mdChangeLink(rcl) + " (cherry-pick of " + mdChangeLink(cl.Num) + ")"
		} else {
			desc = "**CL missing** for cherry-pick of " + mdChangeLink(cl.Num)
		}
		fmt.Fprintf(&md, "  - %s (for", desc)
		for _, n := range cl.Issues {
			fmt.Fprintf(&md, " #%d", n)
		}
		fmt.Fprintf(&md, ")\n")
		if cl.Title != "" {
			fmt.Fprintf(&md, "    - %s\n", mdEscape(cl.Title))
		}
		for _, e := range cl.Errors {
			fmt.Fprintf(&md, "    - **ERROR**: %s\n", strings.Replace(strings.TrimRight(e.Msg, "\n"), "\n", "\n      ", -1))
		}
	}

	if w.Version != "" && w.VersionChange != nil {
		fmt.Fprintf(&md, "\n## Latest build: %s\n", mdEscape(w.Version))
		fmt.Fprintf(&md, "\n    git fetch origin %s &&\n    git checkout %s\n\n", w.VersionChange.Revisions[w.VersionChange.CurrentRevision].Ref, w.VersionCommit)
		w.printReleaseTable(&md)
	}

	fmt.Fprintf(&md, "\n## Log\n\n    ")
	md.WriteString(strings.Replace(w.logBuf.String(), "\n", "\n    ", -1))
	fmt.Fprintf(&md, "\n\n")

	body := wrapStatus(w.Milestone, md.String())
	_, _, err := githubClient.Issues.Edit(context.TODO(), projectOwner, projectRepo, w.ReleaseIssue.GetNumber(), &github.IssueRequest{
		Body: &body,
	})
	if err != nil {
		fmt.Printf("updating issue: %v\n", err)
	}
}

func (w *Work) printReleaseTable(md *bytes.Buffer) {
	w.releaseMu.Lock()
	defer w.releaseMu.Unlock()
	for _, target := range releaseTargets {
		fmt.Fprintf(md, "%s", mdEscape(target))
		info := w.ReleaseInfo[target]
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

func wrapStatus(m *github.Milestone, md string) string {
	return fmt.Sprintf("# %s release status\n\n%s\n%s", strings.Replace(m.GetTitle(), "Go", "Go ", -1), strings.TrimSpace(md), signature())
}

func signature() string {
	return fmt.Sprintf("\n— golang.org/x/build/cmd/releasebot, %v UTC\n", time.Now().UTC().Format(time.Stamp))
}

func (w *Work) checkDocs() {
	// Check that we've documented the release.
	version := strings.ToLower(w.Milestone.GetTitle())
	data, err := ioutil.ReadFile(filepath.Join(w.runDir, "../doc/devel/release.html"))
	if err != nil {
		w.log.Panic(err)
	}
	if !strings.Contains(string(data), "\n<p>\n"+version+" (released ") {
		w.logError(nil, "doc/devel/release.html does not document "+version)
	}
}

func (w *Work) writeVersion() {
	changeID := fmt.Sprintf("I%x", sha1.Sum([]byte(fmt.Sprintf("cmd/pointrelease-version-%s", w.Milestone.GetTitle()))))

	version := strings.ToLower(w.Milestone.GetTitle())
	rc := ""

	haveExisting := false
	n := 0
	if change := w.findGerritChangeForReleaseBranch(changeID); change != nil {
		w.runOut("git", "fetch", "origin", change.Revisions[change.CurrentRevision].Ref)
		out, _ := w.runErr("git", "show", change.CurrentRevision+":VERSION")
		v := strings.TrimSpace(string(out))
		i := strings.Index(v, "rc")
		if i < 0 && !w.FinalRelease {
			w.log.Panic("bad existing VERSION " + v)
		}
		var n int
		var err error
		if i >= 0 {
			n, err = strconv.Atoi(v[i+2:])
			if err != nil {
				w.log.Panic("bad existing VERSION " + v)
			}
		}

		_, parent := w.treeAndParentOfCommit(change.CurrentRevision)
		for i := len(w.CLs) - 1; i >= 0; i-- {
			cl := w.CLs[i]
			if cl.ReleaseBranchGerrit != nil {
				if cl.ReleaseBranchGerrit.CurrentRevision == parent {
					haveExisting = true
					if !w.FinalRelease || n == 0 {
						w.log.Printf("reusing %s for VERSION", change.Revisions[change.CurrentRevision].Ref)
						w.run("git", "reset", "--hard", change.CurrentRevision)
						w.Version = v
						w.VersionCommit = change.CurrentRevision
						w.VersionChange = change
						w.CLs = append(w.CLs, &CL{
							Title:           version + rc,
							ReleaseBranchCL: change.ChangeNumber,
						})
						return
					}
				}
				break
			}
		}
	}
	n++
	rc = fmt.Sprintf("rc%d", n)

	if w.FinalRelease {
		if n-1 > 0 && !haveExisting {
			w.logError(nil, fmt.Sprintf("cannot issue final release - code has changed since %src%d", version, n-1))
			return
		}
		rc = ""
	}

	err := ioutil.WriteFile(filepath.Join(w.runDir, "../VERSION"), []byte(version+rc), 0666)
	if err != nil {
		w.log.Panic(err)
	}

	desc := version + "\n\n"
	if rc != "" {
		desc += "TESTING: " + version + rc + "\n\nDO NOT REVIEW\n\n"
	}
	desc += "Change-Id: " + changeID + "\n"

	w.run("git", "commit", "-m", desc, "../VERSION")
	w.run("git", "codereview", "mail", "-trybot", "HEAD")
	change := w.topGerritCL()
	w.CLs = append(w.CLs, &CL{
		Title:           version + rc,
		ReleaseBranchCL: change.ChangeNumber,
	})
	w.Version = version + rc
	w.VersionCommit = change.CurrentRevision
	w.VersionChange = change
}

func (w *Work) buildReleaseBinary() {
	gopath := filepath.Join(w.Dir, "gopath")
	if err := os.RemoveAll(gopath); err != nil {
		w.log.Panic(err)
	}
	if err := os.MkdirAll(gopath, 0777); err != nil {
		w.log.Panic(err)
	}
	w.extraEnv = append(w.extraEnv, "GOPATH="+gopath, "GOBIN="+filepath.Join(gopath, "bin"))
	w.run("go", "get", "golang.org/x/build/cmd/release")
	w.ReleaseBinary = filepath.Join(gopath, "bin/release")
}

func (w *Work) buildReleases() {
	token := filepath.Join(os.Getenv("HOME"), ".config/gomote/user-release.token")
	if _, err := os.Stat(token); err != nil {
		w.logError(nil, fmt.Sprintf("missing %s - cannot build releases.\n**FIX**: Download https://build-dot-golang-org.appspot.com/key?builder=user-release\nand store in %s", mdEscape(token), mdEscape(token)))
		return
	}

	w.buildReleaseBinary()
	if err := os.MkdirAll(filepath.Join(w.Dir, "release"), 0777); err != nil {
		w.log.Panic(err)
	}
	w.runDir = filepath.Join(w.Dir, "release")
	w.ReleaseInfo = make(map[string]*ReleaseInfo)

	var wg sync.WaitGroup
	for _, target := range releaseTargets {
		func() {
			w.releaseMu.Lock()
			defer w.releaseMu.Unlock()
			w.ReleaseInfo[target] = new(ReleaseInfo)
		}()

		wg.Add(1)
		target := target
		go func() {
			defer wg.Done()
			defer func() {
				if err := recover(); err != nil {
					stk := strings.TrimSpace(string(debug.Stack()))
					msg := fmt.Sprintf("PANIC: %v\n\n    %s\n", mdEscape(fmt.Sprint(err)), strings.Replace(stk, "\n", "\n    ", -1))
					w.logError(nil, msg)
					w.log.Printf("\n\nBuilding %s: PANIC: %v\n\n%s", target, err, debug.Stack())
					w.releaseMu.Lock()
					w.ReleaseInfo[target].Msg = msg
					w.releaseMu.Unlock()
					w.updateSummary()
				}
			}()
			w.buildRelease(target)
		}()
	}
	wg.Wait()

	// Check for release errors and stop if any.
	if w.FinalRelease {
		w.releaseMu.Lock()
		for _, target := range releaseTargets {
			for _, out := range w.ReleaseInfo[target].Outputs {
				if out.Error != "" || len(w.Errors)+len(w.OtherIssues) > 0 {
					w.logError(nil, "RELEASE BUILD FAILED; NOT ISSUING RELEASE\n")
					w.releaseMu.Unlock()
					// Delete the release builds, in case we change something
					// before the next attempt. (For non-final releases, a change
					// would bump the release candidate number, but there's no
					// release candidate number here.)
					files, _ := filepath.Glob(filepath.Join(w.runDir, w.Version+".[a-z]*"))
					for _, f := range files {
						os.Remove(f)
					}
					return
				}
			}
		}
		w.releaseMu.Unlock()
		// TODO: Wait for Gerrit CL to have a +2?
		w.gitTagVersion()
		return
	}

	var md bytes.Buffer
	fmt.Fprintf(&md, "## %s pre-release distributions\n\n", w.Version)
	fmt.Fprintf(&md, "%s distributions are now available for testing:\n\n", w.Version)
	w.printReleaseTable(&md)
	md.WriteString(signature())

	if !w.BetaRelease {
		println("POSTING")
		com := findGithubComment(w.ReleaseIssue.GetNumber(), "## "+w.Version+" ")
		if com != nil {
			updateGithubComment(w.ReleaseIssue.GetNumber(), com, md.String())
		} else {
			postGithubComment(w.ReleaseIssue.GetNumber(), md.String())
		}
	}

	println("TAGGING")
	w.gitTagVersion()
}

// buildRelease builds the release packaging for a given target.
// Because the "release" program can be flaky, it tries up to five times.
// The release files are written to the current release directory
// ($HOME/go-releasebot-work/go1.2.3/release).
// If files for the current version are already present in that
// directory, they are reused instead of being rebuilt.
// buildRelease then uploads the release packaging to the
// gs://golang-release-staging bucket, along with
// files containing the SHA256 hash of the releases,
// for eventual use by the download page.
func (w *Work) buildRelease(target string) {
	log.Printf("BUILDRELEASE %s %s\n", w.Version, target)
	defer log.Printf("DONE BUILDRELEASE %s\n", target)
	prefix := fmt.Sprintf("%s.%s.", w.Version, target)
	var files []string
	switch {
	case strings.HasPrefix(target, "windows-"):
		files = []string{prefix + "zip", prefix + "msi"}
	case strings.HasPrefix(target, "darwin-"):
		files = []string{prefix + "tar.gz", prefix + "pkg"}
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
		_, err := os.Stat(filepath.Join(w.runDir, file))
		if err != nil {
			haveFiles = false
		}
	}
	w.releaseMu.Lock()
	w.ReleaseInfo[target].Outputs = outs
	w.releaseMu.Unlock()

	if haveFiles {
		w.log.Printf("release %s: already have %v; not rebuilding files", target, files)
	} else {
		for failures := 0; ; {
			out, err := w.runErr(w.ReleaseBinary, "-target", target, "-user", "release", "-version", w.Version, "-rev", w.VersionCommit, "-tools", w.ReleaseBranch, "-net", w.ReleaseBranch)
			// Exit code from release binary is apparently unreliable.
			// Look to see if the files we expected were created instead.
			failed := false
			w.releaseMu.Lock()
			for _, out := range outs {
				if _, err := os.Stat(filepath.Join(w.runDir, out.File)); err != nil {
					failed = true
				}
			}
			w.releaseMu.Unlock()
			if !failed {
				break
			}
			w.log.Printf("release %s: %s\n%s", target, err, out)
			if failures++; failures >= 3 {
				w.log.Printf("release %s: too many failures\n", target)
				for _, out := range outs {
					w.releaseMu.Lock()
					out.Error = fmt.Sprintf("release %s: build failed", target)
					w.releaseMu.Unlock()
				}
				return
			}
			w.updateSummary()
			time.Sleep(1 * time.Minute)
		}
	}

	for _, out := range outs {
		if err := w.uploadStagingRelease(target, out); err != nil {
			w.log.Printf("release %s: %s", target, err)
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
func (w *Work) uploadStagingRelease(target string, out *ReleaseOutput) error {
	src := filepath.Join(w.runDir, out.File)
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
