// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
)

// ReadBinariesFromPKG reads pkg, the Go installer .pkg file, and returns
// binaries in bin and pkg/tool directories within GOROOT which we expect
// to have been signed by the macOS signing process.
//
// The map key is a relative path starting with "go/", like "go/bin/gofmt"
// or "go/pkg/tool/darwin_arm64/test2json". The map value holds its bytes.
func ReadBinariesFromPKG(pkg io.Reader) (map[string][]byte, error) {
	// Reading the whole file into memory isn't ideal, but it makes
	// the implementation of pkgPayload easier, and we only have at
	// most a few .pkg installers to process.
	data, err := io.ReadAll(pkg)
	if err != nil {
		return nil, err
	}
	payload, err := pkgPayload(data)
	if errors.Is(err, errNoXARHeader) && bytes.HasPrefix(data, []byte("I'm a PKG! -signed <macOS>\n")) {
		// This invalid XAR file is a fake installer produced by release tests.
		// Since its prefix indicates it was signed, return a fake signed go command binary.
		return map[string][]byte{"go/bin/go": []byte("fake go command -signed <macOS>")}, nil
	} else if err != nil {
		return nil, err
	}
	ix, err := indexCpioGz(payload)
	if err != nil {
		return nil, err
	}
	var binaries = make(map[string][]byte) // Relative path starting with "go/" → binary data.
	for nameWithinPayload, f := range ix {
		name, ok := strings.CutPrefix(nameWithinPayload, "./usr/local/") // Trim ./usr/local/go/ down to just go/.
		if !ok {
			continue
		}
		if !strings.HasPrefix(name, "go/bin/") && !strings.HasPrefix(name, "go/pkg/tool/") {
			continue
		}
		if !f.Mode.IsRegular() || f.Mode.Perm()&0100 == 0 {
			continue
		}
		binaries[name] = f.Data
	}
	return binaries, nil
}

// A minimal xar parser, enough to read macOS .pkg files.
// Command golang.org/x/build/cmd/gorebuild also has one
// for its internal needs.
//
// See https://en.wikipedia.org/wiki/Xar_(archiver)
// and https://github.com/mackyle/xar/wiki/xarformat.

// xarHeader is the main XML data structure for the xar header.
type xarHeader struct {
	XMLName xml.Name `xml:"xar"`
	TOC     xarTOC   `xml:"toc"`
}

// xarTOC is the table of contents.
type xarTOC struct {
	Files []*xarFile `xml:"file"`
}

// xarFile is a single file in the table of contents.
// Directories have Type "directory" and contain other files.
type xarFile struct {
	Data  xarFileData `xml:"data"`
	Name  string      `xml:"name"`
	Type  string      `xml:"type"` // "file", "directory"
	Files []*xarFile  `xml:"file"`
}

// xarFileData is the metadata describing a single file.
type xarFileData struct {
	Length   int64       `xml:"length"`
	Offset   int64       `xml:"offset"`
	Size     int64       `xml:"size"`
	Encoding xarEncoding `xml:"encoding"`
}

// xarEncoding has an attribute giving the encoding for a file's content.
type xarEncoding struct {
	Style string `xml:"style,attr"`
}

var errNoXARHeader = fmt.Errorf("not an XAR file format (missing a 28+ byte header with 'xar!' magic number)")

// pkgPayload parses data as a macOS pkg file for the Go installer
// and returns the content of the file org.golang.go.pkg/Payload.
func pkgPayload(data []byte) ([]byte, error) {
	if len(data) < 28 || string(data[0:4]) != "xar!" {
		return nil, errNoXARHeader
	}
	be := binary.BigEndian
	hdrSize := be.Uint16(data[4:])
	vers := be.Uint16(data[6:])
	tocCSize := be.Uint64(data[8:])
	tocUSize := be.Uint64(data[16:])

	if vers != 1 {
		return nil, fmt.Errorf("bad xar version %d", vers)
	}
	if int(hdrSize) >= len(data) || uint64(len(data))-uint64(hdrSize) < tocCSize {
		return nil, fmt.Errorf("xar header bounds not in file")
	}

	data = data[hdrSize:]
	chdr, data := data[:tocCSize], data[tocCSize:]

	// Header is zlib-compressed XML.
	zr, err := zlib.NewReader(bytes.NewReader(chdr))
	if err != nil {
		return nil, fmt.Errorf("reading xar header: %v", err)
	}
	defer zr.Close()
	hdrXML := make([]byte, tocUSize+1)
	n, err := io.ReadFull(zr, hdrXML)
	if uint64(n) != tocUSize {
		return nil, fmt.Errorf("invalid xar header size %d", n)
	}
	if err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("reading xar header: %v", err)
	}
	hdrXML = hdrXML[:tocUSize]
	var hdr xarHeader
	if err := xml.Unmarshal(hdrXML, &hdr); err != nil {
		return nil, fmt.Errorf("unmarshaling xar header: %v", err)
	}

	// Walk TOC file tree to find org.golang.go.pkg/Payload.
	for _, f := range hdr.TOC.Files {
		if f.Name == "org.golang.go.pkg" && f.Type == "directory" {
			for _, f := range f.Files {
				if f.Name == "Payload" {
					if f.Type != "file" {
						return nil, fmt.Errorf("bad xar payload type %s", f.Type)
					}
					if f.Data.Encoding.Style != "application/octet-stream" {
						return nil, fmt.Errorf("bad xar encoding %s", f.Data.Encoding.Style)
					}
					if f.Data.Offset >= int64(len(data)) || f.Data.Size > int64(len(data))-f.Data.Offset {
						return nil, fmt.Errorf("xar payload bounds not in file")
					}
					return data[f.Data.Offset:][:f.Data.Size], nil
				}
			}
		}
	}
	return nil, fmt.Errorf("payload not found")
}

// A cpioFile represents a single file in a CPIO archive.
type cpioFile struct {
	Name string
	Mode fs.FileMode
	Data []byte
}

// indexCpioGz parses data as a gzip-compressed cpio file and returns an index of its content.
func indexCpioGz(data []byte) (map[string]*cpioFile, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(zr)

	const hdrSize = 76

	ix := make(map[string]*cpioFile)
	hdr := make([]byte, hdrSize)
	for {
		_, err := io.ReadFull(br, hdr)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("reading archive: %v", err)
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
			return nil, fmt.Errorf("reading archive: malformed entry")
		}
		mode, _ := strconv.ParseInt(string(hdr[18:24]), 8, 64)
		nameLen, _ := strconv.ParseInt(string(hdr[59:65]), 8, 64)
		size, _ := strconv.ParseInt(string(hdr[65:76]), 8, 64)
		nameBuf := make([]byte, nameLen)
		if _, err := io.ReadFull(br, nameBuf); err != nil {
			return nil, fmt.Errorf("reading archive: %v", err)
		}
		if nameLen == 0 || nameBuf[nameLen-1] != 0 {
			return nil, fmt.Errorf("reading archive: malformed entry")
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
			return nil, fmt.Errorf("reading archive: %v", err)
		}
		if size != int64(len(data)) {
			return nil, fmt.Errorf("reading archive: short file")
		}

		if fmode&fs.ModeDir != 0 {
			continue
		}

		ix[name] = &cpioFile{name, fmode, data}
	}
	return ix, nil
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
