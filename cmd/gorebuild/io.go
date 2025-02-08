// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SHA256 returns the hexadecimal SHA256 hash of data.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

// Get returns the content at the named URL.
//
// When it encounters what might be a temporary network error,
// it tries fetching multiple times with delays before giving up.
func Get(log *Log, url string) (_ []byte, err error) {
	defer func() {
		if err != nil && log != nil {
			log.Printf("%s", err)
		}
	}()

	// Fetching happens over an unreliable network connection,
	// and will fail sometimes. Be willing to try a few times.
	const maxTries = 5
	var fetchErrors []error
	for i := range maxTries {
		time.Sleep(time.Duration(i*i) * time.Second)
		resp, err := http.Get(url)
		if err != nil {
			fetchErrors = append(fetchErrors, fmt.Errorf("attempt %d: failed to GET: %v", i+1, err))
			continue
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			err := fmt.Errorf("non-200 OK status code: %v body: %q", resp.Status, body)
			if resp.StatusCode/100 == 5 {
				// Consider a 5xx server response to possibly succeed later.
				fetchErrors = append(fetchErrors, fmt.Errorf("attempt %d: %v", i+1, err))
				continue
			}
			return nil, fmt.Errorf("get %s: %v", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			fetchErrors = append(fetchErrors, fmt.Errorf("attempt %d: failed to read body: %v", i+1, err))
			continue
		}
		if log != nil {
			log.Printf("downloaded %s", url)
		}
		return body, nil
	}
	// All tries exhausted, give up at this point.
	return nil, fmt.Errorf("get %s: %v", url, errors.Join(fetchErrors...))
}

// GerritTarGz returns a .tar.gz file corresponding to the named repo and ref on Go's Gerrit server.
func GerritTarGz(log *Log, repo, ref string) ([]byte, error) {
	return Get(log, "https://go.googlesource.com/"+repo+"/+archive/"+ref+".tar.gz")
}

// A DLRelease is the JSON for a release, returned by go.dev/dl.
type DLRelease struct {
	Version string    `json:"version"`
	Stable  bool      `json:"stable"`
	Files   []*DLFile `json:"files"`
}

// A DLFile is the JSON for a file, returned by go.dev/dl.
type DLFile struct {
	Name    string `json:"filename"`
	GOOS    string `json:"os"`
	GOARCH  string `json:"arch"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Kind    string `json:"kind"` // "archive", "installer", "source"
}

// DLReleases returns the release list from go.dev/dl.
func DLReleases(log *Log) ([]*DLRelease, error) {
	var all []*DLRelease
	data, err := Get(log, "https://go.dev/dl/?mode=json&include=all")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("unmarshaling releases JSON: %v", err)
	}

	for _, r := range all {
		for _, f := range r.Files {
			if f.GOARCH == "armv6l" {
				f.GOARCH = "arm"
			}
		}
	}
	return all, nil
}

// OpenTarGz returns a tar.Reader for the given tgz data.
func OpenTarGz(tgz []byte) (*tar.Reader, error) {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	return tar.NewReader(zr), nil
}

// UnpackTarGz unpacks the given tgz data into the named directory.
// On error the directory may contain partial contents.
func UnpackTarGz(dir string, tgz []byte) error {
	if err := os.MkdirAll(dir, 0777); err != nil {
		return err
	}
	tr, err := OpenTarGz(tgz)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if hdr.Typeflag == tar.TypeDir {
			// Ignore directories entirely
			continue
		}
		name := filepath.FromSlash(hdr.Name)
		if name != filepath.Clean(name) || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("invalid name in tgz: %#q", hdr.Name)
		}
		targ := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(targ), 0777); err != nil {
			return err
		}
		f, err := os.OpenFile(targ, os.O_CREATE|os.O_WRONLY, fs.FileMode(hdr.Mode&0777))
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// OpenZip returns a zip.Reader for the given zip data.
func OpenZip(zipdata []byte) (*zip.Reader, error) {
	return zip.NewReader(bytes.NewReader(zipdata), int64(len(zipdata)))
}

// UnpackZip unpacks the given zip data into the named directory.
// On error the directory may contain partial contents.
func UnpackZip(dir string, zipdata []byte) error {
	if err := os.MkdirAll(dir, 0777); err != nil {
		return err
	}
	zr, err := OpenZip(zipdata)
	if err != nil {
		return err
	}
	for _, zf := range zr.File {
		if strings.HasSuffix(zf.Name, "/") {
			// Ignore directories entirely
			continue
		}
		name := filepath.FromSlash(zf.Name)
		if name != filepath.Clean(name) || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("invalid name in zip: %#q", zf.Name)
		}
		targ := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(targ), 0777); err != nil {
			return err
		}
		f, err := os.OpenFile(targ, os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			return err
		}
		zr, err := zf.Open()
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(f, zr)
		zr.Close()
		if err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// A Fixer is a transformation on file content applied during indexing.
// It lets us edit away permitted differences between files, such as code
// signatures that cannot be reproduced without the signing keys.
type Fixer = func(*Log, string, []byte) []byte

// A TarFile summarizes a single file in a tar archive:
// it records the exact header and the SHA256 of the content.
type TarFile struct {
	tar.Header
	SHA256 string
}

// A ZipFile summarizes a single file in a zip archive:
// it records the exact header and the SHA256 of the content.
type ZipFile struct {
	zip.FileHeader
	SHA256 string
}

// A CpioFile represents a single file in a CPIO archive.
type CpioFile struct {
	Name   string
	Mode   fs.FileMode
	Size   int64
	SHA256 string
}

// IndexTarGz parses tgz as a gzip-compressed tar file and returns an index of its content.
// If fix is non-nil, it is applied to file content before indexing.
// This lets us strip code signatures that cannot be reproduced.
func IndexTarGz(log *Log, tgz []byte, fix Fixer) map[string]*TarFile {
	tr, err := OpenTarGz(tgz)
	if err != nil {
		log.Printf("%v", err)
		return nil
	}
	ix := make(map[string]*TarFile)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("reading tgz: %v", err)
			return nil
		}
		if hdr.Typeflag == tar.TypeDir {
			// Ignore directories entirely
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			log.Printf("reading %s from tgz: %v", hdr.Name, err)
			return nil
		}
		if fix != nil {
			data = fix(log, hdr.Name, data)
			hdr.Size = int64(len(data))
		}
		ix[hdr.Name] = &TarFile{*hdr, SHA256(data)}
	}
	return ix
}

// IndexZip parses zipdata as a zip archive and returns an index of its content.
// If fix is non-nil, it is applied to file content before indexing.
// This lets us strip code signatures that cannot be reproduced.
func IndexZip(log *Log, zipdata []byte, fix Fixer) map[string]*ZipFile {
	zr, err := zip.NewReader(bytes.NewReader(zipdata), int64(len(zipdata)))
	if err != nil {
		log.Printf("%v", err)
		return nil
	}
	ix := make(map[string]*ZipFile)
	for _, hdr := range zr.File {
		if strings.HasSuffix(hdr.Name, "/") {
			// Ignore directories entirely
			continue
		}
		rc, err := hdr.Open()
		if err != nil {
			log.Printf("%v", err)
			return nil
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Printf("%v", err)
			return nil
		}
		if fix != nil {
			data = fix(log, hdr.Name, data)
			hdr.CRC32 = crc32.ChecksumIEEE(data)
			hdr.UncompressedSize = uint32(len(data))
			hdr.UncompressedSize64 = uint64(len(data))
		}
		ix[hdr.Name] = &ZipFile{hdr.FileHeader, SHA256(data)}
	}
	return ix
}

// IndexCpioGz parses data as a gzip-compressed cpio file and returns an index of its content.
// If fix is non-nil, it is applied to file content before indexing.
// This lets us strip code signatures that cannot be reproduced.
func IndexCpioGz(log *Log, data []byte, fix Fixer) map[string]*CpioFile {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		log.Printf("%v", err)
		return nil
	}
	br := bufio.NewReader(zr)

	const hdrSize = 76

	ix := make(map[string]*CpioFile)
	hdr := make([]byte, hdrSize)
	for {
		_, err := io.ReadFull(br, hdr)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("reading archive: %v", err)
			return nil
		}

		// https://www.mkssoftware.com/docs/man4/cpio.4.asp
		//
		//	hdr[0:6] "070707"
		//	hdr[6:12] device number (all numbers '0'-padded octal)
		//	hdr[12:18] inode number
		//	hdr[18:24] mode
		//	hdr[24:30] uid
		//	hdr[30:36] gid
		//	hdr[36:42] nlink
		//	hdr[42:48] rdev
		//	hdr[48:59] mtime
		//	hdr[59:65] name length
		//	hdr[65:76] file size

		if !allOctal(hdr[:]) || string(hdr[:6]) != "070707" {
			log.Printf("reading archive: malformed entry")
			return nil
		}
		mode, _ := strconv.ParseInt(string(hdr[18:24]), 8, 64)
		nameLen, _ := strconv.ParseInt(string(hdr[59:65]), 8, 64)
		size, _ := strconv.ParseInt(string(hdr[65:76]), 8, 64)
		nameBuf := make([]byte, nameLen)
		if _, err := io.ReadFull(br, nameBuf); err != nil {
			log.Printf("reading archive: %v", err)
			return nil
		}
		if nameLen == 0 || nameBuf[nameLen-1] != 0 {
			log.Printf("reading archive: malformed entry")
			return nil
		}
		name := string(nameBuf[:nameLen-1])

		// The MKS cpio page says "TRAILER!!"
		// but the Apple pkg files use "TRAILER!!!".
		if name == "TRAILER!!!" {
			break
		}

		fmode := fs.FileMode(mode & 0777)
		if mode&040000 != 0 {
			fmode |= fs.ModeDir
		}

		data, err := io.ReadAll(io.LimitReader(br, size))
		if err != nil {
			log.Printf("reading archive: %v", err)
			return nil
		}
		if size != int64(len(data)) {
			log.Printf("reading archive: short file")
			return nil
		}

		if fmode&fs.ModeDir != 0 {
			continue
		}

		if fix != nil {
			data = fix(log, name, data)
			size = int64(len(data))
		}
		ix[name] = &CpioFile{name, fmode, size, SHA256(data)}
	}
	return ix
}

// allOctal reports whether x is entirely ASCII octal digits.
func allOctal(x []byte) bool {
	for _, b := range x {
		if b < '0' || '7' < b {
			return false
		}
	}
	return true
}

// DiffArchive diffs the archives 'rebuild' and 'posted' based on their indexes.
// It reports to log any files that appear only in one or the other.
// For files that appear in both, DiffArchive calls check, which should
// log any differences found and report whether the files match.
// It reports whether the archives match.
// If either of rebuild or posted is nil, DiffArchive returns false without logging,
// assuming that the code that returned the nil archive took care of reporting the problem.
func DiffArchive[File1, File2 any](log *Log,
	rebuilt map[string]File1, posted map[string]File2,
	check func(*Log, File1, File2) bool) bool {

	if rebuilt == nil || posted == nil {
		return false
	}

	// Build list of all names; will have duplicates.
	var names []string
	for name := range rebuilt {
		names = append(names, name)
	}
	for name := range posted {
		names = append(names, name)
	}
	sort.Strings(names)

	match := true
	for _, name := range names {
		fr, okr := rebuilt[name]
		fp, okp := posted[name]
		if !okr && !okp { // duplicate name
			continue
		}
		if !okp {
			log.Printf("%s: missing from posted archive", name)
			match = false
			continue
		}
		if !okr {
			log.Printf("%s: unexpected file in posted archive", name)
			match = false
			continue
		}
		delete(rebuilt, name)
		delete(posted, name)

		if !check(log, fr, fp) {
			match = false
		}
	}
	return match
}

// DiffTarGz diffs the tgz files rebuilt and posted, reporting any differences to log
// and applying fix to files before comparing them.
// It reports whether the archives match.
func DiffTarGz(log *Log, rebuilt, posted []byte, fix Fixer) bool {
	n := 0
	check := func(log *Log, rebuilt, posted *TarFile) bool {
		match := true
		name := rebuilt.Name
		field := func(what string, rebuilt, posted any) {
			if posted != rebuilt {
				if n++; n <= 100 {
					log.Printf("%s: rebuilt %s = %v, posted = %v", name, what, rebuilt, posted)
				} else if n == 101 {
					log.Printf("eliding additional diffs ...")
				}
				match = false
			}
		}
		r := rebuilt
		p := posted
		field("typeflag", r.Typeflag, p.Typeflag)
		field("linkname", r.Linkname, p.Linkname)
		field("mode", r.Mode, p.Mode)
		field("uid", r.Uid, p.Uid)
		field("gid", r.Gid, p.Gid)
		field("uname", r.Uname, p.Uname)
		field("gname", r.Gname, p.Gname)
		field("mtime", r.ModTime, p.ModTime)
		field("atime", r.AccessTime, p.AccessTime)
		field("ctime", r.ChangeTime, p.ChangeTime)
		field("devmajor", r.Devmajor, p.Devmajor)
		field("devminor", r.Devminor, p.Devminor)
		for k, vhdr := range r.PAXRecords {
			field("PAX:"+k, vhdr, p.PAXRecords[k])
		}
		for k, vf := range p.PAXRecords {
			if vhdr, ok := r.PAXRecords[k]; !ok {
				field("PAX:"+k, vhdr, vf)
			}
		}
		field("format", r.Format, p.Format)
		field("size", r.Size, p.Size)
		field("content", r.SHA256, p.SHA256)
		return match
	}

	return DiffArchive(log, IndexTarGz(log, rebuilt, fix), IndexTarGz(log, posted, fix), check)
}

// DiffZip diffs the zip files rebuilt and posted, reporting any differences to log
// and applying fix to files before comparing them.
// It reports whether the archives match.
func DiffZip(log *Log, rebuilt, posted []byte, fix Fixer) bool {
	n := 0
	check := func(log *Log, rebuilt, posted *ZipFile) bool {
		match := true
		name := rebuilt.Name
		field := func(what string, rebuilt, posted any) {
			if posted != rebuilt {
				if n++; n <= 100 {
					log.Printf("%s: rebuilt %s = %v, posted = %v", name, what, rebuilt, posted)
				} else if n == 101 {
					log.Printf("eliding additional diffs ...")
				}
				match = false
			}
		}
		r := rebuilt
		p := posted

		field("comment", r.Comment, p.Comment)
		field("nonutf8", r.NonUTF8, p.NonUTF8)
		field("creatorversion", r.CreatorVersion, p.CreatorVersion)
		field("readerversion", r.ReaderVersion, p.ReaderVersion)
		field("flags", r.Flags, p.Flags)
		field("method", r.Method, p.Method)
		// Older versions of Go produce unequal Modified times in archive/zip,
		// presumably due to some kind of archive/zip parsing error,
		// or perhaps due to the Extra field being doubled below.
		// The problem does not happen with Go 1.20.
		// To allow people to use older Go versions to run gorebuild,
		// we only check the actual time instant, not the location, in Modified.
		field("modifiedUnix", r.Modified.UnixNano(), p.Modified.UnixNano())
		field("mtime", r.ModifiedTime, p.ModifiedTime)
		field("mdate", r.ModifiedDate, p.ModifiedDate)
		if len(p.Extra) == 2*len(r.Extra) && string(p.Extra) == string(r.Extra)+string(r.Extra) {
			// Mac signing rewrites the zip file, which ends up doubling
			// the Extra field due to go.dev/issue/61572.
			// Allow that.
		} else {
			field("extra", fmt.Sprintf("%x", r.Extra), fmt.Sprintf("%x", p.Extra))
		}
		field("crc32", r.CRC32, p.CRC32)
		field("xattrs", r.ExternalAttrs, p.ExternalAttrs)
		field("usize32", r.UncompressedSize, p.UncompressedSize)
		field("usize64", r.UncompressedSize64, p.UncompressedSize64)
		field("content", r.SHA256, p.SHA256)
		return match
	}

	return DiffArchive(log, IndexZip(log, rebuilt, fix), IndexZip(log, posted, fix), check)
}
