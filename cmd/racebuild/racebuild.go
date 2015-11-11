// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// racebuild builds the race runtime (syso files) on all supported OSes using gomote.
// Usage:
//	$ racebuild -rev <llvm_revision> -goroot <path_to_go_repo>
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var (
	flagGoroot = flag.String("goroot", "", "path to Go repository to update (required)")
	flagRev    = flag.Int("rev", 0, "llvm compiler-rt revision (required)")
)

// TODO: use buildlet package instead of calling out to gomote.
var platforms = []*Platform{
	&Platform{
		OS:   "freebsd",
		Arch: "amd64",
		Type: "freebsd-amd64-race",
		Commands: []string{
			"gomote put14 $INST",
			"gomote run $INST /usr/sbin/pkg install -y clang36-3.6.2 subversion",
			"gomote run $INST /usr/local/bin/git clone https://go.googlesource.com/go",
			"gomote run $INST /usr/local/bin/svn checkout -r $REV http://llvm.org/svn/llvm-project/compiler-rt/trunk/lib tsan",
			"gomote run -e CC=/usr/local/llvm36/bin/clang $INST tsan/tsan/go/buildgo.sh",
			"gomote run $INST /bin/cp tsan/tsan/go/race_$GOOS_$GOARCH.syso go/src/runtime/race",
			"gomote run $INST go/src/race.bash",
			"gomote gettar -dir=go/src/runtime/race $INST",
		},
	},
}

func main() {
	flag.Parse()
	if *flagRev <= 0 || *flagGoroot == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Update revision in the README file.
	// Do this early to check goroot correctness.
	readmeFile := filepath.Join(*flagGoroot, "src", "runtime", "race", "README")
	readme, err := ioutil.ReadFile(readmeFile)
	if err != nil {
		log.Fatalf("bad -goroot? %v", err)
	}
	readmeRev := regexp.MustCompile("Current runtime is built on rev ([0-9]+)\\.").FindSubmatchIndex(readme)
	if readmeRev == nil {
		log.Fatalf("failed to find current revision in src/runtime/race/README")
	}
	readme = bytes.Replace(readme, readme[readmeRev[2]:readmeRev[3]], []byte(strconv.Itoa(*flagRev)), -1)
	if err := ioutil.WriteFile(readmeFile, readme, 0640); err != nil {
		log.Fatalf("failed to write README file: %v", err)
	}

	// Start build on all platforms in parallel.
	var wg sync.WaitGroup
	wg.Add(len(platforms))
	for _, p := range platforms {
		p := p
		go func() {
			defer wg.Done()
			p.Err = p.Build()
			if p.Err != nil {
				p.Err = fmt.Errorf("failed: %v (log is saved to %v)", p.Err, p.Log.Name())
				log.Printf("%v: %v", p.Name, p.Err)
			}
		}()
	}
	wg.Wait()

	// Duplicate results, they can get lost in the log.
	ok := true
	log.Printf("---")
	for _, p := range platforms {
		if p.Err == nil {
			log.Printf("%v: ok", p.Name)
			continue
		}
		ok = false
		log.Printf("%v: %v", p.Name, p.Err)
	}
	if !ok {
		os.Exit(1)
	}
}

type Platform struct {
	OS       string
	Arch     string
	Name     string // something for logging
	Type     string // gomote instance type
	Inst     string // actual gomote instance name
	Err      error
	Log      *os.File
	Commands []string
}

func (p *Platform) Build() error {
	p.Name = fmt.Sprintf("%v-%v", p.OS, p.Arch)

	// Open log file.
	var err error
	p.Log, err = ioutil.TempFile("", p.Name)
	if err != nil {
		return fmt.Errorf("failed to create log file: %v", err)
	}
	defer p.Log.Close()

	// Create gomote instance (or reuse an existing instance for debugging).
	if p.Inst == "" {
		inst, err := p.Exec(fmt.Sprintf("gomote create %v", p.Type), true)
		if err != nil {
			return fmt.Errorf("gomote create failed: %v\n%s", err, inst)
		}
		p.Inst = strings.Trim(string(inst), " \t\n")
	}
	log.Printf("%s: using instance %v", p.Name, p.Inst)

	// Execute the sequence of commands.
	var targz []byte
	for i, command := range p.Commands {
		gettar := i == len(p.Commands)-1 // the last command is supposed to be gomote gettar
		output, err := p.Exec(command, !gettar)
		if err != nil {
			return errors.New("command failed")
		}
		if gettar {
			targz = output
		}
	}

	// Untar the runtime and write it to goroot.
	sysof := filepath.Join(*flagGoroot, "src", "runtime", "race", fmt.Sprintf("race_%v_%s.syso", p.OS, p.Arch))
	if err := p.WriteSyso(sysof, targz); err != nil {
		return fmt.Errorf("%v", err)
	}

	log.Printf("%v: build completed", p.Name)
	return nil
}

func (p *Platform) WriteSyso(sysof string, targz []byte) error {
	syso, err := os.Create(sysof)
	if err != nil {
		return fmt.Errorf("failed to open race runtime: %v", err)
	}
	defer syso.Close()

	// Ungzip.
	gzipr, err := gzip.NewReader(bytes.NewReader(targz))
	if err != nil {
		return fmt.Errorf("failed to read gzip archive: %v", err)
	}
	defer gzipr.Close()

	// Find the necessary file in tar.
	tr := tar.NewReader(gzipr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return fmt.Errorf("failed to read tar archive: %v", err)
		}
		if hdr.Name == filepath.Base(syso.Name()) {
			break
		}
	}

	if _, err := io.Copy(syso, tr); err != nil {
		return fmt.Errorf("failed to write race runtime: %v", err)
	}
	return nil
}

func (p *Platform) Exec(command string, logOutout bool) ([]byte, error) {
	command = strings.Replace(command, "$REV", strconv.Itoa(*flagRev), -1)
	command = strings.Replace(command, "$INST", p.Inst, -1)
	command = strings.Replace(command, "$GOOS", p.OS, -1)
	command = strings.Replace(command, "$GOARCH", p.Arch, -1)
	log.Printf("%s: %s", p.Name, command)
	fmt.Fprintf(p.Log, "$ %v\n", command)
	args := strings.Split(command, " ")
	cmd := exec.Command(args[0], args[1:]...)
	output, err := cmd.CombinedOutput()
	if logOutout {
		p.Log.Write(output)
	}
	fmt.Fprintf(p.Log, "\n\n")
	return output, err
}
