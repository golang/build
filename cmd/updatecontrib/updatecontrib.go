// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The updateac command updates the CONTRIBUTORS file in the Go repository.
//
// This binary should be run at the top of GOROOT.
// It will try to fetch and update a bunch of subrepos in your GOPATH workspace,
// whose location is determined by running go env GOPATH.
package main // import "golang.org/x/build/cmd/updatecontrib"

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// TODO: automatically use Gerrit names like we do with GitHub

func main() {
	log.SetFlags(0)

	flag.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage:

  $ cd $(gotip env GOROOT)
  $ updateac

`)
		flag.PrintDefaults()
	}
	flag.Parse()

	all := gitAuthorEmails() // call first (it will reset CONTRIBUTORS)
	c := file("CONTRIBUTORS")
	var actions, warnings, errors bytes.Buffer
	for _, who := range all {
		// Skip exact emails that are present in CONTRIBUTORS file.
		if c.Contains(&acLine{email: who.email}) {
			continue
		}
		if !validName(who.name) {
			ghUser, err := FetchGitHubInfo(who)
			if err != nil {
				fmt.Fprintf(&errors, "Error fetching GitHub name for %s: %v\n", who.Debug(), err)
				continue
			}
			if ghUser == nil {
				fmt.Fprintf(&warnings, "There is no GitHub user associated with %s, skipping\n", who.Debug())
				continue
			}
			if validName(ghUser.Name) {
				// Use the GitHub name since it looks valid.
				fmt.Fprintf(&actions, "Used GitHub name %q for %s\n", ghUser.Name, who.Debug())
				who.name = ghUser.Name
			} else if (ghUser.Name == ghUser.Login || ghUser.Name == "") && who.name == ghUser.Login {
				// Special case: if the GitHub name is the same as the GitHub username or empty,
				// and who.name is the GitHub username, then use "GitHub User @<username> (<ID>)" form.
				fmt.Fprintf(&actions, "Used GitHub User @%s (%d) form for %s\n", ghUser.Login, ghUser.ID, who.Debug())
				who.name = fmt.Sprintf("GitHub User @%s (%d)", ghUser.Login, ghUser.ID)
			} else {
				fmt.Fprintf(&warnings, "Found invalid-looking name %q for GitHub user @%s, skipping %v\n", ghUser.Name, ghUser.Login, who.Debug())
				continue
			}
		}
		if !c.Contains(who) {
			c.addLine(who)
			fmt.Fprintf(&actions, "Added %s <%s>\n", who.name, who.firstEmail())
		} else {
			// The name exists, but with a different email. We don't update lines automatically. (TODO)
			// We'll need to update "GitHub User" names when they provide a better one.
		}
	}
	if actions.Len() > 0 {
		fmt.Println("Actions taken (relative to CONTRIBUTORS at origin/master):")
		lines := strings.SplitAfter(actions.String(), "\n")
		sort.Strings(lines)
		os.Stdout.WriteString(strings.Join(lines, ""))
	}
	err := sortACFile("CONTRIBUTORS")
	if err != nil {
		log.Fatalf("Error sorting CONTRIBUTORS file: %v", err)
	}
	if errors.Len() > 0 {
		log.Printf("\nExiting with errors:")
		lines := strings.SplitAfter(errors.String(), "\n")
		sort.Strings(lines)
		os.Stderr.WriteString(strings.Join(lines, ""))
		os.Exit(1)
	}
	if warnings.Len() > 0 {
		log.Printf("\nExiting with warnings:")
		lines := strings.SplitAfter(warnings.String(), "\n")
		sort.Strings(lines)
		os.Stderr.WriteString(strings.Join(lines, ""))
	}
}

// validName is meant to reject most invalid names with a simple rule, and a whitelist.
func validName(name string) bool {
	if valid, ok := validNames[name]; ok {
		return valid
	}
	return strings.Contains(name, " ")
}

type acFile struct {
	name    string
	lines   []*acLine
	byEmail map[string]*acLine // emailNorm(email) to line
	byName  map[string]*acLine // nameNorm(name) to line
}

func (f *acFile) Contains(who *acLine) bool {
	for _, email := range who.email {
		if _, ok := f.byEmail[emailNorm(email)]; ok {
			return true
		}
	}
	if who.name != "" {
		if _, ok := f.byName[nameNorm(who.name)]; ok {
			return true
		}
	}
	return false
}

func emailNorm(e string) string {
	return strings.Replace(strings.ToLower(e), ".", "", -1)
}

func nameNorm(e string) string {
	return strings.Replace(strings.Replace(strings.ToLower(e), ".", "", -1), ",", "", -1)
}

func (f *acFile) addLine(line *acLine) {
	of, err := os.OpenFile(f.name, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := io.WriteString(of, line.String()); err != nil {
		log.Fatal(err)
	}
	if err := of.Close(); err != nil {
		log.Fatal(err)
	}

	f.recordLine(line)
}

func (f *acFile) recordLine(ln *acLine) {
	for _, email := range ln.email {
		if _, ok := f.byEmail[emailNorm(email)]; !ok {
			f.byEmail[emailNorm(email)] = ln
		} else {
			// TODO: print for debugging, shouldn't happen
		}
	}
	if _, ok := f.byName[nameNorm(ln.name)]; !ok {
		f.byName[nameNorm(ln.name)] = ln
	} else {
		// TODO: print for debugging, shouldn't happen
	}
	f.lines = append(f.lines, ln)
}

type acLine struct {
	name        string
	email       []string
	repos       map[string]bool
	firstRepo   string
	firstCommit string
}

func (w *acLine) firstEmail() string {
	if len(w.email) > 0 {
		return w.email[0]
	}
	return ""
}

func (w *acLine) String() string {
	line := w.name
	for _, email := range w.email {
		line += fmt.Sprintf(" <%s>", email)
	}
	line += "\n"
	return line
}

func (w *acLine) Debug() string {
	repos := make([]string, 0, len(w.repos))
	for k := range w.repos {
		k = path.Base(k)
		repos = append(repos, k)
	}
	githubOrg, githubRepo := githubOrgRepo(w.firstRepo)
	email := w.firstEmail()
	if len(w.email) > 1 {
		email = fmt.Sprint(w.email)
	}
	sort.Strings(repos)
	return fmt.Sprintf("%s <%s> https://github.com/%s/%s/commit/%s %v", w.name, email,
		githubOrg, githubRepo, w.firstCommit, repos)
}

var emailRx = regexp.MustCompile(`<[^>]+>`)

func file(name string) *acFile {
	f, err := os.Open(name)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	acf := &acFile{
		name:    name,
		byName:  make(map[string]*acLine),
		byEmail: make(map[string]*acLine),
	}
	for s.Scan() {
		t := strings.TrimSpace(s.Text())
		if t == "" || t[0] == '#' {
			continue
		}
		ln := new(acLine)
		ln.name = strings.TrimSpace(emailRx.ReplaceAllStringFunc(t, func(email string) string {
			email = strings.Trim(email, "<>")
			ln.email = append(ln.email, email)
			return ""
		}))
		acf.recordLine(ln)
	}
	if err := s.Err(); err != nil {
		log.Fatal(err)
	}
	return acf
}

// repos is a list of all the repositories that are fetched (if missing),
// updated, and used to find contributors to add to the CONTRIBUTORS file.
// It includes "go", which represents the main Go repository,
// and an import path corresponding to each subrepository root.
var repos = []string{
	"go", // main repo
	"golang.org/x/arch",
	"golang.org/x/benchmarks",
	"golang.org/x/blog",
	"golang.org/x/build",
	"golang.org/x/crypto",
	"golang.org/x/debug",
	"golang.org/x/exp",
	"github.com/golang/gddo", // The canonical import path for gddo is on GitHub.
	"golang.org/x/image",
	"golang.org/x/lint",
	"golang.org/x/mobile",
	"golang.org/x/net",
	"golang.org/x/oauth2",
	"golang.org/x/perf",
	"golang.org/x/playground",
	"go.googlesource.com/proposal.git", // It doesn't have an /x/ vanity import path.
	"golang.org/x/review",
	"golang.org/x/sync",
	"golang.org/x/sys",
	"golang.org/x/talks",
	"golang.org/x/term",
	"golang.org/x/text",
	"golang.org/x/time",
	"golang.org/x/tools",
	"golang.org/x/tour",
	"golang.org/x/vgo",
	"golang.org/x/website",
	"golang.org/x/xerrors",
}

// githubOrgRepo takes an import path (from the forms in the repos global variable)
// and returns the GitHub org and repo.
func githubOrgRepo(repo string) (githubOrg, githubRepo string) {
	if repo == "go" {
		return "golang", "go"
	}
	return "golang", strings.TrimSuffix(path.Base(repo), ".git")
}

func gitAuthorEmails() []*acLine {
	goPath, err := goPath()
	if err != nil {
		log.Fatal(err)
	}
	var ret []*acLine
	seen := map[string]map[string]bool{} // email -> repo -> true
	for _, repo := range repos {
		log.Printf("Processing repo: %s", repo)
		dir := ""
		if repo != "go" {
			dir = filepath.Join(goPath, "src", repo)
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				log.Printf("go get -d %s ...", repo)
				cmd := exec.Command("go", "get", "-d", repo)
				var stderr bytes.Buffer
				cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
				if err := cmd.Run(); err != nil && !bytes.Contains(stderr.Bytes(), []byte(" no Go files ")) {
					log.Fatal(err)
				}
			}
		}
		cmd := exec.Command("git", "fetch")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Fatalf("Error updating repo %q: %v, %s", repo, err, out)
		}
		if repo == "go" {
			// Initialize CONTRIBUTORS file to latest copy from origin/master.
			exec.Command("git", "checkout", "origin/master", "--", "CONTRIBUTORS").Run()
			exec.Command("git", "reset").Run()
		}

		cmd = exec.Command("git", "log", "--format=%ae/%h/%an", "origin/master") //, "HEAD@{5 years ago}..HEAD")
		cmd.Dir = dir
		cmd.Stderr = os.Stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			log.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			log.Fatal(err)
		}
		s := bufio.NewScanner(out)
		for s.Scan() {
			line := s.Text()
			f := strings.SplitN(line, "/", 3)
			email, commit, name := f[0], f[1], f[2]
			if uselessCommit(commit) {
				continue
			}
			for _, phrase := range debugPeople {
				if strings.Contains(line, phrase) {
					log.Printf("DEBUG(%q): Repo %q, email %q, commit %s, name %q", phrase, repo, email, commit, name)
				}
			}
			if skipEmail[email] {
				continue
			}
			if v, ok := emailFix[email]; ok {
				email = v
			}
			if v, ok := nameFix[name]; ok {
				name = v
			}
			if userRepos, first := seen[email]; !first {
				userRepos = map[string]bool{repo: true}
				seen[email] = userRepos
				ret = append(ret, &acLine{
					name:        name,
					email:       []string{email},
					repos:       userRepos,
					firstRepo:   repo,
					firstCommit: commit,
				})
			} else {
				userRepos[repo] = true
			}
		}
		if err := s.Err(); err != nil {
			log.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("Done processing all repos.")
	log.Println()
	return ret
}

// goPath returns the output of running go env GOPATH.
func goPath() (string, error) {
	out, err := exec.Command("go", "env", "GOPATH").Output()
	if err != nil {
		return "", err
	}
	goPath := string(bytes.TrimSpace(out))
	if goPath == "" {
		return "", fmt.Errorf("no GOPATH")
	}
	return goPath, nil
}

// sortACFile sorts the named file in place.
func sortACFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	sorted, err := sortAC(f)
	f.Close()
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(path, sorted, 0644)
	return err
}

func sortAC(r io.Reader) ([]byte, error) {
	bs := bufio.NewScanner(r)
	var header []string
	var lines []string
	for bs.Scan() {
		t := bs.Text()
		lines = append(lines, t)
		if t == "# Please keep the list sorted." {
			header = lines
			lines = nil
			continue
		}
	}
	if err := bs.Err(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	c := collate.New(language.Und, collate.Loose)
	c.SortStrings(lines)
	for _, l := range header {
		fmt.Fprintln(&out, l)
	}
	for _, l := range lines {
		fmt.Fprintln(&out, l)
	}
	return out.Bytes(), nil
}

func uselessCommit(commit string) bool {
	switch commit[:7] {
	case "0d51c71":
		// I (Brad) forgot to accept a CLA for typo?
		// https://github.com/golang/net/commit/0d51c71
		return true
	case "ad051cf":
		// I (Brad) forgot to accept a CLA for typo? 2014.
		// https://github.com/golang/oauth2/commit/ad051cf
		return true
	case "661ac69", "fd68af8", "b036f29":
		// Motorola sent but never did CLA so it was reverted.
		return true
	case "a51e4cc":
		// khr <khr@khr-glaptop.roam.corp.google.com> https://github.com/golang/go/commit/a51e4cc9ce
		return true
	case "198c542":
		// adg forgot to check CLA, before the Google CLA bot handled it?
		// Load proxy variables from the environment
		// https://github.com/golang/gddo/pull/200
		return true
	case "2cfa4c7":
		// nf (adg) forgot to check CLA, before the Google CLA bot handled it?
		// https://github.com/golang/gddo/pull/156
		return true
	case "834a0af":
		// garyburd never checked CLA, prior to Google taking over the project (no CLA checks then)
		// https://github.com/golang/gddo/pull/105
		return true
	case "da10956":
		// Actually useless contribution, but no CLA on file under either Owner nor Author mail:
		// https://code-review.googlesource.com/#/c/2930/
		// https://github.com/GoogleCloudPlatform/google-cloud-go/commit/da10956
		return true
	case "0b6b69c":
		// googlebot approved it, but we can't find the record. Small change.
		// https://github.com/golang/crypto/pull/35
		// https://groups.google.com/a/google.com/d/msg/signcla-users/qpX9Z10YjQI/zjpEBmt_BgAJ
		return true
	}
	return false
}

var skipEmail = map[string]bool{
	"noreply-gerritcodereview@google.com": true,
	// Easter egg commits.
	"bwk@research.att.com": true,
	"research!bwk":         true,
	"bwk":                  true,
}

// TODO(dmitshur): Use golang.org/x/build/internal/gophers package to perform some of
// the name and email fixes, eliminating the need for current entries in nameFix and emailFix.

// nameFix is a map of name -> new name replacements to make.
// For example, "named Gopher": "Named Gopher".
var nameFix = map[string]string{
	"Emmanuel T Odeke": "Emmanuel Odeke", // to prevent a duplicate "Emmanuel T Odeke <emmanuel@orijtech.com>" entry, since "Emmanuel Odeke <emm.odeke@gmail.com> <odeke@ualberta.ca>" already exists
	"fREW Schmidt":     "Frew Schmidt",   // to use a normalized name capitalization, based on seeing it used on Medium and LinkedIn
}

// emailFix is a map of email -> new email replacements to make.
// For example, "gopher+bad@example.com": "gopher@example.com".
var emailFix = map[string]string{
	"haya14busa@gmail.com":                     "hayabusa1419@gmail.com",          // to prevent a duplicate "GitHub User @haya14busa (3797062) <haya14busa@gmail.com>" entry, since "Toshiki Shima <hayabusa1419@gmail.com>" already exists
	"36011612+steuhs@users.noreply.github.com": "steuhs@users.noreply.github.com", // to prevent a duplicate "Stephen L <36011612+steuhs@users.noreply.github.com>" entry, since "Stephen Lu <steuhs@users.noreply.github.com>" already exists
}

var validNames = map[string]bool{}

var debugPeople = []string{}
