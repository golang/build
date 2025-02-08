// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/sync/errgroup"
)

// VSCodeGoReleaseTask releases vscode go.
//
// 1. cross-compile github.com/golang/vscode-go/vscgo and store the artifacts in the scratchFS.
// 2. (TODO) sign the artifacts.
// 3. (TODO) tag the repository.
// 4. (TODO) trigger the GCB workflow that reads the signed artifacts, packages, and publishes them.
// 5. (TODO) announce (SNS)
type VSCodeGoReleaseTask struct {
	CloudBuild CloudBuildClient
	*ScratchFS
	Revision string
}

func checkVersion(v string) error {
	if len(v) < 2 || v[0] != 'v' {
		return errors.New("release version must start with 'v'")
	}
	return nil
}

var vscgoVersionParam = wf.ParamDef[string]{
	Name:    "Extension version to release",
	Example: "v0.0.0-rc.1", Check: checkVersion,
}

func (t *VSCodeGoReleaseTask) NewDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ToolsTeam}})

	version := wf.Param(wd, vscgoVersionParam)
	unsignedArtifacts := wf.Task1(wd, "build vscgo", t.buildVSCGO, version)
	wf.Output(wd, "build artifacts", unsignedArtifacts)

	// TODO: sign
	return wd
}

var vscgoPlatforms = []struct {
	Platform string
	Env      []string
}{
	{Platform: "win32-x64", Env: []string{"GOOS=windows", "GOARCH=amd64"}},
	{Platform: "win32-arm64", Env: []string{"GOOS=windows", "GOARCH=arm64"}},
	{Platform: "darwin-x64", Env: []string{"GOOS=darwin", "GOARCH=amd64"}},
	{Platform: "darwin-arm64", Env: []string{"GOOS=darwin", "GOARCH=arm64"}},
	{Platform: "linux-x64", Env: []string{"GOOS=linux", "GOARCH=amd64"}},
	{Platform: "linux-arm64", Env: []string{"GOOS=linux", "GOARCH=arm64"}},
	// { Platform: "linux-arm", Env: []string{"GOOS=linux", "GOARCH=arm"}},
	// { Platform:"linux-armhf", Env: []string{"GOOS=linux", "GOARCH=arm64", "GOARM=7"}},
}

type goBuildArtifact struct {
	Platform string
	Filename string
}

func (t *VSCodeGoReleaseTask) buildVSCGO(ctx *wf.TaskContext, version string) ([]goBuildArtifact, error) {
	// TODO: version stamping won't use the tagged version with go build.

	// TODO: encode it in vscode-go's build script. Then,
	// we can just "go run -C extension tools/release/release.go build-vscgo".
	var b strings.Builder
	fmt.Fprintf(&b, "git fetch && git switch %v\n", t.Revision)
	fmt.Fprintf(&b, "export OUT=$(mktemp -d /tmp/vscgo-XXXXXXXX)\n")
	fmt.Fprintf(&b, "export CGO_ENABLED=0\n")
	for _, info := range vscgoPlatforms {
		envs := strings.Join(info.Env, " ")
		base := "vscgo"
		if strings.HasPrefix(info.Platform, "win32") {
			base = "vscgo.exe"
		}
		fmt.Fprintf(&b, "mkdir ${OUT}/%v\n", info.Platform)
		fmt.Fprintf(&b, "%v go build -o ${OUT}/%v/%v github.com/golang/vscode-go/vscgo\n", envs, info.Platform, base)
	}
	fmt.Fprintf(&b, "mkdir out && mv ${OUT}/* out/\n")
	script := b.String()

	build, err := t.CloudBuild.RunScript(ctx, script, "vscode-go", []string{"out"})
	if err != nil {
		return nil, err
	}
	if _, err := AwaitCondition(ctx, 30*time.Second, func() (string, bool, error) {
		return t.CloudBuild.Completed(ctx, build)
	}); err != nil {
		return nil, err
	}
	outfs, err := t.CloudBuild.ResultFS(ctx, build)
	if err != nil {
		return nil, err
	}
	var artifacts []goBuildArtifact
	err = fs.WalkDir(outfs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if name := d.Name(); name != "vscgo" && name != "vscgo.exe" {
			return nil
		}
		platform := filepath.Base(filepath.Dir(path)) // platform name is the parent name.
		artifacts = append(artifacts, goBuildArtifact{
			Platform: platform,
			Filename: path,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	var eg errgroup.Group
	for i := range artifacts {
		idx := i
		eg.Go(func() error {
			platform, path := artifacts[idx].Platform, artifacts[idx].Filename

			in, err := outfs.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			name, out, err := t.ScratchFS.OpenWrite(ctx, platform+"-vscgo.zip")
			if err != nil {
				out.Close()
				return err
			}
			// write as zip file.
			zw := zip.NewWriter(out)
			f, err := zw.Create(filepath.Base(path))
			if err != nil {
				out.Close()
				return err
			}
			if _, err := io.Copy(f, in); err != nil {
				out.Close()
				return err
			}
			if err := zw.Close(); err != nil {
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			// replace artifacts
			artifacts[idx] = goBuildArtifact{
				Platform: platform,
				Filename: name,
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return artifacts, nil
}
