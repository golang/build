// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gerrit

import (
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Auth is a Gerrit authentication mode.
// The most common ones are NoAuth or BasicAuth.
type Auth interface {
	setAuth(*Client, *http.Request)
}

// BasicAuth sends a username and password.
func BasicAuth(username, password string) Auth {
	return basicAuth{username, password}
}

type basicAuth struct {
	username, password string
}

func (ba basicAuth) setAuth(c *Client, r *http.Request) {
	r.SetBasicAuth(ba.username, ba.password)
}

// GitCookiesAuth derives the Gerrit authentication token from
// gitcookies based on the URL of the Gerrit request.
func GitCookiesAuth() Auth {
	return gitCookiesAuth{}
}

type gitCookiesAuth struct{}

func (gitCookiesAuth) setAuth(c *Client, r *http.Request) {
	url, err := url.Parse(c.url)
	if err != nil {
		// Something else will complain about this.
		return
	}
	host := url.Host

	// First look in Git's http.cookiefile, which is where Gerrit
	// now tells users to store this information.
	git := exec.Command("git", "config", "http.cookiefile")
	git.Stderr = os.Stderr
	gitOut, err := git.Output()
	if err != nil {
		log.Printf("git config http.cookiefile failed: %s", err)
		return
	}
	cookieFile := strings.TrimSpace(string(gitOut))
	if len(cookieFile) != 0 {
		// Load the cookie file.
		data, _ := ioutil.ReadFile(cookieFile)
		jar := parseGitCookies(string(data))
		cookies := jar.Cookies(url)
		if len(cookies) > 0 {
			for _, cookie := range cookies {
				r.AddCookie(cookie)
			}
			return
		}
	}

	// If not there, then look in $HOME/.netrc, which is where Gerrit
	// used to tell users to store the information, until the passwords
	// got so long that old versions of curl couldn't handle them.
	data, _ := ioutil.ReadFile(filepath.Join(os.Getenv("HOME"), ".netrc"))
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) >= 6 && f[0] == "machine" && f[1] == host && f[2] == "login" && f[4] == "password" {
			r.SetBasicAuth(f[3], f[5])
			return
		}
	}
}

func parseGitCookies(data string) *cookiejar.Jar {
	jar, _ := cookiejar.New(nil)
	for _, line := range strings.Split(data, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			continue
		}
		expires, err := strconv.ParseInt(f[4], 10, 64)
		if err != nil {
			continue
		}
		c := http.Cookie{
			Domain:  f[0],
			Path:    f[2],
			Secure:  f[3] == "TRUE",
			Expires: time.Unix(expires, 0),
			Name:    f[5],
			Value:   f[6],
		}
		// Construct a fake URL to add c to the jar.
		url := url.URL{
			Scheme: "http",
			Host:   c.Domain,
			Path:   c.Path,
		}
		jar.SetCookies(&url, []*http.Cookie{&c})
	}
	return jar
}

// NoAuth makes requests unauthenticated.
var NoAuth = noAuth{}

type noAuth struct{}

func (noAuth) setAuth(c *Client, r *http.Request) {}
