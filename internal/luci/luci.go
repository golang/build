// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package luci contains commonly needed LUCI helpers.
package luci

import (
	"fmt"
	"strings"
)

// PlatformToGoValues converts the swarming dimensions cipd_platform string into the
// corresponding runtime.GOOS and runtime.GOARCH values.
func PlatformToGoValues(platform string) (goos string, goarch string, err error) {
	goos, goarch, ok := strings.Cut(platform, "-")
	if !ok {
		return "", "", fmt.Errorf("cipd_platform not in proper format=%s", platform)
	}
	if goos == "Mac" || goos == "mac" {
		goos = "darwin"
	}
	if goarch == "armv6l" || goarch == "armv7l" {
		goarch = "arm"
	}
	return goos, goarch, nil
}
