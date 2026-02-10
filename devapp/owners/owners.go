// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package owners

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/build/repos"
)

type Owner struct {
	// GitHubUsername is a GitHub user name or team name.
	GitHubUsername string `json:"githubUsername"`
	GerritEmail    string `json:"gerritEmail"`
}

type Entry struct {
	Primary   []Owner `json:"primary"`
	Secondary []Owner `json:"secondary,omitempty"`
}

type displayEntry struct {
	Primary   []Owner
	Secondary []Owner
	GerritURL string
}

type Request struct {
	Payload struct {
		// Paths is a set of relative paths rooted at go.googlesource.com,
		// where the first path component refers to the repository name,
		// while the rest refers to a path within that repository.
		//
		// For instance, a path like go/src/runtime/trace/trace.go refers
		// to the repository at go.googlesource.com/go, and the path
		// src/runtime/trace/trace.go within that repository.
		//
		// A request with Paths set will return the owner entry
		// for the deepest part of each path that it has information
		// on.
		//
		// For example, the path go/src/runtime/trace/trace.go will
		// match go/src/runtime/trace if there exist entries for both
		// go/src/runtime and go/src/runtime/trace.
		//
		// Must be empty if All is true.
		Paths []string `json:"paths"`

		// All indicates that the response must contain every available
		// entry about code owners.
		//
		// If All is true, Paths must be empty.
		All bool `json:"all"`

		// Platform indicates that the response should contain all platform
		// owners entries.
		Platform bool `json:"platform"`
	} `json:"payload"`
	Version int `json:"v"` // API version
}

type Response struct {
	Payload struct {
		Entries   map[string]*Entry `json:"entries"`   // paths in request -> Entry
		Platforms map[string]*Entry `json:"platforms"` // platforms (GOOS or GOARCH) -> Entry
	} `json:"payload"`
	Error string `json:"error,omitempty"`
}

// match takes a path consisting of the repo name and full path of a file or
// directory within that repo and returns the deepest Entry match in the file
// hierarchy for the given resource.
func match(path string) *Entry {
	var deepestPath string
	for p := range entries {
		if hasPathPrefix(path, p) && len(p) > len(deepestPath) {
			deepestPath = p
		}
	}
	return entries[deepestPath]
}

// hasPathPrefix reports whether the slash-separated path s
// begins with the elements in prefix.
//
// Copied from go/src/cmd/go/internal/str.HasPathPrefix.
func hasPathPrefix(s, prefix string) bool {
	if len(s) == len(prefix) {
		return s == prefix
	}
	if prefix == "" {
		return true
	}
	if len(s) > len(prefix) {
		if prefix[len(prefix)-1] == '/' || s[len(prefix)] == '/' {
			return s[:len(prefix)] == prefix
		}
	}
	return false
}

// Handler takes one or more paths and returns a map of each to a matching
// Entry struct. If no Entry is matched for the path, the value for the key
// is nil.
func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "GET":
		serveIndex(w, r)
		return
	case "POST":
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "unable to decode request", http.StatusBadRequest)
			// TODO: increment expvar for monitoring.
			log.Printf("unable to decode owners request: %v", err)
			return
		}

		if len(req.Payload.Paths) > 0 && req.Payload.All {
			jsonError(w, "paths must be empty when all is true", http.StatusBadRequest)
			// TODO: increment expvar for monitoring.
			log.Printf("invalid request: paths is non-empty but all is true")
			return
		}

		var resp Response
		if req.Payload.All {
			resp.Payload.Entries = entries
			resp.Payload.Platforms = archOses
		} else {
			resp.Payload.Entries = make(map[string]*Entry)
			for _, p := range req.Payload.Paths {
				resp.Payload.Entries[p] = match(p)
			}
			if req.Payload.Platform {
				resp.Payload.Platforms = archOses
			}
		}
		// resp.Payload.Entries and resp.Payload.Platforms must not be mutated because they
		// contain references to the global "entries" and "archOses" values.

		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(resp); err != nil {
			jsonError(w, "unable to encode response", http.StatusInternalServerError)
			// TODO: increment expvar for monitoring.
			log.Printf("unable to encode owners response: %v", err)
			return
		}
		w.Write(buf.Bytes())
	case "OPTIONS":
		// Likely a CORS preflight request; leave resp.Payload empty.
	default:
		jsonError(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
}

func jsonError(w http.ResponseWriter, text string, code int) {
	w.WriteHeader(code)
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(Response{Error: text}); err != nil {
		// TODO: increment expvar for monitoring.
		log.Printf("unable to encode error response: %v", err)
		return
	}
	w.Write(buf.Bytes())
}

// TranslatePathForIssues takes a path for a package based on go.googlesource.com
// and translates it into a form that aligns more closely with the issue
// tracker.
//
// Specifically, Go standard library packages lose the go/src prefix,
// repositories with a golang.org/x/ import path get the x/ prefix,
// and all other paths are left as-is (this includes e.g. domains).
func TranslatePathForIssues(path string) string {
	// Check if it's in the standard library, in which case,
	// drop the prefix.
	if strings.HasPrefix(path, "go/src/") {
		return path[len("go/src/"):]
	}

	// Check if it's some other path in the main repo, in which case,
	// drop the go/ prefix.
	if strings.HasPrefix(path, "go/") {
		return path[len("go/"):]
	}

	// Check if it's a golang.org/x/ repository, and if so add an x/ prefix.
	firstComponent := path
	i := strings.IndexRune(path, '/')
	if i > 0 {
		firstComponent = path[:i]
	}
	if _, ok := repos.ByImportPath["golang.org/x/"+firstComponent]; ok {
		return "x/" + path
	}

	// None of the above was true, so just leave it untouched.
	return path
}

// formatEntries returns an entries map adjusted for better readability on
// https://dev.golang.org/owners.
func formatEntries(entries map[string]*Entry) (map[string]*displayEntry, error) {
	tm := make(map[string]*displayEntry)
	for path, entry := range entries {
		tPath := TranslatePathForIssues(path)
		if _, ok := tm[tPath]; ok {
			return nil, fmt.Errorf("path translation of %q creates a duplicate entry %q", path, tPath)
		}
		tm[tPath] = &displayEntry{
			Primary:   entry.Primary,
			Secondary: entry.Secondary,
			GerritURL: gerritURL(path, tPath),
		}
	}
	return tm, nil
}

func gerritURL(path, tPath string) string {
	var project string
	var dir string
	if strings.HasPrefix(path, "go/") {
		project = "go"
		dir = tPath
	} else if strings.HasPrefix(tPath, "x/") {
		parts := strings.SplitN(tPath, "/", 3)
		project = parts[1]
		if len(parts) == 3 {
			dir = parts[2]
		}
	} else {
		return ""
	}
	url := "https://go-review.googlesource.com/q/project:" + project
	if dir != "" {
		url += "+dir:" + dir
	}
	return url
}

// ownerData is passed to the Template, which produces two tables.
type ownerData struct {
	Paths    map[string]*displayEntry
	ArchOSes map[string]*displayEntry
}

func serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	indexCache.once.Do(func() {
		paths, err := formatEntries(entries)
		if err != nil {
			indexCache.err = err
			return
		}

		archOses, err := formatEntries(archOses)
		if err != nil {
			indexCache.err = err
			return
		}

		displayEntries := ownerData{paths, archOses}

		var buf bytes.Buffer
		indexCache.err = indexTmpl.Execute(&buf, displayEntries)
		indexCache.html = buf.Bytes()
	})
	if indexCache.err != nil {
		log.Printf("unable to serve index page HTML: %v", indexCache.err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Write(indexCache.html)
}

// indexCache is a cache of the owners index page HTML.
//
// As long as the owners are defined at package initialization time
// and not modified at runtime, the HTML doesn't change per request.
var indexCache struct {
	once sync.Once
	html []byte // Page HTML rendered by indexTmpl.
	err  error
}

var indexTmpl = template.Must(template.New("index").Funcs(template.FuncMap{
	"githubURL": func(githubUsername string) string {
		if org, team, ok := strings.Cut(githubUsername, "/"); ok {
			// A GitHub team like "{org}/{team}".
			return "https://github.com/orgs/" + org + "/teams/" + team
		}
		return "https://github.com/" + githubUsername
	},
}).Parse(`<!DOCTYPE html>
<html lang="en">
<title>Go Code Owners</title>
<meta name=viewport content="width=device-width, initial-scale=1">
<style>
* {
	box-sizing: border-box;
	margin: 0;
	padding: 0;
}
body {
	font-family: sans-serif;
	margin: 1rem 1.5rem;
}
.header {
	color: #666;
	font-size: 90%;
	margin-bottom: 1rem;
}
.table-header {
	font-weight: bold;
	position: sticky;
	top: 0;
}
.table-header,
.entry {
	background-color: #fff;
	border-bottom: 1px solid #ddd;
	display: flex;
	flex-wrap: wrap;
	justify-content: space-between;
	margin: .15rem 0;
	padding: .15rem 0;
}
.path,
.primary,
.secondary {
	flex-basis: 33.3%;
}
</style>
<header class="header">
	<p>Reviews are automatically assigned to primary owners.</p>
	<p>Alter these entries at
	<a href="https://go.dev/cs/x/build/+/HEAD:devapp/owners/">golang.org/x/build/devapp/owners</a>.</p>
</header>
<main>
<div class="table-header">
	<span class="path">Path</span>
	<span class="primary">Primaries</span>
	<span class="secondary">Secondaries</span>
</div>
{{range $path, $entry := .Paths}}
	<div class="entry">
		<span class="path">
			{{if $entry.GerritURL}}<a href="{{$entry.GerritURL}}" target="_blank" rel="noopener">{{end}}
			{{$path}}
			{{if $entry.GerritURL}}</a>{{end}}
		</span>
		<span class="primary">
			{{range .Primary}}
				<a href="{{githubURL .GitHubUsername}}" target="_blank" rel="noopener">@{{.GitHubUsername}}</a>
			{{end}}
		</span>
		<span class="secondary">
			{{range .Secondary}}
				<a href="{{githubURL .GitHubUsername}}" target="_blank" rel="noopener">@{{.GitHubUsername}}</a>
			{{end}}
		</span>
	</div>
{{end}}
<div class="table-header">
	<span class="path">Arch/OS</span>
	<span class="primary">Primaries</span>
	<span class="secondary">Secondaries</span>
</div>
{{range $path, $entry := .ArchOSes}}
	<div class="entry">
		<span class="path">{{$path}}</span>
		<span class="primary">
			{{range .Primary}}
				<a href="{{githubURL .GitHubUsername}}" target="_blank" rel="noopener">@{{.GitHubUsername}}</a>
			{{end}}
		</span>
		<span class="secondary">
			{{range .Secondary}}
				<a href="{{githubURL .GitHubUsername}}" target="_blank" rel="noopener">@{{.GitHubUsername}}</a>
			{{end}}
		</span>
	</div>
{{end}}
</main>
`))
