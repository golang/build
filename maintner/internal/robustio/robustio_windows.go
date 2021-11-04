// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package robustio

import (
	"errors"
	"syscall"
)

const errFileNotFound = syscall.ERROR_FILE_NOT_FOUND
const errSharingViolation syscall.Errno = 32 // Copied from internal/syscall/windows.ERROR_SHARING_VIOLATION

// isEphemeralError returns true if err may be resolved by waiting.
func isEphemeralError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ERROR_ACCESS_DENIED,
			syscall.ERROR_FILE_NOT_FOUND,
			errSharingViolation:
			return true
		}
	}
	return false
}
