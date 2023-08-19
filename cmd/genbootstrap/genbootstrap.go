// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
Genbootstrap prepares GOROOT_BOOTSTRAP tarballs suitable for
use on builders. It's a wrapper around bootstrap.bash. After
bootstrap.bash produces the full output, genbootstrap trims it up,
removing unnecessary and unwanted files.

Usage:

	genbootstrap [-upload] [-rev=rev] [-v] GOOS-GOARCH[-suffix]...

The argument list can be a single glob pattern (for example '*'),
which expands to all known targets matching that pattern.

Deprecated: As of Go 1.21.0, genbootstrap is superseded
by make.bash -distpack and doesn't need to be used anymore.
The one exception are GOOS=windows targets, since their
go.dev/dl downloads are in .zip format but the builders
in x/build support pushing .tar.gz format only.
*/
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"cloud.google.com/go/storage"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/envutil"
	"golang.org/x/build/maintner/maintnerd/maintapi/version"
)

var skipBuild = flag.String("skip_build", "", "skip bootstrap.bash and reuse output in `dir` instead")
var upload = flag.Bool("upload", false, "upload outputs to gs://go-builder-data/")
var verbose = flag.Bool("v", false, "show verbose output")
var rev = flag.String("rev", "go1.17.13", "build Go at Git revision `rev`")

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: genbootstrap GOOS-GOARCH[-GO$GOARCH]... (or a glob pattern like '*')")
	flag.PrintDefaults()
}

func main() {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}

	list := flag.Args()
	if len(list) == 1 && strings.ContainsAny(list[0], "*?[]") {
		pattern := list[0]
		list = nil
		for _, name := range allPairs() {
			if ok, err := path.Match(pattern, name); ok {
				list = append(list, name)
			} else if err != nil {
				log.Fatalf("invalid match: %v", err)
			}
		}
		if len(list) == 0 {
			log.Fatalf("no matches for %q", pattern)
		}
		log.Printf("expanded %s: %v", pattern, list)
	}

	var nonWindowsTargets []string
	for _, pair := range list {
		f := strings.Split(pair, "-")
		if len(f) != 2 && len(f) != 3 {
			log.Fatalf("invalid target: %q", pair)
		}
		if goos := f[0]; goos != "windows" {
			nonWindowsTargets = append(nonWindowsTargets, pair)
		}
	}

	if x, _ := version.Go1PointX(*rev); x >= 21 && len(nonWindowsTargets) > 0 {
		log.Fatalf("genbootstrap isn't needed to build Go 1.21.0 and newer bootstrap toolchains for %v, "+
			"they're already built with make.bash -distpack and made available at go.dev/dl for all ports "+
			"(GOOS=windows targets are permitted; builders need .tar.gz format so go.dev/dl can't be used as is)", nonWindowsTargets)
	}

	dir, err := os.MkdirTemp("", "genbootstrap-*")
	if err != nil {
		log.Fatal(err)
	}
	goroot := filepath.Join(dir, "goroot")
	if err := os.MkdirAll(goroot, 0777); err != nil {
		log.Fatal(err)
	}

	log.Printf("Bootstrapping in %s at revision %s\n", dir, *rev)

	resp, err := http.Get("https://go.googlesource.com/go/+archive/" + *rev + ".tar.gz")
	if err != nil {
		log.Fatal(err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		log.Fatalf("fetching %s: %v\n%s", *rev, resp.Status, body)
	}

	cmd := exec.Command("tar", "-C", goroot, "-xzf", "-")
	cmd.Stdin = resp.Body
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("tar: %v\n%s", err, out)
	}

	// Work around GO_LDSO bug by removing implicit setting from make.bash.
	// See go.dev/issue/54196 and go.dev/issue/54197.
	// Even if those are fixed, the old toolchains we are using for bootstrap won't get the fix.
	makebash := filepath.Join(goroot, "src/make.bash")
	data, err := os.ReadFile(makebash)
	if err != nil {
		log.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("GO_LDSO"), []byte("GO_LDSO_BUG"))
	if err := os.WriteFile(makebash, data, 0666); err != nil {
		log.Fatal(err)
	}

	var storageClient *storage.Client
	if *upload {
		ctx := context.Background()
		storageClient, err = storage.NewClient(ctx)
		if err != nil {
			log.Fatalf("storage.NewClient: %v", err)
		}
	}

	gorootSrc := filepath.Join(goroot, "src")
List:
	for _, pair := range list {
		f := strings.Split(pair, "-")
		goos, goarch, gosuffix := f[0], f[1], ""
		if len(f) == 3 {
			gosuffix = "-" + f[2]
		}

		log.Printf("# %s-%s%s\n", goos, goarch, gosuffix)

		tgz := filepath.Join(dir, "gobootstrap-"+goos+"-"+goarch+gosuffix+"-"+*rev+".tar.gz")
		os.Remove(tgz)
		outDir := filepath.Join(dir, "go-"+goos+"-"+goarch+"-bootstrap")
		if *skipBuild != "" {
			outDir = *skipBuild
		} else {
			os.RemoveAll(outDir)
			cmd := exec.Command(filepath.Join(gorootSrc, "bootstrap.bash"))
			envutil.SetDir(cmd, gorootSrc)
			envutil.SetEnv(cmd,
				"GOROOT="+goroot,
				"CGO_ENABLED=0",
				"GOOS="+goos,
				"GOARCH="+goarch,
				"GOROOT_BOOTSTRAP="+os.Getenv("GOROOT_BOOTSTRAP"),
			)
			if gosuffix != "" {
				envutil.SetEnv(cmd, "GO"+strings.ToUpper(goarch)+"="+gosuffix[len("-"):])
			}
			if *verbose {
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					log.Print(err)
					continue List
				}
			} else {
				if out, err := cmd.CombinedOutput(); err != nil {
					os.Stdout.Write(out)
					log.Print(err)
					continue List
				}
			}

			// bootstrap.bash makes a bzipped tar file too,
			// but it's fat and full of stuff we don't need. Delete it.
			os.Remove(outDir + ".tbz")
		}

		if err := filepath.Walk(outDir, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(strings.TrimPrefix(path, outDir), "/")
			base := filepath.Base(path)
			var pkgrel string // relative to pkg/<goos>_<goarch>/, or empty
			if strings.HasPrefix(rel, "pkg/") && strings.Count(rel, "/") >= 2 {
				pkgrel = strings.TrimPrefix(rel, "pkg/")
				pkgrel = pkgrel[strings.Index(pkgrel, "/")+1:]
				if *verbose {
					log.Printf("rel %q => %q", rel, pkgrel)
				}
			}
			remove := func() error {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				if fi.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			switch pkgrel {
			case "cmd":
				return remove()
			}
			switch rel {
			case "api",
				"bin/gofmt",
				"doc",
				"misc/android",
				"misc/cgo",
				"misc/chrome",
				"misc/swig",
				"test":
				return remove()
			}
			if base == "testdata" {
				return remove()
			}
			if strings.HasPrefix(rel, "pkg/tool/") {
				switch base {
				case "addr2line", "api", "cgo", "cover",
					"dist", "doc", "fix", "nm",
					"objdump", "pack", "pprof",
					"trace", "vet", "yacc":
					return remove()
				}
			}
			if fi.IsDir() {
				return nil
			}
			if isEditorJunkFile(path) {
				return remove()
			}
			if !fi.Mode().IsRegular() {
				return remove()
			}
			if strings.HasSuffix(path, "_test.go") {
				return remove()
			}
			if *verbose {
				log.Printf("keeping: %s\n", rel)
			}
			return nil
		}); err != nil {
			log.Print(err)
			continue List
		}

		cmd := exec.Command("tar", "zcf", tgz, ".")
		envutil.SetDir(cmd, outDir)
		if *verbose {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				log.Print(err)
				continue List
			}
		} else {
			if out, err := cmd.CombinedOutput(); err != nil {
				os.Stdout.Write(out)
				log.Print(err)
				continue List
			}
		}

		log.Printf("Built %s", tgz)
		if *upload {
			project := "symbolic-datum-552"
			bucket := "go-builder-data"
			object := filepath.Base(tgz)
			w := storageClient.Bucket(bucket).Object(object).NewWriter(context.Background())
			// If you don't give the owners access, the web UI seems to
			// have a bug and doesn't have access to see that it's public, so
			// won't render the "Shared Publicly" link. So we do that, even
			// though it's dumb and unnecessary otherwise:
			w.ACL = append(w.ACL, storage.ACLRule{Entity: storage.ACLEntity("project-owners-" + project), Role: storage.RoleOwner})
			w.ACL = append(w.ACL, storage.ACLRule{Entity: storage.AllUsers, Role: storage.RoleReader})
			f, err := os.Open(tgz)
			if err != nil {
				log.Print(err)
				continue List
			}
			w.ContentType = "application/octet-stream"
			_, err1 := io.Copy(w, f)
			f.Close()
			err = w.Close()
			if err == nil {
				err = err1
			}
			if err != nil {
				log.Printf("Failed to upload %s: %v", tgz, err)
				continue List
			}
			log.Printf("Uploaded gs://%s/%s", bucket, object)
		}
	}
}

func isEditorJunkFile(path string) bool {
	path = filepath.Base(path)
	if strings.HasPrefix(path, "#") && strings.HasSuffix(path, "#") {
		return true
	}
	if strings.HasSuffix(path, "~") {
		return true
	}
	return false
}

// allPairs returns a list of all known builder GOOS/GOARCH pairs.
func allPairs() []string {
	have := make(map[string]bool)
	var list []string
	add := func(name string) {
		if !have[name] {
			have[name] = true
			list = append(list, name)
		}
	}

	for _, b := range dashboard.Builders {
		f := strings.Split(b.Name, "-")
		switch f[0] {
		case "android", "ios", "js", "misc":
			// skip
			continue
		}
		name := f[0] + "-" + f[1]
		if f[1] == "arm" {
			add(name + "-5")
			add(name + "-7")
			continue
		}
		add(name)
	}
	sort.Strings(list)
	return list
}
