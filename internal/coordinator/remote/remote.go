// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16 && (linux || darwin)
// +build go1.16
// +build linux darwin

package remote

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
)

var (
	remoteBuildlets = struct {
		sync.Mutex
		m map[string]*remoteBuildlet // keyed by buildletName
	}{m: map[string]*remoteBuildlet{}}

	cleanTimer *time.Timer
)

const (
	remoteBuildletIdleTimeout   = 30 * time.Minute
	remoteBuildletCleanInterval = time.Minute
)

func init() {
	cleanTimer = time.AfterFunc(remoteBuildletCleanInterval, expireBuildlets)
}

type remoteBuildlet struct {
	User        string // "user-foo" build key
	Name        string // dup of key
	HostType    string
	BuilderType string // default builder config to use if not overwritten
	Created     time.Time
	Expires     time.Time

	buildlet *buildlet.Client
}

// renew renews rb's idle timeout if ctx hasn't expired.
// renew should run in its own goroutine.
func (rb *remoteBuildlet) renew(ctx context.Context) {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	select {
	case <-ctx.Done():
		return
	default:
	}
	if got := remoteBuildlets.m[rb.Name]; got == rb {
		rb.Expires = time.Now().Add(remoteBuildletIdleTimeout)
		time.AfterFunc(time.Minute, func() { rb.renew(ctx) })
	}
}

func addRemoteBuildlet(rb *remoteBuildlet) (name string) {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	n := 0
	for {
		name = fmt.Sprintf("%s-%s-%d", rb.User, rb.BuilderType, n)
		if _, ok := remoteBuildlets.m[name]; ok {
			n++
		} else {
			remoteBuildlets.m[name] = rb
			return name
		}
	}
}

func isGCERemoteBuildlet(instName string) bool {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	for _, rb := range remoteBuildlets.m {
		if rb.buildlet.GCEInstanceName() == instName {
			return true
		}
	}
	return false
}

func expireBuildlets() {
	defer cleanTimer.Reset(remoteBuildletCleanInterval)
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	now := time.Now()
	for name, rb := range remoteBuildlets.m {
		if !rb.Expires.IsZero() && rb.Expires.Before(now) {
			go rb.buildlet.Close()
			delete(remoteBuildlets.m, name)
		}
	}
}

var timeNow = time.Now // for testing

type byBuildletName []*remoteBuildlet

func (s byBuildletName) Len() int           { return len(s) }
func (s byBuildletName) Less(i, j int) bool { return s[i].Name < s[j].Name }
func (s byBuildletName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func remoteBuildletStatus() string {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()

	if len(remoteBuildlets.m) == 0 {
		return "<i>(none)</i>"
	}

	var buf bytes.Buffer
	var all []*remoteBuildlet
	for _, rb := range remoteBuildlets.m {
		all = append(all, rb)
	}
	sort.Sort(byBuildletName(all))

	buf.WriteString("<ul>")
	for _, rb := range all {
		fmt.Fprintf(&buf, "<li><b>%s</b>, created %v ago, expires in %v</li>\n",
			html.EscapeString(rb.Name),
			time.Since(rb.Created), rb.Expires.Sub(time.Now()))
	}
	buf.WriteString("</ul>")

	return buf.String()
}

// userFromGomoteInstanceName returns the username part of a gomote
// remote instance name.
//
// The instance name is of two forms. The normal form is:
//
//     user-bradfitz-linux-amd64-0
//
// The overloaded form to convey that the user accepts responsibility
// for changes to the underlying host is to prefix the same instance
// name with the string "mutable-", such as:
//
//     mutable-user-bradfitz-darwin-amd64-10_8-0
//
// The mutable part is ignored by this function.
func userFromGomoteInstanceName(name string) string {
	name = strings.TrimPrefix(name, "mutable-")
	if !strings.HasPrefix(name, "user-") {
		return ""
	}
	user := name[len("user-"):]
	hyphen := strings.IndexByte(user, '-')
	if hyphen == -1 {
		return ""
	}
	return user[:hyphen]
}
