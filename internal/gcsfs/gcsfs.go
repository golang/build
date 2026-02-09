// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// gcsfs implements io/fs for GCS, adding writability.
package gcsfs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

// FromURL creates a new FS from a file:// or gs:// URL.
// client is only used for gs:// URLs and can be nil otherwise.
func FromURL(ctx context.Context, client *storage.Client, base string) (fs.FS, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "gs":
		if u.Host == "" {
			return nil, fmt.Errorf("missing bucket in %q", base)
		}
		fsys := NewFS(ctx, client, u.Host)
		if prefix := strings.TrimPrefix(u.Path, "/"); prefix != "" {
			return fs.Sub(fsys, prefix)
		}
		return fsys, nil
	case "file":
		return DirFS(u.Path), nil
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
}

// Create creates a new file on fsys, which must be a CreateFS.
func Create(fsys fs.FS, name string) (WriterFile, error) {
	cfs, ok := fsys.(CreateFS)
	if !ok {
		return nil, &fs.PathError{Op: "create", Path: name, Err: fmt.Errorf("not implemented on type %T", fsys)}
	}
	return cfs.Create(name)
}

// CreateFS is an fs.FS that supports creating writable files.
type CreateFS interface {
	fs.FS
	Create(string) (WriterFile, error)
}

// WriterFile is an fs.File that can be written to.
// The behavior of writing and reading the same file is undefined.
type WriterFile interface {
	fs.File
	io.Writer
}

// WriteFile is like os.WriteFile for CreateFSs.
func WriteFile(fsys fs.FS, filename string, contents []byte) error {
	f, err := Create(fsys, filename)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(contents); err != nil {
		return err
	}
	return f.Close()
}

// gcsFS implements fs.FS for GCS.
type gcsFS struct {
	ctx    context.Context
	client *storage.Client
	bucket *storage.BucketHandle
	prefix string
}

var _ = fs.FS((*gcsFS)(nil))
var _ = CreateFS((*gcsFS)(nil))
var _ = fs.SubFS((*gcsFS)(nil))

// NewFS creates a new fs.FS that uses ctx for all of its operations.
// Creating a new FS does not access the network, so they can be created
// and destroyed per-context.
//
// Once the context has finished, all objects created by this FS should
// be considered invalid. In particular, Writers and Readers will be canceled.
func NewFS(ctx context.Context, client *storage.Client, bucket string) fs.FS {
	return &gcsFS{
		ctx:    ctx,
		client: client,
		bucket: client.Bucket(bucket),
	}
}

func (fsys *gcsFS) object(name string) *storage.ObjectHandle {
	return fsys.bucket.Object(path.Join(fsys.prefix, name))
}

// Open opens the named file.
func (fsys *gcsFS) Open(name string) (fs.File, error) {
	if !validPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		name = ""
	}
	return &GCSFile{
		fs:   fsys,
		name: strings.TrimSuffix(name, "/"),
	}, nil
}

// Create creates the named file.
func (fsys *gcsFS) Create(name string) (WriterFile, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	return f.(*GCSFile), nil
}

func (fsys *gcsFS) Sub(dir string) (fs.FS, error) {
	copy := *fsys
	copy.prefix = path.Join(fsys.prefix, dir)
	return &copy, nil
}

// fstest likes to send us backslashes. Treat them as invalid.
func validPath(name string) bool {
	return fs.ValidPath(name) && !strings.ContainsRune(name, '\\')
}

// GCSFile implements fs.File for GCS. It is also a WriteFile.
type GCSFile struct {
	fs   *gcsFS
	name string

	reader   io.ReadCloser
	writer   io.WriteCloser
	iterator *storage.ObjectIterator
}

var _ = fs.File((*GCSFile)(nil))
var _ = fs.ReadDirFile((*GCSFile)(nil))
var _ = io.WriteCloser((*GCSFile)(nil))

func (f *GCSFile) Close() error {
	if f.reader != nil {
		defer f.reader.Close()
	}
	if f.writer != nil {
		defer f.writer.Close()
	}

	if f.reader != nil {
		err := f.reader.Close()
		if err != nil {
			return f.translateError("close", err)
		}
	}
	if f.writer != nil {
		err := f.writer.Close()
		if err != nil {
			return f.translateError("close", err)
		}
	}
	return nil
}

func (f *GCSFile) Read(b []byte) (int, error) {
	if f.reader == nil {
		reader, err := f.fs.object(f.name).NewReader(f.fs.ctx)
		if err != nil {
			return 0, f.translateError("read", err)
		}
		f.reader = reader
	}
	n, err := f.reader.Read(b)
	return n, f.translateError("read", err)
}

// Write writes to the GCS object associated with this File.
//
// A new object will be created unless an object with this name already exists.
// Otherwise any previous object with the same name will be replaced.
// The object will not be available (and any previous object will remain)
// until Close has been called.
func (f *GCSFile) Write(b []byte) (int, error) {
	if f.writer == nil {
		f.writer = f.fs.object(f.name).NewWriter(f.fs.ctx)
	}
	return f.writer.Write(b)
}

// ReadDir implements io/fs.ReadDirFile.
func (f *GCSFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if f.iterator == nil {
		f.iterator = f.fs.iterator(f.name)
	}
	var result []fs.DirEntry
	var err error
	for {
		var info *storage.ObjectAttrs
		info, err = f.iterator.Next()
		if err != nil {
			break
		}
		result = append(result, &gcsFileInfo{info})
		if len(result) == n {
			break
		}
	}
	if err == iterator.Done {
		if n <= 0 {
			err = nil
		} else {
			err = io.EOF
		}
	}
	return result, f.translateError("readdir", err)
}

// Stats the file.
// The returned FileInfo exposes *storage.ObjectAttrs as its Sys() result.
func (f *GCSFile) Stat() (fs.FileInfo, error) {
	// Check for a real file.
	attrs, err := f.fs.object(f.name).Attrs(f.fs.ctx)
	if err != nil && err != storage.ErrObjectNotExist {
		return nil, f.translateError("stat", err)
	}
	if err == nil {
		return &gcsFileInfo{attrs: attrs}, nil
	}
	// Check for a "directory".
	iter := f.fs.iterator(f.name)
	if _, err := iter.Next(); err == nil {
		return &gcsFileInfo{
			attrs: &storage.ObjectAttrs{
				Prefix: f.name + "/",
			},
		}, nil
	}
	return nil, f.translateError("stat", storage.ErrObjectNotExist)
}

func (f *GCSFile) translateError(op string, err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	nested := err
	if err == storage.ErrBucketNotExist || err == storage.ErrObjectNotExist {
		nested = fs.ErrNotExist
	} else if pe, ok := err.(*fs.PathError); ok {
		nested = pe.Err
	}
	return &fs.PathError{Op: op, Path: strings.TrimPrefix(f.name, f.fs.prefix), Err: nested}
}

// gcsFileInfo implements fs.FileInfo and fs.DirEntry.
type gcsFileInfo struct {
	attrs *storage.ObjectAttrs
}

var _ = fs.FileInfo((*gcsFileInfo)(nil))
var _ = fs.DirEntry((*gcsFileInfo)(nil))

func (fi *gcsFileInfo) Name() string {
	if fi.attrs.Prefix != "" {
		return path.Base(fi.attrs.Prefix)
	}
	return path.Base(fi.attrs.Name)
}

func (fi *gcsFileInfo) Size() int64 {
	return fi.attrs.Size
}

func (fi *gcsFileInfo) Mode() fs.FileMode {
	if fi.IsDir() {
		return fs.ModeDir | 0777
	}
	return 0666 // check fi.attrs.ACL?
}

func (fi *gcsFileInfo) ModTime() time.Time {
	return fi.attrs.Updated
}

func (fi *gcsFileInfo) IsDir() bool {
	return fi.attrs.Prefix != ""
}

func (fi *gcsFileInfo) Sys() any {
	return fi.attrs
}

func (fi *gcsFileInfo) Info() (fs.FileInfo, error) {
	return fi, nil
}

func (fi *gcsFileInfo) Type() fs.FileMode {
	return fi.Mode() & fs.ModeType
}

func (fsys *gcsFS) iterator(name string) *storage.ObjectIterator {
	prefix := path.Join(fsys.prefix, name)
	if prefix != "" {
		prefix += "/"
	}
	return fsys.bucket.Objects(fsys.ctx, &storage.Query{
		Delimiter: "/",
		Prefix:    prefix,
	})
}
