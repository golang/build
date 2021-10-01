// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package envutil provides utilities for working with environment variables.
package envutil

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Dedup returns a copy of env with any duplicates removed, in favor of
// later values.
// Items are expected to be on the normal environment "key=value" form.
//
// Keys are interpreted as if on the given GOOS.
// (On Windows, key comparison is case-insensitive.)
func Dedup(goos string, env []string) []string {
	caseInsensitive := (goos == "windows")

	// Construct the output in reverse order, to preserve the
	// last occurrence of each key.
	saw := map[string]bool{}
	out := make([]string, 0, len(env))
	for n := len(env); n > 0; n-- {
		kv := env[n-1]

		k, _ := Split(kv)
		if caseInsensitive {
			k = strings.ToLower(k)
		}
		if saw[k] {
			continue
		}

		saw[k] = true
		out = append(out, kv)
	}

	// Now reverse the slice to restore the original order.
	for i := 0; i < len(out)/2; i++ {
		j := len(out) - i - 1
		out[i], out[j] = out[j], out[i]
	}

	return out
}

// Get returns the value of key in env, interpreted according to goos.
func Get(goos string, env []string, key string) string {
	for n := len(env); n > 0; n-- {
		kv := env[n-1]
		if v, ok := Match(goos, kv, key); ok {
			return v
		}
	}
	return ""
}

// Match checks whether a "key=value" string matches key and, if so,
// returns the value.
//
// On Windows, the key comparison is case-insensitive.
func Match(goos, kv, key string) (value string, ok bool) {
	if len(kv) <= len(key) || kv[len(key)] != '=' {
		return "", false
	}

	if goos == "windows" {
		// Case insensitive.
		if !strings.EqualFold(kv[:len(key)], key) {
			return "", false
		}
	} else {
		// Case sensitive.
		if kv[:len(key)] != key {
			return "", false
		}
	}

	return kv[len(key)+1:], true
}

// Split splits a "key=value" string into a key and value.
func Split(kv string) (key, value string) {
	parts := strings.SplitN(kv, "=", 2)
	if len(parts) < 2 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// SetDir sets cmd.Dir to dir, and also updates cmd.Env to ensure that PWD matches.
//
// If dir is the empty string, SetDir clears cmd.Dir and sets PWD to the current
// working directory.
func SetDir(cmd *exec.Cmd, dir string) {
	if dir == "" {
		cmd.Dir = ""
		dir, _ = os.Getwd()
	} else {
		cmd.Dir = dir
	}
	SetEnv(cmd, "PWD="+dir)
}

// SetEnv sets cmd.Env to include the given key=value pairs,
// removing any duplicates for the key and leaving all other keys unchanged.
//
// (Removing duplicates is not strictly necessary with modern versions of the Go
// standard library, but causes less confusion if cmd.Env is written to a log —
// as is sometimes done in packages within this module.)
func SetEnv(cmd *exec.Cmd, kv ...string) {
	if len(kv) == 0 {
		return
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = Dedup(runtime.GOOS, append(env, kv...))
}
