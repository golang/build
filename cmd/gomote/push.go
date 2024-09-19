// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/sync/errgroup"
)

func push(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "print what would be done only")
	fs.Usage = func() {
		log := usageLogger
		log.Print("push usage: gomote push <instance>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)

	goroot, err := getGOROOT()
	if err != nil {
		return err
	}

	var pushSet []string
	if fs.NArg() == 1 {
		pushSet = append(pushSet, fs.Arg(0))
	} else if activeGroup != nil {
		for _, inst := range activeGroup.Instances {
			pushSet = append(pushSet, inst)
		}
	} else {
		fs.Usage()
	}

	detailedProgress := len(pushSet) == 1
	eg, ctx := errgroup.WithContext(context.Background())
	for _, inst := range pushSet {
		inst := inst
		eg.Go(func() error {
			log.Printf("Pushing GOROOT %q to %q...\n", goroot, inst)
			return doPush(ctx, inst, goroot, dryRun, detailedProgress)
		})
	}
	return eg.Wait()
}

func doPush(ctx context.Context, name, goroot string, dryRun, detailedProgress bool) error {
	logf := func(s string, a ...interface{}) {
		if detailedProgress {
			log.Printf(s, a...)
		}
	}
	remote := map[string]buildlet.DirEntry{} // keys like "src/make.bash"

	client := gomoteServerClient(ctx)
	resp, err := client.ListDirectory(ctx, &protos.ListDirectoryRequest{
		GomoteId:  name,
		Directory: ".",
		Recursive: true,
		SkipFiles: []string{
			// Ignore binary output directories:
			"go/pkg", "go/bin",
			// We don't care about the digest of
			// particular source files for Go 1.4.  And
			// exclude /pkg. This leaves go1.4/bin, which
			// is enough to know whether we have Go 1.4 or
			// not.
			"go1.4/src", "go1.4/pkg",
			// Ignore the cache and tmp directories, these slowly grow, and will
			// eventually cause the listing to exceed the maximum gRPC message
			// size.
			"gocache", "goplscache", "tmp",
		},
		Digest: true,
	})
	if err != nil {
		return fmt.Errorf("error listing buildlet's existing files: %w", err)
	}
	for _, entry := range resp.GetEntries() {
		de := buildlet.DirEntry{Line: entry}
		en := de.Name()
		if strings.HasPrefix(en, "go/") && en != "go/" {
			remote[en[len("go/"):]] = de
		}
	}
	// TODO(66635) remove once gomotes can no longer be created via the coordinator.
	if luciDisabled() {
		logf("installing go-bootstrap version in the working directory")
		if dryRun {
			logf("(Dry-run) Would have pushed go-bootstrap")
		} else {
			_, err := client.AddBootstrap(ctx, &protos.AddBootstrapRequest{
				GomoteId: name,
			})
			if err != nil {
				return fmt.Errorf("unable to add bootstrap version of Go to instance: %w", err)
			}
		}
	}

	type fileInfo struct {
		fi   os.FileInfo
		sha1 string // if regular file
	}
	local := map[string]fileInfo{} // keys like "src/make.bash"

	// Ensure that the goroot passed to filepath.Walk ends in a trailing slash,
	// so that if GOROOT is a symlink we walk the underlying directory.
	walkRoot := goroot
	if walkRoot != "" && !os.IsPathSeparator(walkRoot[len(walkRoot)-1]) {
		walkRoot += string(filepath.Separator)
	}
	absToRel := make(map[string]string)
	if err := filepath.Walk(walkRoot, func(path string, fi os.FileInfo, err error) error {
		if isEditorBackup(path) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(goroot, path)
		if err != nil {
			return fmt.Errorf("error calculating relative path from %q to %q", goroot, path)
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil // .git is a file in `git worktree` checkouts.
		}
		if fi.IsDir() {
			switch rel {
			case "pkg", "bin":
				return filepath.SkipDir
			}
		}
		inf := fileInfo{fi: fi}
		absToRel[path] = rel
		if fi.Mode().IsRegular() {
			inf.sha1, err = fileSHA1(path)
			if err != nil {
				return err
			}
		}
		local[rel] = inf
		return nil
	}); err != nil {
		return fmt.Errorf("error enumerating local GOROOT files: %w", err)
	}

	ignored := make(map[string]bool)
	for _, path := range gitIgnored(goroot, absToRel) {
		ignored[absToRel[path]] = true
		delete(local, absToRel[path])
	}

	var toDel []string
	for rel := range remote {
		if rel == "VERSION" {
			// Don't delete this. It's harmless, and
			// necessary. Clients can overwrite it if they
			// want. But if there's no VERSION file there,
			// make.bash/bat assumes there's a git repo in
			// place, but there's not only not a git repo
			// there with gomote, but there's no git tool
			// available either.
			continue
		}
		// Also don't delete the auto-generated files from cmd/dist.
		// Otherwise gomote users can't gomote push + gomote run make.bash
		// and then iteratively:
		// -- hack locally
		// -- gomote push
		// -- gomote run go test -v ...
		// Because the go test would fail remotely without
		// these files if they were deleted by gomote push.
		if isGoToolDistGenerated(rel) {
			continue
		}
		if ignored[rel] {
			// Don't delete remote gitignored files; this breaks built toolchains.
			continue
		}
		rel = strings.TrimRight(rel, "/")
		if rel == "" {
			continue
		}
		if _, ok := local[rel]; !ok {
			toDel = append(toDel, rel)
		}
	}
	if len(toDel) > 0 {
		withGo := make([]string, len(toDel)) // with the "go/" prefix
		for i, v := range toDel {
			withGo[i] = "go/" + v
		}
		sort.Strings(withGo)
		if dryRun {
			logf("(Dry-run) Would have deleted remote files: %q", withGo)
		} else {
			logf("Deleting remote files: %q", withGo)
			if _, err := client.RemoveFiles(ctx, &protos.RemoveFilesRequest{
				GomoteId: name,
				Paths:    withGo,
			}); err != nil {
				return fmt.Errorf("failed to delete remote unwanted files: %w", err)
			}
		}
	}
	var toSend []string
	notHave := 0
	const maxNotHavePrint = 5
	for rel, inf := range local {
		if isGoToolDistGenerated(rel) || rel == "VERSION.cache" {
			continue
		}
		if !inf.fi.Mode().IsRegular() {
			if !inf.fi.IsDir() {
				logf("Ignoring local non-regular, non-directory file %s: %v", rel, inf.fi.Mode())
			}
			continue
		}
		rem, ok := remote[rel]
		if !ok {
			if notHave++; notHave <= maxNotHavePrint {
				logf("Remote doesn't have %q", rel)
			}
			toSend = append(toSend, rel)
			continue
		}
		if rem.Digest() != inf.sha1 {
			logf("Remote's %s digest is %q; want %q", rel, rem.Digest(), inf.sha1)
			toSend = append(toSend, rel)
		}
	}
	if notHave > maxNotHavePrint {
		logf("Remote doesn't have %d files (only showed %d).", notHave, maxNotHavePrint)
	}
	_, localHasVersion := local["VERSION"]
	if _, remoteHasVersion := remote["VERSION"]; !remoteHasVersion && !localHasVersion {
		logf("Remote lacks a VERSION file; sending a fake one")
		toSend = append(toSend, "VERSION")
	}
	if len(toSend) > 0 {
		sort.Strings(toSend)
		tgz, err := generateDeltaTgz(goroot, toSend)
		if err != nil {
			return err
		}
		logf("Uploading %d new/changed files; %d byte .tar.gz", len(toSend), tgz.Len())
		if dryRun {
			logf("(Dry-run mode; not doing anything.")
			return nil
		}
		resp, err := client.UploadFile(ctx, &protos.UploadFileRequest{})
		if err != nil {
			return fmt.Errorf("unable to request credentials for a file upload: %w", err)
		}
		if err := uploadToGCS(ctx, resp.GetFields(), tgz, resp.GetObjectName(), resp.GetUrl()); err != nil {
			return fmt.Errorf("unable to upload file to GCS: %w", err)
		}
		if _, err := client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
			GomoteId:  name,
			Url:       fmt.Sprintf("%s%s", resp.GetUrl(), resp.GetObjectName()),
			Directory: "go",
		}); err != nil {
			return fmt.Errorf("failed writing tarball to buildlet: %w", err)
		}
	}
	return nil
}

func isGoToolDistGenerated(path string) bool {
	switch path {
	case "src/cmd/cgo/zdefaultcc.go",
		"src/cmd/go/internal/cfg/zdefaultcc.go",
		"src/cmd/go/internal/cfg/zosarch.go",
		"src/cmd/internal/objabi/zbootstrap.go",
		"src/go/build/zcgo.go",
		"src/internal/buildcfg/zbootstrap.go",
		"src/internal/runtime/sys/zversion.go",
		"src/runtime/internal/sys/zversion.go", // relevant only prior to CL 600436
		"src/time/tzdata/zzipdata.go":
		return true
	}
	return false
}

func isEditorBackup(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") && strings.HasSuffix(base, ".swp") {
		// vi
		return true
	}
	if strings.HasSuffix(path, "~") || strings.HasSuffix(path, "#") ||
		strings.HasPrefix(base, "#") || strings.HasPrefix(base, ".#") {
		// emacs
		return true
	}
	return false
}

// file is forward-slash separated
func generateDeltaTgz(goroot string, files []string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, file := range files {
		// Special.
		if file == "VERSION" && !localFileExists(filepath.Join(goroot, file)) {
			// TODO(bradfitz): a dummy VERSION file's contents to make things
			// happy. Notably it starts with "devel ". Do we care about it
			// being accurate beyond that?
			version := "devel gomote.XXXXX"
			if err := tw.WriteHeader(&tar.Header{
				Name: "VERSION",
				Mode: 0644,
				Size: int64(len(version)),
			}); err != nil {
				return nil, err
			}
			if _, err := io.WriteString(tw, version); err != nil {
				return nil, err
			}
			continue
		}
		f, err := os.Open(filepath.Join(goroot, file))
		if err != nil {
			return nil, err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		header, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			f.Close()
			return nil, err
		}
		header.Name = file // forward slash
		if err := tw.WriteHeader(header); err != nil {
			f.Close()
			return nil, err
		}
		if _, err := io.CopyN(tw, f, header.Size); err != nil {
			f.Close()
			return nil, fmt.Errorf("error copying contents of %s: %w", file, err)
		}
		f.Close()
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	return &buf, nil
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s1 := sha1.New()
	if _, err := io.Copy(s1, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", s1.Sum(nil)), nil
}

func getGOROOT() (string, error) {
	goroot := os.Getenv("GOROOT")
	if goroot == "" {
		slurp, err := exec.Command("go", "env", "GOROOT").Output()
		if err != nil {
			return "", fmt.Errorf("failed to get GOROOT from go env: %w", err)
		}
		goroot = strings.TrimSpace(string(slurp))
		if goroot == "" {
			return "", errors.New("Failed to get $GOROOT from environment or go env")
		}
	}
	goroot = filepath.Clean(goroot)
	return goroot, nil
}

func localFileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// gitIgnored checks whether any of the paths listed as keys in absToRel
// are git ignored in goroot. It returns the list of ignored paths.
func gitIgnored(goroot string, absToRel map[string]string) []string {
	var stdin, stdout, stderr bytes.Buffer
	for abs := range absToRel {
		stdin.WriteString(abs)
		stdin.WriteString("\x00")
	}

	// Invoke 'git check-ignore' and use it to query whether paths have been gitignored.
	// If anything goes wrong at any point, fall back to assuming that nothing is gitignored.
	cmd := exec.Command("git", "-C", goroot, "check-ignore", "--stdin", "-z")
	cmd.Stdin = &stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok && e.ExitCode() == 1 {
			// exit 1 means no files are ignored
			err = nil
		}
		if err != nil {
			log.Printf("exec git check-ignore: %v\n%s", err, stderr.Bytes())
		}
	}

	var ignored []string
	br := bufio.NewReader(&stdout)
	for {
		// Response is of the form "<source> <NUL>"
		f, err := br.ReadBytes('\x00')
		if err != nil {
			if err != io.EOF {
				log.Printf("git check-ignore: unexpected error reading output: %s", err)
			}
			break
		}
		ignored = append(ignored, string(f[:len(f)-len("\x00")]))
	}
	return ignored
}
