// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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
func HandleStatusFunc(pool interface{ List() []*remote.Session }) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		type Instance struct {
			Name    string
			Created time.Duration
			Expires time.Duration
		}
		var instances []Instance
		sessions := pool.List()
		for _, s := range sessions {
			instances = append(instances, Instance{
				Name:    html.EscapeString(s.ID),
				Created: time.Since(s.Created),
				Expires: time.Until(s.Expires),
			})
		}
		statusHTMLTmpl.Execute(w, instances)
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
