// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	"golang.org/x/build/maintner/maintpb"
)

// The on-DiskMutationLogger format is as follows:
//
// The log is a stream of proto3-marshalled *maintpb.Mutation, spread
// over 1 or more files named maintner-YYYY-MM-DD.mutlog.  Each record
// begins with the variably-lengthed prefix "REC@XXX+YYY=" where the
// 0+ XXXX digits are the hex offset on disk (where the 'R' on disk is
// written) and the 0+ YYY digits are the hex length of the marshalled
// proto. After the YYY digits there is a '=' byte before the YYY bytes
// of proto. There is no record footer.
var (
	headerPrefix = []byte("REC@")
	headerSuffix = []byte("=")
	plus         = []byte("+")
)

// A MutationLogger logs mutations.
type MutationLogger interface {
	Log(*maintpb.Mutation) error
}

// DiskMutationLogger logs mutations to disk.
type DiskMutationLogger struct {
	directory string
	mu        sync.RWMutex
}

// NewDiskMutationLogger creates a new DiskMutationLogger, which will create
// mutations in the given directory.
func NewDiskMutationLogger(directory string) *DiskMutationLogger {
	if directory == "" {
		panic("empty directory")
	}
	return &DiskMutationLogger{directory: directory}
}

// filename returns the filename to write to. The oldest filename must come
// first in lexical order.
func (d *DiskMutationLogger) filename() string {
	now := time.Now().UTC()
	return filepath.Join(d.directory, fmt.Sprintf("maintner-%s.mutlog", now.Format("2006-01-02")))
}

// Log will write m to disk. If a mutation file does not exist for the current
// day, it will be created.
func (d *DiskMutationLogger) Log(m *maintpb.Mutation) error {
	data, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	f, err := os.OpenFile(d.filename(), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	off, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if off != st.Size() {
		return fmt.Errorf("Size %v != offset %v", st.Size(), off)
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "REC@%x+%x=", off, len(data))
	buf.Write(data)
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (d *DiskMutationLogger) GetMutations(ctx context.Context) <-chan *maintpb.Mutation {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ch := make(chan *maintpb.Mutation, 50) // buffered: overlap gunzip/unmarshal with loading
	if d.directory == "" {
		panic("empty directory")
	}
	go func() {
		// Walk guarantees that files are walked in lexical order, which we depend on.
		err := filepath.Walk(d.directory, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() && path != filepath.Clean(d.directory) {
				return filepath.SkipDir
			}
			if !strings.HasPrefix(fi.Name(), "maintner-") {
				return nil
			}
			if !strings.HasSuffix(fi.Name(), ".mutlog") {
				return nil
			}
			var off int64
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			br := bufio.NewReader(f)
			var buf bytes.Buffer
			for {
				startOff := off
				hdr, err := br.ReadSlice('=')
				if err != nil {
					if err == io.EOF && len(hdr) == 0 {
						return nil
					}
					return err
				}
				if len(hdr) > 40 {
					return fmt.Errorf("malformed overlong header %q... at %v, offset %v", hdr[:40], path, startOff)
				}
				if !bytes.HasPrefix(hdr, headerPrefix) || !bytes.HasSuffix(hdr, headerSuffix) || bytes.Count(hdr, plus) != 1 {
					return fmt.Errorf("malformed header %q at %v, offset %v", hdr, path, startOff)
				}
				plusPos := bytes.IndexByte(hdr, '+')
				hdrOff, err := strconv.ParseInt(string(hdr[len(headerPrefix):plusPos]), 16, 64)
				if err != nil {
					return fmt.Errorf("malformed header %q (malformed offset) at %v, offset %v", hdr, path, startOff)
				}
				if hdrOff != startOff {
					return fmt.Errorf("malformed header %q with offset %v doesn't match expected offset %v in %v", hdr, hdrOff, startOff, path)
				}
				hdrSize, err := strconv.ParseInt(string(hdr[plusPos+1:len(hdr)-1]), 16, 64)
				if err != nil {
					return fmt.Errorf("malformed header %q (bad size) at %v, offset %v", hdr, path, startOff)
				}
				off += int64(len(hdr))

				buf.Reset()
				if _, err := io.CopyN(&buf, br, hdrSize); err != nil {
					return fmt.Errorf("truncated record at offset %v: %v", startOff, err)
				}
				off += hdrSize

				m := new(maintpb.Mutation)
				if err := proto.Unmarshal(buf.Bytes(), m); err != nil {
					return err
				}
				select {
				case ch <- m:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})
		if err != nil {
			log.Printf("error walking directory %s: %v", d.directory, err)
		}
		close(ch)
	}()
	return ch
}
