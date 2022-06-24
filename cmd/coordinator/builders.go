// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16 && (linux || darwin)
// +build go1.16
// +build linux darwin

package main

import (
	"bytes"
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
}).Parse(`
<!DOCTYPE html>
<html>
<head><link rel="stylesheet" href="/style.css"/><title>Go Farmer</title></head>
<body>
{{template "build-header"}}

<h2 id='builders'>Defined Builders</h2>

<table>
<thead><tr><th>name</th><th>pool</th><th>owners</th><th>known issue</th><th>notes</th></tr>
</thead>
{{range .Builders}}
<tr>
	<td>{{.Name}}</td>
	<td><a href='#{{.HostType}}'>{{.HostType}}</a></td>
	<td>{{builderOwners .}}</td>
	<td>{{range $i, $issue := .KnownIssues}}{{if ne $i 0}}, {{end}}<a href="https://go.dev/issue/{{$issue}}" title="This builder has a known issue. See: go.dev/issue/{{$issue}}.">#{{$issue}}</a>{{end}}</td>
	<td>{{.Notes}}</td>
</tr>
{{end}}
</table>

<h2 id='hosts'>Defined Host Types (pools)</h2>

<table>
<thead><tr><th>name</th><th>type</th><th>notes</th></tr>
</thead>
{{range .Hosts}}
<tr id='{{.HostType}}'>
	<td>{{.HostType}}</td>
	<td>{{.PoolName}}</td>
	<td>{{.Notes}}</td>
</tr>
{{end}}
</table>


</body>
</html>
`))
