// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package task implements tasks involved in making a Go release.
package task

import (
	"bytes"
	"context"
	"sync"
	"time"

	wf "golang.org/x/build/internal/workflow"
)

// Published holds information for a Go release
// that by this time has already been published.
//
// Published in this context refers to it being
// available for download at https://go.dev/dl/.
// It doesn't mean it has been announced by now;
// that step happens sometime after publication.
type Published struct {
	Version string        // Version that's published, in the same format as Go tags. For example, "go1.21rc1".
	Files   []WebsiteFile // Files that are published.
}

// CommunicationTasks combines communication tasks together.
type CommunicationTasks struct {
	AnnounceMailTasks
	SocialMediaTasks
}

var AwaitDivisor int = 1

// AwaitCondition calls the condition function every period until it returns
// true to indicate success, or an error. If the condition succeeds,
// AwaitCondition returns its result.
func AwaitCondition[T any](ctx *wf.TaskContext, period time.Duration, condition func() (T, bool, error)) (T, error) {
	pollTimer := time.NewTicker(period / time.Duration(AwaitDivisor))
	defer pollTimer.Stop()
	for {
		res, done, err := condition()
		if done || err != nil {
			return res, err
		}
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-pollTimer.C:
			ctx.ResetWatchdog()
		}
	}
}

// LogWriter is an io.Writer that writes to a workflow task's log, flushing
// its buffer periodically to avoid too many writes.
type LogWriter struct {
	Logger wf.Logger

	flushTicker *time.Ticker

	mu  sync.Mutex
	buf []byte
}

func (w *LogWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, b...)
	if len(w.buf) > 1<<20 {
		w.flushLocked(false)
		w.flushTicker.Reset(10 * time.Second)
	}
	return len(b), nil
}

func (w *LogWriter) flush(force bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked(force)
}

func (w *LogWriter) flushLocked(force bool) {
	if len(w.buf) == 0 {
		return
	}
	log, rest := w.buf, []byte(nil)
	if !force {
		nl := bytes.LastIndexByte(w.buf, '\n')
		if nl == -1 {
			return
		}
		log, rest = w.buf[:nl], w.buf[nl+1:]
	}
	w.Logger.Printf("\n%s", string(log))
	w.buf = append([]byte(nil), rest...) // don't leak
}

func (w *LogWriter) Run(ctx context.Context) {
	w.flushTicker = time.NewTicker(10 * time.Second)
	defer w.flushTicker.Stop()
	for {
		select {
		case <-w.flushTicker.C:
			w.flush(false)
		case <-ctx.Done():
			w.flush(true)
			return
		}
	}
}

// WebsiteFile represents a file on the go.dev downloads page.
// It should be kept in sync with the download code in x/website/internal/dl.
type WebsiteFile struct {
	Filename       string `json:"filename"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	Version        string `json:"version"`
	ChecksumSHA256 string `json:"sha256"`
	Size           int64  `json:"size"`
	Kind           string `json:"kind"` // "archive", "installer", "source"
}

func (f WebsiteFile) GOARCH() string {
	if f.OS == "linux" && f.Arch == "armv6l" {
		return "arm"
	}
	return f.Arch
}

type WebsiteRelease struct {
	Version        string        `json:"version"`
	Stable         bool          `json:"stable"`
	Files          []WebsiteFile `json:"files"`
	Visible        bool          `json:"-"` // show files on page load
	SplitPortTable bool          `json:"-"` // whether files should be split by primary/other ports.
}
