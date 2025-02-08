// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type msiFile struct {
	Name   string
	Size   int64
	SHA256 string
}

// DiffWindowsMsi diffs the content of the Windows msi and zip files provided,
// logging differences. It returns true if the files were successfully parsed
// and contain the same files, false otherwise.
func DiffWindowsMsi(log *Log, zip, msi []byte) (ok, skip bool) {
	check := func(log *Log, rebuilt *ZipFile, posted *msiFile) bool {
		match := true
		name := rebuilt.Name
		field := func(what string, rebuilt, posted any) {
			if posted != rebuilt {
				log.Printf("%s: rebuilt %s = %v, posted = %v", name, what, rebuilt, posted)
				match = false
			}
		}
		r := rebuilt
		p := posted
		field("name", r.Name, p.Name)
		field("size", int64(r.UncompressedSize64), p.Size)
		field("content", r.SHA256, p.SHA256)
		return match
	}

	ix, skip := indexMsi(log, msi, nil)
	if skip {
		return
	}

	return DiffArchive(log, IndexZip(log, zip, nil), ix, check), false
}

func indexMsi(log *Log, msi []byte, fix Fixer) (m map[string]*msiFile, skip bool) {
	dir, err := os.MkdirTemp("", "gorebuild-")
	if err != nil {
		log.Printf("%v", err)
		return nil, false
	}
	defer os.RemoveAll(dir)

	tmpmsi := filepath.Join(dir, "go.msi")
	if err := os.WriteFile(tmpmsi, msi, 0666); err != nil {
		log.Printf("%v", err)
		return nil, false
	}

	cmd := exec.Command("msiextract", tmpmsi)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("msiextract: %s\n%s", err, out)
		return nil, errors.Is(err, exec.ErrNotFound)
	}
	// msiextract lists every file, so don't show the output on success.

	// amd64 installer uses Go but 386 uses Program Files\Go. Try both.
	root := filepath.Join(dir, "Go")
	if _, err := os.Stat(root); err != nil {
		root = filepath.Join(dir, `Program Files/Go`)
	}

	ix := make(map[string]*msiFile)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := "go/" + filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if fix != nil {
			data = fix(log, name, data)
		}
		ix[name] = &msiFile{name, int64(len(data)), SHA256(data)}
		return nil
	})
	if err != nil {
		log.Printf("%v", err)
		return nil, false
	}
	return ix, false
}
