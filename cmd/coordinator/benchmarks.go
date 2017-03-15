// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"golang.org/x/build/buildlet"
)

// benchRuns is the number of times to run each benchmark binary
const benchRuns = 5

type benchmarkItem struct {
	binary   string   // name of binary relative to goroot
	args     []string // args to run binary with
	preamble string   // string to print before benchmark results (e.g. "pkg: test/bench/go1\n")
	output   []string // old, new benchmark output

	build func(bc *buildlet.Client, goroot string, w io.Writer) (remoteErr, err error) // how to build benchmark binary
}

func (b *benchmarkItem) name() string {
	return b.binary + " " + strings.Join(b.args, " ")
}

// buildGo1 builds the Go 1 benchmarks.
func (st *buildStatus) buildGo1(bc *buildlet.Client, goroot string, w io.Writer) (remoteErr, err error) {
	workDir, err := bc.WorkDir()
	if err != nil {
		return nil, err
	}
	var found bool
	if err := bc.ListDir(path.Join(goroot, "test/bench/go1"), buildlet.ListDirOpts{}, func(e buildlet.DirEntry) {
		switch e.Name() {
		case "go1.test", "go1.test.exe":
			found = true
		}
	}); err != nil {
		return nil, err
	}
	if found {
		return nil, nil
	}
	return bc.Exec(path.Join(goroot, "bin", "go"), buildlet.ExecOpts{
		Output:   w,
		ExtraEnv: []string{"GOROOT=" + st.conf.FilePathJoin(workDir, goroot)},
		Args:     []string{"test", "-c"},
		Dir:      path.Join(goroot, "test/bench/go1"),
	})
}

// buildXBenchmark builds a benchmark from x/benchmarks.
func (st *buildStatus) buildXBenchmark(bc *buildlet.Client, goroot string, w io.Writer, rev, pkg, name string) (remoteErr, err error) {
	workDir, err := bc.WorkDir()
	if err != nil {
		return nil, err
	}
	if err := bc.ListDir("gopath/src/golang.org/x/benchmarks", buildlet.ListDirOpts{}, func(buildlet.DirEntry) {}); err != nil {
		if err := st.fetchSubrepo(bc, "benchmarks", rev); err != nil {
			return nil, err
		}
	}
	return bc.Exec(path.Join(goroot, "bin/go"), buildlet.ExecOpts{
		Output: w,
		ExtraEnv: []string{
			"GOROOT=" + st.conf.FilePathJoin(workDir, goroot),
			"GOPATH=" + st.conf.FilePathJoin(workDir, "gopath"),
		},
		Args: []string{"build", "-o", st.conf.FilePathJoin(workDir, goroot, name), pkg},
	})
}

func (st *buildStatus) enumerateBenchmarks(bc *buildlet.Client) ([]*benchmarkItem, error) {
	workDir, err := bc.WorkDir()
	if err != nil {
		err = fmt.Errorf("buildBench, WorkDir: %v", err)
		return nil, err
	}
	// Fetch x/benchmarks
	rev := getRepoHead("benchmarks")
	if rev == "" {
		rev = "master" // should happen rarely; ok if it does.
	}

	if err := st.fetchSubrepo(bc, "benchmarks", rev); err != nil {
		return nil, err
	}

	var out []*benchmarkItem

	// These regexes shard the go1 tests so each shard takes about 20s, ensuring no test runs for
	for _, re := range []string{`^Benchmark[BF]`, `^Benchmark[HR]`, `^Benchmark[^BFHR]`} {
		out = append(out, &benchmarkItem{
			binary:   "test/bench/go1/go1.test",
			args:     []string{"-test.bench", re, "-test.benchmem"},
			preamble: "pkg: test/bench/go1\n",
			build:    st.buildGo1,
		})
	}

	// Enumerate x/benchmarks
	var buf bytes.Buffer
	remoteErr, err := bc.Exec("go/bin/go", buildlet.ExecOpts{
		Output: &buf,
		ExtraEnv: []string{
			"GOROOT=" + st.conf.FilePathJoin(workDir, "go"),
			"GOPATH=" + st.conf.FilePathJoin(workDir, "gopath"),
		},
		Args: []string{"list", "-f", `{{if eq .Name "main"}}{{.ImportPath}}{{end}}`, "golang.org/x/benchmarks/..."},
	})
	if remoteErr != nil {
		return nil, remoteErr
	}
	if err != nil {
		return nil, err
	}
	for _, pkg := range strings.Fields(buf.String()) {
		pkg := pkg
		name := "bench-" + path.Base(pkg) + ".exe"
		out = append(out, &benchmarkItem{
			binary: name, args: nil, build: func(bc *buildlet.Client, goroot string, w io.Writer) (error, error) {
				return st.buildXBenchmark(bc, goroot, w, rev, pkg, name)
			}})
	}
	// TODO(quentin): Enumerate package benchmarks that were affected by the CL
	return out, nil
}

// runOneBenchBinary runs a binary on the buildlet and writes its output to w with a trailing newline.
func (st *buildStatus) runOneBenchBinary(bc *buildlet.Client, w io.Writer, goroot string, path string, args []string) (remoteErr, err error) {
	defer w.Write([]byte{'\n'})
	workDir, err := bc.WorkDir()
	if err != nil {
		return nil, fmt.Errorf("runOneBenchBinary, WorkDir: %v", err)
	}
	// Some benchmarks need GOROOT so they can invoke cmd/go.
	return bc.Exec(path, buildlet.ExecOpts{
		Output: w,
		Args:   args,
		Path:   []string{"$WORKDIR/" + goroot + "/bin", "$PATH"},
		ExtraEnv: []string{
			"GOROOT=" + st.conf.FilePathJoin(workDir, goroot),
		},
	})
}

func (b *benchmarkItem) buildParent(st *buildStatus, bc *buildlet.Client, w io.Writer) error {
	pbr := st.builderRev // copy
	rev := st.trySet.ci.Revisions[st.trySet.ci.CurrentRevision]
	if rev.Commit == nil {
		return fmt.Errorf("commit information missing for revision %q", st.trySet.ci.CurrentRevision)
	}
	if len(rev.Commit.Parents) == 0 {
		// TODO(quentin): Log?
		return errors.New("commit has no parent")
	}
	pbr.rev = rev.Commit.Parents[0].CommitID
	if pbr.snapshotExists() {
		return bc.PutTarFromURL(pbr.snapshotURL(), "go-parent")
	}
	if err := bc.PutTar(versionTgz(pbr.rev), "go-parent"); err != nil {
		return err
	}
	srcTar, err := getSourceTgz(st, "go", pbr.rev)
	if err != nil {
		return err
	}
	if err := bc.PutTar(srcTar, "go-parent"); err != nil {
		return err
	}
	remoteErr, err := st.runMake(bc, "go-parent", w)
	if err != nil {
		return err
	}
	return remoteErr
}

// run runs all the iterations of this benchmark on bc.
// Build output is sent to w. Benchmark output is stored in b.output.
// TODO(quentin): Take a list of commits so this can be used for non-try runs.
func (b *benchmarkItem) run(st *buildStatus, bc *buildlet.Client, w io.Writer) (remoteErr, err error) {
	// Ensure we have a built parent repo.
	if err := bc.ListDir("go-parent", buildlet.ListDirOpts{}, func(buildlet.DirEntry) {}); err != nil {
		sp := st.createSpan("bench_build_parent", bc.Name())
		err := b.buildParent(st, bc, w)
		sp.done(err)
		if err != nil {
			return nil, err
		}
	}
	// Build benchmark.
	for _, goroot := range []string{"go", "go-parent"} {
		sp := st.createSpan("bench_build", fmt.Sprintf("%s/%s: %s", goroot, b.binary, bc.Name()))
		remoteErr, err = b.build(bc, goroot, w)
		sp.done(err)
		if remoteErr != nil || err != nil {
			return remoteErr, err
		}
	}

	type commit struct {
		path string
		out  bytes.Buffer
	}
	commits := []*commit{
		{path: "go-parent"},
		{path: "go"},
	}

	for _, c := range commits {
		c.out.WriteString(b.preamble)
	}

	// Run bench binaries and capture the results
	for i := 0; i < benchRuns; i++ {
		for _, c := range commits {
			fmt.Fprintf(&c.out, "iteration: %d\nstart-time: %s\n", i, time.Now().UTC().Format(time.RFC3339))
			p := path.Join(c.path, b.binary)
			sp := st.createSpan("run_one_bench", p)
			remoteErr, err = st.runOneBenchBinary(bc, &c.out, c.path, p, b.args)
			sp.done(err)
			if err != nil || remoteErr != nil {
				c.out.WriteTo(w)
				return
			}
		}
	}
	b.output = []string{
		commits[0].out.String(),
		commits[1].out.String(),
	}
	return nil, nil
}
