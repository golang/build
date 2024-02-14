// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/internal/workflow"
)

const (
	goDir = "go"
	go14  = "go1.4"
)

type BuildletStep struct {
	Target      *releasetargets.Target
	Buildlet    buildlet.RemoteClient
	BuildConfig *dashboard.BuildConfig
	LogWriter   io.Writer
}

func (b *BuildletStep) makeEnv() []string {
	// We need GOROOT_FINAL both during the binary build and test runs. See go.dev/issue/52236.
	makeEnv := []string{"GOROOT_FINAL=" + dashboard.GorootFinal(b.Target.GOOS)}
	// Add extra vars from the target's configuration.
	makeEnv = append(makeEnv, b.Target.ExtraEnv...)
	return makeEnv
}

// ExtractFile copies the first file in tgz matching glob to dest.
func ExtractFile(tgz io.Reader, dest io.Writer, glob string) error {
	zr, err := gzip.NewReader(tgz)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		} else if err != nil {
			return err
		}
		if match, _ := path.Match(glob, h.Name); !h.FileInfo().IsDir() && match {
			break
		}
	}
	_, err = io.Copy(dest, tr)
	return err
}

func (b *BuildletStep) TestTarget(ctx *workflow.TaskContext, binaryArchive io.Reader) error {
	if err := b.Buildlet.PutTar(ctx, binaryArchive, ""); err != nil {
		return err
	}
	if u := b.BuildConfig.GoBootstrapURL(buildenv.Production); u != "" {
		ctx.Printf("Installing go1.4 (second time, for all.bash).")
		if err := b.Buildlet.PutTarFromURL(ctx, u, go14); err != nil {
			return err
		}
	}
	ctx.Printf("Building (all.bash to ensure tests pass).")
	ctx.SetWatchdogScale(b.BuildConfig.GoTestTimeoutScale())
	return b.exec(ctx, goDir+"/"+b.BuildConfig.AllScript(), b.BuildConfig.AllScriptArgs(), buildlet.ExecOpts{
		ExtraEnv: b.makeEnv(),
	})
}

func (b *BuildletStep) RunTryBot(ctx *workflow.TaskContext, sourceArchive io.Reader) error {
	ctx.Printf("Pushing source to buildlet.")
	if err := b.Buildlet.PutTar(ctx, sourceArchive, ""); err != nil {
		return fmt.Errorf("failed to put generated source tarball: %v", err)
	}

	if u := b.BuildConfig.GoBootstrapURL(buildenv.Production); u != "" {
		ctx.Printf("Installing go1.4.")
		if err := b.Buildlet.PutTarFromURL(ctx, u, go14); err != nil {
			return err
		}
	}

	if b.BuildConfig.CompileOnly {
		ctx.Printf("Building toolchain (make.bash).")
		if err := b.exec(ctx, goDir+"/"+b.BuildConfig.MakeScript(), b.BuildConfig.MakeScriptArgs(), buildlet.ExecOpts{}); err != nil {
			return fmt.Errorf("building toolchain failed: %v", err)
		}
		ctx.Printf("Compiling-only tests (via BuildConfig.CompileOnly).")
		if err := b.runGo(ctx, []string{"tool", "dist", "test", "-compile-only"}, buildlet.ExecOpts{}); err != nil {
			return fmt.Errorf("building tests failed: %v", err)
		}
		return nil
	}

	ctx.Printf("Testing (all.bash).")
	ctx.SetWatchdogScale(b.BuildConfig.GoTestTimeoutScale())
	err := b.exec(ctx, goDir+"/"+b.BuildConfig.AllScript(), b.BuildConfig.AllScriptArgs(), buildlet.ExecOpts{})
	if err != nil {
		ctx.Printf("testing failed: %v", err)
	}
	return err
}

// exec runs cmd with args. Its working dir is opts.Dir, or the directory of cmd.
// Its environment is the buildlet's environment, plus a GOPATH setting, plus opts.ExtraEnv.
// If the command fails, its output is included in the returned error.
func (b *BuildletStep) exec(ctx context.Context, cmd string, args []string, opts buildlet.ExecOpts) error {
	work, err := b.Buildlet.WorkDir(ctx)
	if err != nil {
		return err
	}

	// Set up build environment. The caller's environment wins if there's a conflict.
	env := append(b.BuildConfig.Env(), "GOPATH="+work+"/gopath")
	env = append(env, opts.ExtraEnv...)
	opts.Output = b.LogWriter
	opts.ExtraEnv = env
	opts.Args = args
	opts.Debug = true // Print debug info.
	remoteErr, execErr := b.Buildlet.Exec(ctx, cmd, opts)
	if execErr != nil {
		return execErr
	}
	if remoteErr != nil {
		return fmt.Errorf("Command %v %s failed: %v", cmd, args, remoteErr)
	}

	return nil
}

// runGo runs the go command with args using BuildletStep.exec.
func (b *BuildletStep) runGo(ctx context.Context, args []string, execOpts buildlet.ExecOpts) error {
	return b.exec(ctx, goDir+"/bin/go", args, execOpts)
}

func ConvertZIPToTGZ(r io.ReaderAt, size int64, w io.Writer) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}

	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)

	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     f.Name,
			Typeflag: tar.TypeReg,
			Mode:     0o777,
			Size:     int64(f.UncompressedSize64),

			AccessTime: f.Modified,
			ChangeTime: f.Modified,
			ModTime:    f.Modified,
		}); err != nil {
			return err
		}
		content, err := f.Open()
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, content); err != nil {
			return err
		}
		if err := content.Close(); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}
