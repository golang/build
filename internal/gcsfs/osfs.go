package gcsfs

import (
	"io/fs"
	"os"
	"path"
	"runtime"
)

var _ = fs.FS((*dirFS)(nil))
var _ = CreateFS((*dirFS)(nil))

func DirFS(dir string) fs.FS {
	return dirFS(dir)
}

func containsAny(s, chars string) bool {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return true
			}
		}
	}
	return false
}

type dirFS string

func (dir dirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || runtime.GOOS == "windows" && containsAny(name, `\:`) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	f, err := os.Open(string(dir) + "/" + name)
	if err != nil {
		return nil, err // nil fs.File
	}
	return f, nil
}

func (dir dirFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) || runtime.GOOS == "windows" && containsAny(name, `\:`) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	f, err := os.Stat(string(dir) + "/" + name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (dir dirFS) Create(name string) (WriteFile, error) {
	if !fs.ValidPath(name) || runtime.GOOS == "windows" && containsAny(name, `\:`) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	f, err := os.Create(string(dir) + "/" + name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (dir dirFS) Sub(subDir string) (fs.FS, error) {
	return dirFS(path.Join(string(dir), subDir)), nil
}
