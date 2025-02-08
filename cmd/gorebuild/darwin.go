// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"compress/zlib"
	"debug/macho"
	"encoding/binary"
	"encoding/xml"
	"io"
	"io/fs"
	"strings"
)

// StripDarwinSig parses data as a Mach-O executable, strips the macOS code signature from it,
// and returns the resulting Mach-O executable. It edits data directly, in addition to returning
// a shortened version.
// If data is not a Mach-O executable, StripDarwinSig silently returns it unaltered.
func StripDarwinSig(log *Log, name string, data []byte) []byte {
	// Binaries only expected in bin and pkg/tool.
	// This is an archive path, not a host file system path, so always forward slash.
	if !strings.Contains(name, "/bin/") && !strings.Contains(name, "/pkg/tool/") {
		return data
	}
	// Check 64-bit Mach-O magic before trying to parse, to keep log quiet.
	if len(data) < 4 || string(data[:4]) != "\xcf\xfa\xed\xfe" {
		return data
	}

	h, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		log.Printf("macho %s: %v", name, err)
		return data
	}
	if len(h.Loads) < 4 {
		log.Printf("macho %s: too few loads", name)
		return data
	}

	// at returns the uint32 at the given data offset.
	// If the offset is out of range, at returns 0.
	le := binary.LittleEndian
	at := func(off int) uint32 {
		if off < 0 || off+4 < 0 || off+4 > len(data) {
			log.Printf("macho %s: offset out of bounds", name)
			return 0
		}
		return le.Uint32(data[off : off+4])
	}

	// LC_CODE_SIGNATURE must be the last load.
	raw := h.Loads[len(h.Loads)-1].Raw()
	const LC_CODE_SIGNATURE = 0x1d
	if len(raw) != 16 || le.Uint32(raw[0:]) != LC_CODE_SIGNATURE || le.Uint32(raw[4:]) != 16 {
		// OK not to have a signature. No logging.
		return data
	}
	sigOff := le.Uint32(raw[8:])
	sigSize := le.Uint32(raw[12:])
	if int64(sigOff) >= int64(len(data)) {
		log.Printf("macho %s: invalid signature", name)
		return data
	}

	// Find __LINKEDIT segment (3rd or 4th load, usually).
	// Each load command has its size as the second uint32 of the command.
	// We maintain the offset in the file as we walk, since we need to edit
	// the loads later.
	off := 32
	load := 0
	for {
		if load >= len(h.Loads) {
			log.Printf("macho %s: cannot find __LINKEDIT", name)
			return data
		}
		lc64, ok := h.Loads[load].(*macho.Segment)
		if ok && lc64.Name == "__LINKEDIT" {
			break
		}
		off += int(at(off + 4))
		load++
	}
	if at(off) != uint32(macho.LoadCmdSegment64) {
		log.Printf("macho %s: confused finding __LINKEDIT", name)
		return data
	}
	linkOff := off + 4 + 4 + 16 + 8 // skip cmd, len, name, addr
	if linkOff < 0 || linkOff+32 < 0 || linkOff+32 > len(data) {
		log.Printf("macho %s: confused finding __LINKEDIT", name)
		return data
	}
	for ; load < len(h.Loads)-1; load++ {
		off += int(at(off + 4))
	}
	if off < 0 || off+16 < 0 || off+16 > len(data) {
		log.Printf("macho %s: confused finding signature load", name)
		return data
	}

	// Point of no return: edit data to strip signature.

	// Delete LC_CODE_SIGNATURE entry in load table
	le.PutUint32(data[16:], at(16)-1)  // ncmd--
	le.PutUint32(data[20:], at(20)-16) // cmdsz -= 16
	copy(data[off:], make([]byte, 16)) // clear LC_CODE_SIGNATURE

	// Update __LINKEDIT file and memory size to not include signature.
	//	filesz -= sigSize
	//	memsz = filesz
	// We can't do memsz -= sigSize because the Apple signer rounds memsz
	// to a page boundary. Go always sets memsz = filesz (unrounded).
	fileSize := le.Uint64(data[linkOff+16:]) - uint64(sigSize)
	le.PutUint64(data[linkOff:], fileSize)    // memsz
	le.PutUint64(data[linkOff+16:], fileSize) // filesize

	// Remove signature bytes at end of file.
	data = data[:sigOff]

	return data
}

// DiffDarwinPkg diffs the content of the macOS pkg and tgz files provided,
// logging differences. It returns true if the files were successfully parsed
// and contain the same files, false otherwise.
//
// The pkg file is expected to have paths beginning with ./usr/local/go instead of go.
// The pkg file is allowed to have an extra /etc/paths.d/go file.
func DiffDarwinPkg(log *Log, tgz, pkg []byte) bool {
	check := func(log *Log, rebuilt *TarFile, posted *CpioFile) bool {
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
		field("size", r.Size, p.Size)
		field("mode", fs.FileMode(r.Mode&0777), p.Mode)
		field("content", r.SHA256, p.SHA256)
		return match
	}

	return DiffArchive(log, IndexTarGz(log, tgz, nil), indexPkg(log, pkg, nil), check)
}

// indexPkg returns an index of the pkg file for comparison with a tgz file.
func indexPkg(log *Log, data []byte, fix Fixer) map[string]*CpioFile {
	payload := pkgPayload(log, data)
	if payload == nil {
		return nil
	}
	ix := IndexCpioGz(log, payload, fix)
	if ix == nil {
		return nil
	}

	// Delete ./etc/paths.d/go, which is not in the tgz,
	// and trim ./usr/local/go/ down to just go/.
	delete(ix, "./etc/paths.d/go")
	for name, f := range ix {
		if strings.HasPrefix(name, "./usr/local/") {
			delete(ix, name)
			name = strings.TrimPrefix(name, "./usr/local/")
			f.Name = name
			ix[name] = f
		}
	}
	return ix
}

// A minimal xar parser, enough to read macOS .pkg files.
// Package golang.org/x/build/internal/task also has one
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

// pkgPayload parses data as a macOS pkg file for the Go installer
// and returns the content of the file org.golang.go.pkg/Payload.
func pkgPayload(log *Log, data []byte) []byte {
	if len(data) < 28 || string(data[0:4]) != "xar!" {
		log.Printf("not an XAR file format (missing a 28+ byte header with 'xar!' magic number)")
		return nil
	}
	be := binary.BigEndian
	hdrSize := be.Uint16(data[4:])
	vers := be.Uint16(data[6:])
	tocCSize := be.Uint64(data[8:])
	tocUSize := be.Uint64(data[16:])

	if vers != 1 {
		log.Printf("bad xar version %d", vers)
		return nil
	}
	if int(hdrSize) >= len(data) || uint64(len(data))-uint64(hdrSize) < tocCSize {
		log.Printf("xar header bounds not in file")
		return nil
	}

	data = data[hdrSize:]
	chdr, data := data[:tocCSize], data[tocCSize:]

	// Header is zlib-compressed XML.
	zr, err := zlib.NewReader(bytes.NewReader(chdr))
	if err != nil {
		log.Printf("reading xar header: %v", err)
		return nil
	}
	defer zr.Close()
	hdrXML := make([]byte, tocUSize+1)
	n, err := io.ReadFull(zr, hdrXML)
	if uint64(n) != tocUSize {
		log.Printf("invalid xar header size %d", n)
		return nil
	}
	if err != io.ErrUnexpectedEOF {
		log.Printf("reading xar header: %v", err)
		return nil
	}
	hdrXML = hdrXML[:tocUSize]
	var hdr xarHeader
	if err := xml.Unmarshal(hdrXML, &hdr); err != nil {
		log.Printf("unmarshaling xar header: %v", err)
		return nil
	}

	// Walk TOC file tree to find org.golang.go.pkg/Payload.
	for _, f := range hdr.TOC.Files {
		if f.Name == "org.golang.go.pkg" && f.Type == "directory" {
			for _, f := range f.Files {
				if f.Name == "Payload" {
					if f.Type != "file" {
						log.Printf("bad xar payload type %s", f.Type)
						return nil
					}
					if f.Data.Encoding.Style != "application/octet-stream" {
						log.Printf("bad xar encoding %s", f.Data.Encoding.Style)
						return nil
					}
					if f.Data.Offset >= int64(len(data)) || f.Data.Size > int64(len(data))-f.Data.Offset {
						log.Printf("xar payload bounds not in file")
						return nil
					}
					return data[f.Data.Offset:][:f.Data.Size]
				}
			}
		}
	}
	log.Printf("payload not found")
	return nil
}
