// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package ui

import (
	"bytes"
	_ "embed"
	"html"
	"html/template"
	"net/http"
	"time"

	"golang.org/x/build/internal/coordinator/remote"
)

var (
	processStartTime = time.Now()
	statusHTMLTmpl   = template.Must(template.New("statusHTML").Parse(string(statusHTML)))
)

// HandleStatusFunc gives a HTTP handler which can report the status of the instances
// in the session pool.
func HandleStatusFunc(pool interface{ List() []*remote.Session }, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		type Instance struct {
			Name           string
			Created        time.Duration
			Expires        time.Duration
			SwarmingTaskID string
		}
		type Status struct {
			Instances []Instance
			Version   string
		}
		var instances []Instance
		sessions := pool.List()
		for _, s := range sessions {
			instances = append(instances, Instance{
				Name:           html.EscapeString(s.ID),
				Created:        time.Since(s.Created).Truncate(time.Second), // Truncate the duration to seconds to make it more readable.
				Expires:        time.Until(s.Expires).Truncate(time.Second), // Truncate the duration to seconds to make it more readable.
				SwarmingTaskID: s.SwarmingTaskID,
			})
		}
		statusHTMLTmpl.Execute(w, Status{
			Instances: instances,
			Version:   version,
		})
	}
}

//go:embed style.css
var styleCSS []byte

//go:embed status.html
var statusHTML []byte

// HandleStyleCSS responds with the CSS code.
func HandleStyleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, max-age=0")
	http.ServeContent(w, r, "style.css", processStartTime, bytes.NewReader(styleCSS))
}

// Redirect redirects requests from the source host to the destination host if the host
// matches the source host. If the host does not match the source host, then the request
// will be passed to the passed in handler.
func Redirect(hf http.HandlerFunc, srcHost, dstHost string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Host == srcHost {
			http.Redirect(w, r, "https://"+dstHost, http.StatusSeeOther)
			return
		}
		hf(w, r)
	}
}
