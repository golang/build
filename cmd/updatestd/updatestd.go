// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// updatestd is a command used to update dependency versions
// in the Go standard library as part of go.dev/issue/36905.
// It's expected to be modified to meet the ongoing needs of
// that recurring maintenance work.
package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/envutil"
)

var goCmd string // the go command

func main() {
	log.SetFlags(0)

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: updatestd -goroot=<goroot> -branch=<branch>")
		flag.PrintDefaults()
	}
	goroot := flag.String("goroot", "", "path to a working copy of https://go.googlesource.com/go (required)")
	branch := flag.String("branch", "", "branch to target, such as master or internal-branch.go1.Y-vendor (required)")
	flag.Parse()
	if flag.NArg() != 0 || *goroot == "" || *branch == "" {
		flag.Usage()
		os.Exit(2)
	}

	// Determine the Go version from the GOROOT source tree.
	goVersion, err := gorootVersion(*goroot)
	if err != nil {
		log.Fatalln(err)
	}

	goCmd = filepath.Join(*goroot, "bin", "go")

	// Confirm that bundle is in PATH.
	// It's needed for a go generate step later.
	bundlePath, err := exec.LookPath("bundle")
	if err != nil {
		log.Fatalln("can't find bundle in PATH; did you run 'go install golang.org/x/tools/cmd/bundle@latest' and add it to PATH?")
	}
	if bi, err := buildinfo.ReadFile(bundlePath); err != nil || bi.Path != "golang.org/x/tools/cmd/bundle" {
		// Not the bundle command we want.
		log.Fatalln("unexpected bundle command in PATH; did you run 'go install golang.org/x/tools/cmd/bundle@latest' and add it to PATH?")
	}

	// Fetch latest hashes of Go projects from Gerrit,
	// using the specified branch name.
	//
	// We get a fairly consistent snapshot of all golang.org/x module versions
	// at a given point in time. This ensures selection of latest available
	// pseudo-versions is done without being subject to module mirror caching,
	// and that selected pseudo-versions can be re-used across multiple modules.
	//
	// TODO: Consider a future enhancement of fetching build status for all
	// commits that are selected and reporting if any of them have a failure.
	//
	cl := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
	projs, err := cl.ListProjects(context.Background())
	if err != nil {
		log.Fatalln("failed to get a list of Gerrit projects:", err)
	}
	hashes := map[string]string{}
	for _, p := range projs {
		b, err := cl.GetBranch(context.Background(), p.Name, *branch)
		if errors.Is(err, gerrit.ErrResourceNotExist) {
			continue
		} else if err != nil {
			log.Fatalf("failed to get the %q branch of Gerrit project %q: %v\n", *branch, p.Name, err)
		}
		hashes[p.Name] = b.Revision
	}

	w := Work{
		Branch:        *branch,
		GoVersion:     fmt.Sprintf("1.%d", goVersion),
		ProjectHashes: hashes,
	}

	// Print environment information.
	r := runner{filepath.Join(*goroot, "src")}
	r.run(goCmd, "version")
	r.run(goCmd, "env", "GOROOT")
	r.run(goCmd, "version", "-m", bundlePath)
	log.Println()

	// Walk the standard library source tree (GOROOT/src),
	// skipping directories that the Go command ignores (see go help packages)
	// and update modules that are found.
	err = filepath.Walk(filepath.Join(*goroot, "src"), func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() && (strings.HasPrefix(fi.Name(), ".") || strings.HasPrefix(fi.Name(), "_") || fi.Name() == "testdata" || fi.Name() == "vendor") {
			return filepath.SkipDir
		}
		goModFile := fi.Name() == "go.mod" && !fi.IsDir()
		if goModFile {
			moduleDir := filepath.Dir(path)
			err := w.UpdateModule(moduleDir)
			if err != nil {
				return fmt.Errorf("failed to update module in %s: %v", moduleDir, err)
			}
			return filepath.SkipDir // Skip the remaining files in this directory.
		}
		return nil
	})
	if err != nil {
		log.Fatalln(err)
	}

	// Re-bundle packages in the standard library.
	//
	// TODO: Maybe do GOBIN=$(mktemp -d) go install golang.org/x/tools/cmd/bundle@version or so,
	// and add it to PATH to eliminate variance in bundle tool version. Can be considered later.
	//
	log.Println("updating bundles in", r.dir)
	r.run(goCmd, "generate", "-run=bundle", "std", "cmd")

	// Update the golang.org/x/pkgsite/cmd/internal/doc version used
	// as the underlying implementation of 'go doc -http'.
	if goVersion == 25 { // TODO: Delete this case after Go 1.25 stops being supported.
		log.Println(`skipping updating x/pkgsite/cmd/internal/doc version:

on Go 1.25 release branch, this maintenance is done manually by editing the version constant in doPkgsite:

	$EDITOR src/cmd/internal/doc/main.go
	(see https://cs.opensource.google/go/go/+/release-branch.go1.25:src/cmd/internal/doc/main.go;l=246;drc=b4309ece66ca989a38ed65404850a49ae8f92742)

automated updatestd maintenance support currently applies to Go 1.27+`)
	} else if goVersion == 26 { // TODO: Delete this case after Go 1.26 stops being supported.
		log.Println(`skipping updating x/pkgsite/cmd/internal/doc version:

on Go 1.26 release branch, this maintenance is done manually by editing the version constant in doPkgsite:

	$EDITOR src/cmd/go/internal/doc/pkgsite.go
	(see https://cs.opensource.google/go/go/+/release-branch.go1.26:src/cmd/go/internal/doc/pkgsite.go;l=74;drc=866e461b9689d03dbbf2df19b86cace21270865b)

automated updatestd maintenance support currently applies to Go 1.27+`)
	} else if pkgsiteHash := hashes["pkgsite"]; pkgsiteHash != "" {
		log.Println("updating x/pkgsite/cmd/internal/doc version to", pkgsiteHash)
		out := r.runOut(goCmd, "list", "-mod=readonly", "-m", "-json", "golang.org/x/pkgsite/cmd/internal/doc@"+pkgsiteHash)
		var mod struct{ Version string }
		if err := json.Unmarshal(out, &mod); err != nil {
			log.Fatalf("error parsing go list -m -json output: %v", err)
		}
		err := editPkgsiteVersion(*goroot, mod.Version)
		if err != nil {
			log.Fatalln(err)
		}
	} else {
		log.Println("skipping updating x/pkgsite/cmd/internal/doc version because x/pkgsite doesn't have a branch named", *branch)
	}
}

type Work struct {
	Branch        string            // Target branch name.
	GoVersion     string            // Major Go version, like "1.x".
	ProjectHashes map[string]string // Gerrit project name → commit hash.
}

// UpdateModule updates the standard library module found in dir:
//
//  1. Set the expected Go version in go.mod file to w.GoVersion.
//  2. For modules in the build list with "golang.org/x/" prefix,
//     update to pseudo-version corresponding to w.ProjectHashes.
//  3. Run go mod tidy.
//  4. Run go mod vendor.
//
// The logic in this method needs to serve the dependency update
// policy for the purpose of golang.org/issue/36905, although it
// does not directly define said policy.
func (w Work) UpdateModule(dir string) error {
	// Determine the build list.
	main, deps := buildList(dir)

	// Determine module versions to get.
	goGet := []string{goCmd, "get"}
	for _, m := range deps {
		if !strings.HasPrefix(m.Path, "golang.org/x/") {
			log.Printf("skipping %s (out of scope, it's not a golang.org/x dependency)\n", m.Path)
			continue
		}
		gerritProj := m.Path[len("golang.org/x/"):]
		hash, ok := w.ProjectHashes[gerritProj]
		if !ok {
			log.Printf("skipping %s because branch %s doesn't exist\n", m.Path, w.Branch)
			continue
		}
		goGet = append(goGet, m.Path+"@"+hash)
	}

	// Run all the commands.
	log.Println("updating module", main.Path, "in", dir)
	r := runner{dir}
	gowork := strings.TrimSpace(string(r.runOut(goCmd, "env", "GOWORK")))
	if gowork != "" && gowork != "off" {
		log.Printf("warning: GOWORK=%q, things may go wrong?", gowork)
	}
	r.run(goCmd, "mod", "edit", "-go="+w.GoVersion)
	r.run(goGet...)
	r.run(goCmd, "mod", "tidy")
	r.run(goCmd, "mod", "vendor")
	log.Println()
	return nil
}

// buildList determines the build list in the directory dir
// by invoking the go command. It uses -mod=readonly mode.
// It returns the main module and other modules separately
// for convenience to the UpdateModule caller.
//
// See https://go.dev/ref/mod#go-list-m and https://go.dev/ref/mod#glos-build-list.
func buildList(dir string) (main module, deps []module) {
	out := runner{dir}.runOut(goCmd, "list", "-mod=readonly", "-m", "-json", "all")
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var m module
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		} else if err != nil {
			log.Fatalf("internal error: unexpected problem decoding JSON returned by go list -json: %v", err)
		}
		if m.Main {
			main = m
			continue
		}
		deps = append(deps, m)
	}
	return main, deps
}

type module struct {
	Path string // Module path.
	Main bool   // Is this the main module?
}

// gorootVersion reads the GOROOT/src/internal/goversion/goversion.go
// file and reports the Version declaration value found therein.
func gorootVersion(goroot string) (int, error) {
	// Parse the goversion.go file, extract the declaration from the AST.
	//
	// This is a pragmatic approach that relies on the trajectory of the
	// internal/goversion package being predictable and unlikely to change.
	// If that stops being true, this small helper is easy to re-write.
	//
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(goroot, "src", "internal", "goversion", "goversion.go"), nil, 0)
	if os.IsNotExist(err) {
		return 0, fmt.Errorf("did not find goversion.go file (%v); wrong goroot or internal/goversion package changed", err)
	} else if err != nil {
		return 0, err
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			v, ok := s.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].String() != "Version" || len(v.Values) != 1 {
				continue
			}
			l, ok := v.Values[0].(*ast.BasicLit)
			if !ok || l.Kind != token.INT {
				continue
			}
			// Found it.
			return strconv.Atoi(l.Value)
		}
	}
	return 0, fmt.Errorf("did not find Version declaration in %s; wrong goroot or internal/goversion package changed", fset.File(f.Pos()).Name())
}

// editPkgsiteVersion reads the GOROOT/src/cmd/go/internal/doc/pkgsite.go
// file and edits the pkgsiteCmdInternalDocVersion constant found therein.
func editPkgsiteVersion(goroot, version string) error {
	// Parse the pkgsite.go file, extract the declaration from the AST,
	// edit it in place and overwrite the file.
	//
	// This is a pragmatic approach that relies on the trajectory of the
	// cmd/go/internal/doc package being predictable and in our control.
	// This small helper is easy to replace with something else if it
	// becomes desirable to start using another approach for this task.
	//
	filename := filepath.Join(goroot, "src", "cmd", "go", "internal", "doc", "pkgsite.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if os.IsNotExist(err) {
		return fmt.Errorf("did not find pkgsite.go file (%v); wrong goroot or cmd/go/internal/doc package changed", err)
	} else if err != nil {
		return err
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			v, ok := s.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].String() != "pkgsiteCmdInternalDocVersion" || len(v.Values) != 1 {
				continue
			}
			l, ok := v.Values[0].(*ast.BasicLit)
			if !ok || l.Kind != token.STRING {
				continue
			}

			// Found it.
			// Edit its value and overwrite the existing file.
			l.Value = fmt.Sprintf("%q", version)
			var buf bytes.Buffer
			err := format.Node(&buf, fset, f)
			if err != nil {
				return err
			}
			return os.WriteFile(filename, buf.Bytes(), 0) // file already exists, so perm is unused
		}
	}
	return fmt.Errorf("did not find pkgsiteCmdInternalDocVersion declaration in %s; wrong goroot or cmd/go/internal/doc package changed", fset.File(f.Pos()).Name())
}

type runner struct{ dir string }

// run runs the command and requires that it succeeds.
// It logs the command's combined output.
func (r runner) run(args ...string) {
	log.Printf("> %s\n", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	envutil.SetDir(cmd, r.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("command failed: %s\n%s", err, out)
	}
	if len(out) != 0 {
		log.Print(string(out))
	}
}

// runOut runs the command, requires that it succeeds,
// and returns the command's standard output.
func (r runner) runOut(args ...string) []byte {
	cmd := exec.Command(args[0], args[1:]...)
	envutil.SetDir(cmd, r.dir)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("> %s\n", strings.Join(args, " "))
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			out = append(out, ee.Stderr...)
		}
		log.Fatalf("command failed: %s\n%s", err, out)
	}
	return out
}
