// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	goversion "go/version"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// A Report is the report about this reproduction attempt.
// It also holds unexported state for use during the attempt.
type Report struct {
	Version    string // module@version of gorebuild command
	GoVersion  string // version of go command gorebuild was built with
	GOOS       string
	GOARCH     string
	Start      time.Time    // time reproduction started
	End        time.Time    // time reproduction ended
	Work       string       // work directory
	Full       bool         // full bootstrap back to Go 1.4
	Bootstraps []*Bootstrap // bootstrap toolchains used
	Releases   []*Release   // releases reproduced
	Log        Log

	dl []*DLRelease // information from go.dev/dl
}

// A Bootstrap describes the result of building or obtaining a bootstrap toolchain.
type Bootstrap struct {
	Version string
	Dir     string
	Err     error
	Log     Log
}

// A Release describes results for files from a single release of Go.
type Release struct {
	Version string // Go version string "go1.21.3"
	Log     Log
	dl      *DLRelease

	mu    sync.Mutex
	Files []*File // Files reproduced
}

// A File describes the result of reproducing a single file.
type File struct {
	Name   string // Name of file on go.dev/dl ("go1.21.3-linux-amd64.tar.gz")
	GOOS   string
	GOARCH string
	SHA256 string // SHA256 hex of file
	Log    Log
	dl     *DLFile

	cache bool
	mu    sync.Mutex
	data  []byte
}

// A Log contains timestamped log messages as well as an overall
// result status derived from them.
type Log struct {
	Name string

	// mu must be held when using the Log from multiple goroutines.
	// It is OK not to hold mu when there is only a single goroutine accessing
	// the data, such as during json.Marshal or json.Unmarshal.
	mu       sync.Mutex
	Messages []Message
	Status   Status
}

// A Status reports the overall result of the report, version, or file:
// FAIL, PASS, or SKIP.
type Status string

const (
	FAIL Status = "FAIL"
	PASS Status = "PASS"
	SKIP Status = "SKIP"
)

// A Message is a single log message.
type Message struct {
	Time time.Time
	Text string
}

// Printf adds a new message to the log.
// If the message begins with FAIL:, PASS:, or SKIP:,
// the status is updated accordingly.
func (l *Log) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	text := fmt.Sprintf(format, args...)
	text = strings.TrimRight(text, "\n")
	now := time.Now()
	l.Messages = append(l.Messages, Message{now, text})

	if strings.HasPrefix(format, "FAIL:") {
		l.Status = FAIL
	} else if strings.HasPrefix(format, "PASS:") && l.Status != FAIL {
		l.Status = PASS
	} else if strings.HasPrefix(format, "SKIP:") && l.Status == "" {
		l.Status = SKIP
	}

	prefix := ""
	if l.Name != "" {
		prefix = "[" + l.Name + "] "
	}
	fmt.Fprintf(os.Stderr, "%s %s%s\n", now.Format("15:04:05.000"), prefix, text)
}

// Run runs the rebuilds indicated by args and returns the resulting report.
func Run(args []string) *Report {
	r := &Report{
		Version:   "(unknown)",
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		Start:     time.Now(),
		Full:      runtime.GOOS == "linux" && runtime.GOARCH == "amd64",
	}
	defer func() {
		r.End = time.Now()
	}()
	if info, ok := debug.ReadBuildInfo(); ok {
		m := &info.Main
		if m.Replace != nil {
			m = m.Replace
		}
		r.Version = m.Path + "@" + m.Version
	}

	var err error
	defer func() {
		if err != nil {
			r.Log.Printf("FAIL: %v", err)
		}
	}()

	r.Work, err = os.MkdirTemp("", "gorebuild-")
	if err != nil {
		return r
	}

	r.dl, err = DLReleases(&r.Log)
	if err != nil {
		return r
	}

	// Allocate files for all the arguments.
	if len(args) == 0 {
		args = []string{""}
	}
	for _, arg := range args {
		sys, vers, ok := strings.Cut(arg, "@")
		versions := []string{vers}
		if !ok {
			versions = defaultVersions(r.dl)
		}
		for _, version := range versions {
			rel := r.Release(version)
			if rel == nil {
				r.Log.Printf("FAIL: unknown version %q", version)
				continue
			}
			r.File(rel, rel.Version+".src.tar.gz", "", "").cache = true
			for _, f := range rel.dl.Files {
				if f.Kind == "source" || sys == "" || sys == f.GOOS+"-"+f.GOARCH {
					r.File(rel, f.Name, f.GOOS, f.GOARCH).dl = f
					if f.GOOS != "" && f.GOARCH != "" {
						mod := "v0.0.1-" + rel.Version + "." + f.GOOS + "-" + f.GOARCH
						r.File(rel, mod+".info", f.GOOS, f.GOARCH)
						r.File(rel, mod+".mod", f.GOOS, f.GOARCH)
						r.File(rel, mod+".zip", f.GOOS, f.GOARCH)
					}
				}
			}
		}
	}

	// Do the work.
	// Fetch or build the bootstraps single-threaded.
	for _, rel := range r.Releases {
		// If BootstrapVersion fails, the parallel loop will report that.
		bver, _ := BootstrapVersion(rel.Version)
		if bver != "" {
			r.BootstrapDir(bver)
		}
	}

	// Run every file in its own goroutine.
	// Limit parallelism with channel.
	N := *pFlag
	if N < 1 {
		log.Fatalf("invalid parallelism -p=%d", *pFlag)
	}
	limit := make(chan int, N)
	for i := 0; i < N; i++ {
		limit <- 1
	}
	for _, rel := range r.Releases {
		rel := rel
		// Download source code.
		src, err := GerritTarGz(&rel.Log, "go", "refs/tags/"+rel.Version)
		if err != nil {
			rel.Log.Printf("FAIL: downloading source: %v", err)
			continue
		}

		// Reproduce all the files.
		for _, file := range rel.Files {
			file := file
			<-limit
			go func() {
				defer func() { limit <- 1 }()
				r.ReproFile(rel, file, src)
			}()
		}
	}

	// Wait for goroutines to finish.
	for i := 0; i < N; i++ {
		<-limit
	}

	// Collect results.
	// Sort the list of work for nicer presentation.
	if r.Log.Status != FAIL {
		r.Log.Status = PASS
	}
	sort.Slice(r.Releases, func(i, j int) bool { return goversion.Compare(r.Releases[i].Version, r.Releases[j].Version) > 0 })
	for _, rel := range r.Releases {
		if rel.Log.Status != FAIL {
			rel.Log.Status = PASS
		}
		sort.Slice(rel.Files, func(i, j int) bool { return rel.Files[i].Name < rel.Files[j].Name })
		for _, f := range rel.Files {
			if f.Log.Status == "" {
				f.Log.Printf("FAIL: file not checked")
			}
			if f.Log.Status == FAIL {
				rel.Log.Printf("FAIL: %s did not verify", f.Name)
			}
			if f.Log.Status == SKIP && rel.Log.Status == PASS {
				rel.Log.Status = SKIP // be clear not completely verified
			}
		}
		if rel.Log.Status == PASS {
			rel.Log.Printf("PASS")
		}
		if rel.Log.Status == FAIL {
			r.Log.Printf("FAIL: %s did not verify", rel.Version)
			r.Log.Status = FAIL
		}
		if rel.Log.Status == SKIP && r.Log.Status == PASS {
			r.Log.Status = SKIP // be clear not completely verified
		}
	}
	if r.Log.Status == PASS {
		r.Log.Printf("PASS")
	}

	return r
}

// defaultVersions returns the list of default versions to rebuild.
// (See the package documentation for details about which ones.)
func defaultVersions(releases []*DLRelease) []string {
	var versions []string
	seen := make(map[string]bool)
	for _, r := range releases {
		// Take the first unstable entry if there are no stable ones yet.
		// That will be the latest release candidate.
		// Otherwise skip; that will skip earlier release candidates
		// and unstable older releases.
		if !r.Stable {
			if len(versions) == 0 {
				versions = append(versions, r.Version)
			}
			continue
		}

		// Watch major versions go by. Take the first of each and stop after two.
		major := r.Version
		if strings.Count(major, ".") == 2 {
			major = major[:strings.LastIndex(major, ".")]
		}
		if !seen[major] {
			if major == "go1.20" {
				// not reproducible
				break
			}
			versions = append(versions, r.Version)
			seen[major] = true
			if len(seen) == 2 {
				break
			}
		}
	}
	return versions
}

func (r *Report) ReproFile(rel *Release, file *File, src []byte) (err error) {
	defer func() {
		if err != nil {
			file.Log.Printf("FAIL: %v", err)
		}
	}()

	if file.dl == nil || file.dl.Kind != "archive" {
		// Checked as a side effect of rebuilding a different file.
		return nil
	}

	file.Log.Printf("start %s", file.Name)

	goroot := filepath.Join(r.Work, fmt.Sprintf("repro-%s-%s-%s", rel.Version, file.GOOS, file.GOARCH))
	defer os.RemoveAll(goroot)

	if err := UnpackTarGz(goroot, src); err != nil {
		return err
	}
	env := []string{"GOOS=" + file.GOOS, "GOARCH=" + file.GOARCH}
	// For historical reasons, the linux-arm downloads are built
	// with GOARM=6, even though the cross-compiled default is 7.
	if strings.HasSuffix(file.Name, "-armv6l.tar.gz") || strings.HasSuffix(file.Name, ".linux-arm.zip") {
		env = append(env, "GOARM=6")
	}
	if err := r.Build(&file.Log, goroot, rel.Version, env, []string{"-distpack"}); err != nil {
		return err
	}

	distpack := filepath.Join(goroot, "pkg/distpack")
	built, err := os.ReadDir(distpack)
	if err != nil {
		return err
	}
	for _, b := range built {
		data, err := os.ReadFile(filepath.Join(distpack, b.Name()))
		if err != nil {
			return err
		}

		// Look up file from posted list.
		// For historical reasons, the linux-arm downloads are named linux-armv6l.
		// Other architectures are not renamed that way.
		// Also, the module zips are not renamed that way, even on Linux.
		name := b.Name()
		if strings.HasPrefix(name, "go") && strings.HasSuffix(name, ".linux-arm.tar.gz") {
			name = strings.TrimSuffix(name, "-arm.tar.gz") + "-armv6l.tar.gz"
		}
		bf := r.File(rel, name, file.GOOS, file.GOARCH)

		pubData, ok := r.Download(bf)
		if !ok {
			continue
		}

		match := bytes.Equal(data, pubData)
		if !match && file.GOOS == "darwin" {
			if strings.HasSuffix(bf.Name, ".tar.gz") && DiffTarGz(&bf.Log, data, pubData, StripDarwinSig) ||
				strings.HasSuffix(bf.Name, ".zip") && DiffZip(&bf.Log, data, pubData, StripDarwinSig) {
				bf.Log.Printf("verified match after stripping signatures from executables")
				match = true
			}
		}
		if !match {
			if strings.HasSuffix(bf.Name, ".tar.gz") {
				DiffTarGz(&bf.Log, data, pubData, nil)
			}
			if strings.HasSuffix(bf.Name, ".zip") {
				DiffZip(&bf.Log, data, pubData, nil)
			}
			bf.Log.Printf("FAIL: rebuilt SHA256 %s does not match public download SHA256 %s", SHA256(data), SHA256(pubData))
			continue
		}
		bf.Log.Printf("PASS: rebuilt with %q", env)
		if bf.dl != nil && bf.dl.Kind == "archive" {
			if file.GOOS == "darwin" {
				r.ReproDarwinPkg(rel, bf, pubData)
			}
			if file.GOOS == "windows" {
				r.ReproWindowsMsi(rel, bf, pubData)
			}
		}
	}
	return nil
}

func (r *Report) ReproWindowsMsi(rel *Release, file *File, zip []byte) {
	mf := r.File(rel, strings.TrimSuffix(file.Name, ".zip")+".msi", file.GOOS, file.GOARCH)
	if mf.dl == nil {
		mf.Log.Printf("FAIL: not found posted for download")
		return
	}
	msi, ok := r.Download(mf)
	if !ok {
		return
	}
	ok, skip := DiffWindowsMsi(&mf.Log, zip, msi)
	if ok {
		mf.Log.Printf("PASS: verified content against posted zip")
	} else if skip {
		mf.Log.Printf("SKIP: msiextract not found")
	}
}

func (r *Report) ReproDarwinPkg(rel *Release, file *File, tgz []byte) {
	pf := r.File(rel, strings.TrimSuffix(file.Name, ".tar.gz")+".pkg", file.GOOS, file.GOARCH)
	if pf.dl == nil {
		pf.Log.Printf("FAIL: not found posted for download")
		return
	}
	pkg, ok := r.Download(pf)
	if !ok {
		return
	}
	if DiffDarwinPkg(&pf.Log, tgz, pkg) {
		pf.Log.Printf("PASS: verified content against posted tgz")
	}
}

func (r *Report) Download(f *File) ([]byte, bool) {
	url := "https://go.dev/dl/"
	if strings.HasPrefix(f.Name, "v") {
		url += "mod/golang.org/toolchain/@v/"
	}
	if f.cache {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.data != nil {
			return f.data, true
		}
	}
	data, err := Get(&f.Log, url+f.Name)
	if err != nil {
		f.Log.Printf("FAIL: cannot download public copy")
		return nil, false
	}

	sum := SHA256(data)
	if f.dl != nil && f.dl.SHA256 != sum {
		f.Log.Printf("FAIL: go.dev/dl-listed SHA256 %s does not match public download SHA256 %s", f.dl.SHA256, sum)
		return nil, false
	}
	if f.cache {
		f.data = data
	}
	return data, true
}

func (r *Report) Release(version string) *Release {
	for _, rel := range r.Releases {
		if rel.Version == version {
			return rel
		}
	}

	var dl *DLRelease
	for _, dl = range r.dl {
		if dl.Version == version {
			rel := &Release{
				Version: version,
				dl:      dl,
			}
			rel.Log.Name = version
			r.Releases = append(r.Releases, rel)
			return rel
		}
	}
	return nil
}

func (r *Report) File(rel *Release, name, goos, goarch string) *File {
	rel.mu.Lock()
	defer rel.mu.Unlock()

	for _, f := range rel.Files {
		if f.Name == name {
			return f
		}
	}

	f := &File{
		Name:   name,
		GOOS:   goos,
		GOARCH: goarch,
	}
	f.Log.Name = name
	rel.Files = append(rel.Files, f)
	return f
}
