// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"

	"golang.org/x/build/dashboard"
)

func handleBuilders(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Builders map[string]*dashboard.BuildConfig
		Hosts    map[string]*dashboard.HostConfig
	}{dashboard.Builders, dashboard.Hosts}
	if r.FormValue("mode") == "json" {
		j, err := json.MarshalIndent(data, "", "\t")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(j)
	} else {
		var buf bytes.Buffer
		if err := buildersTmpl.Execute(&buf, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		buf.WriteTo(w)
	}
}

//go:embed templates/builders.html
var buildersTmplStr string

var buildersTmpl = template.Must(baseTmpl.New("builders").Funcs(template.FuncMap{
	"builderOwners": func(bc *dashboard.BuildConfig) template.HTML {
		owners := bc.HostConfig().Owners
		if len(owners) == 0 {
			return "golang-dev"
		}
		var buf strings.Builder
		for i, p := range owners {
			if i != 0 {
				buf.WriteString(", ")
			}
			if p.GitHub != "" {
				fmt.Fprintf(&buf, `<a href="https://github.com/%s">@%[1]s</a>`, html.EscapeString(p.GitHub))
			} else if len(p.Emails) > 0 {
				name := p.Name
				if name == "" {
					name = p.Emails[0]
				}
				fmt.Fprintf(&buf, `<a href="mailto:%s">%s</a>`, html.EscapeString(p.Emails[0]), html.EscapeString(name))
			} else if p.Name != "" {
				buf.WriteString(html.EscapeString(p.Name))
			} else {
				buf.WriteString("(unnamed)")
			}
		}
		return template.HTML(buf.String())
	},
}).Parse(buildersTmplStr))
