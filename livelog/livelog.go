// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package livelog provides a buffer that can be simultaneously written to by
// one writer and read from by many readers.
package livelog

import (
	"io"
	"sync"
)

const (
	// MaxBufferSize is the maximum buffer size, as it is more output than
	// we expect from reasonable tests.
	MaxBufferSize = 2 << 20 // 2 MB

	// truncationMessage is added to the end of the log when it reaches the
	// maximum size.
	truncationMessage = "\n\n... log truncated ..."

	// maxUserSize is the total user output we can place in the buffer
	// while still leaving room for the truncation message.
	maxUserSize = MaxBufferSize - len(truncationMessage)
)

// Buffer is an io.WriteCloser that provides multiple Readers that each yield
// the same data.
//
// It is safe to Write to a Buffer while Readers consume data. A Buffer has a
// maximum size of MaxBufferSize, after which Write will silently drop
// additional data and the buffer will contain a truncation note at the end.
//
// The zero value is a ready-to-use buffer.
type Buffer struct {
	mu     sync.Mutex // Guards the fields below.
	wake   *sync.Cond // Created on demand by reader.
	buf    []byte // Length is in the range [0, MaxBufferSize].
	eof    bool
	lastID int
}

// Write appends data to the Buffer.
// It will wake any blocked Readers.
func (b *Buffer) Write(b2 []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	needTrunc := false
	b2len := len(b2)
	if len(b.buf) == MaxBufferSize {
		// b.buf is full and truncationMessage was written.
		b2 = nil
	} else if len(b.buf)+b2len > maxUserSize {
		b2 = b2[:maxUserSize-len(b.buf)]
		needTrunc = true
		// After this write, b.buf will reach MaxBufferSize length.
	}
	b.buf = append(b.buf, b2...)
	if needTrunc {
		b.buf = append(b.buf, []byte(truncationMessage)...)
	}
	b.wakeReaders()
	return b2len, nil
}

// Close signals EOF to all Readers.
func (b *Buffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.eof = true
	b.wakeReaders()
	return nil
}

// wakeReaders wakes any sleeping readers.
// b.mu must be held when calling.
func (b *Buffer) wakeReaders() {
	if b.wake != nil {
		b.wake.Broadcast()
	}
}

// Bytes returns a copy of the underlying buffer.
func (b *Buffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]byte(nil), b.buf...)
}

// String returns a copy of the underlying buffer as a string.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return string(b.buf)
}

// Reader initializes and returns a ReadCloser that will emit the entire buffer.
// It is safe to call Read and Close concurrently.
func (b *Buffer) Reader() io.ReadCloser {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lastID++
	return &reader{buf: b, id: b.lastID}
}

type reader struct {
	buf    *Buffer
	id     int  // Read-only.
	read   int  // Bytes read; accessed by only the Read method.
	closed bool // Guarded by buf.mu.
}

func (r *reader) Read(b []byte) (int, error) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()

	// Wait for data or writer EOF or reader closed.
	for len(r.buf.buf) == r.read && !r.buf.eof && !r.closed {
		if r.buf.wake == nil {
			r.buf.wake = sync.NewCond(&r.buf.mu)
		}
		r.buf.wake.Wait()
	}
	// Return EOF if writer reported EOF or this reader is closed.
	if (len(r.buf.buf) == r.read && r.buf.eof) || r.closed {
		return 0, io.EOF
	}
	// Emit some data.
	n := copy(b, r.buf.buf[r.read:])
	r.read += n
	return n, nil
}

func (r *reader) Close() error {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()

	r.closed = true

	// Wake any sleeping readers to unblock a pending read on this reader.
	// (For other open readers this will be a no-op.)
	r.buf.wakeReaders()

	return nil
}
