// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"math/bits"
	"os"
	"strconv"

	"golang.org/x/term"
)

func canColor() bool {
	if os.Getenv("TERM") == "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if !term.IsTerminal(1) {
		return false
	}
	return true
}

type colorizer struct {
	enabled bool
}

func newColorizer(enabled bool) *colorizer {
	return &colorizer{enabled}
}

type colorFlags uint64

const (
	colorBold      colorFlags = 1 << 1
	colorFgBlack              = 1 << 30
	colorFgRed                = 1 << 31
	colorFgGreen              = 1 << 32
	colorFgYellow             = 1 << 33
	colorFgBlue               = 1 << 34
	colorFgMagenta            = 1 << 35
	colorFgCyan               = 1 << 36
	colorFgWhite              = 1 << 37
)

func (c colorizer) color(s string, f colorFlags) string {
	if !c.enabled || f == 0 {
		return s
	}
	pfx := make([]byte, 0, 16)
	pfx = append(pfx, 0x1b, '[')
	for f != 0 {
		flag := uint64(bits.TrailingZeros64(uint64(f)))
		f &^= 1 << flag
		if len(pfx) > 2 {
			pfx = append(pfx, ';')
		}
		pfx = strconv.AppendUint(pfx, flag, 10)
	}
	pfx = append(pfx, 'm')
	return string(pfx) + s + "\x1b[0m"
}
