// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package devapp

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"golang.org/x/build/godash"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/user"
)

func findEmail(ctx context.Context, data *godash.Data) string {
	u := user.Current(ctx)

	if u != nil {
		return data.Reviewers.Preferred(u.Email)
	}
	return ""
}

func showDash(w http.ResponseWriter, req *http.Request) {
	ctx := appengine.NewContext(req)
	req.ParseForm()

	var cache Cache
	if err := datastore.Get(ctx, datastore.NewKey(ctx, entityPrefix+"Cache", "data", 0, nil), &cache); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	data := &godash.Data{Reviewers: &godash.Reviewers{}}
	if err := gob.NewDecoder(bytes.NewBuffer(cache.Value)).Decode(data); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Load information about logged-in user.
	var d display
	d.email = findEmail(ctx, data)
	d.data = data
	d.activeMilestones = data.GetActiveMilestones()
	// TODO(quentin): Load the user's preferences into d.pref.

	tmpl, err := ioutil.ReadFile("template/dash.html")
	if err != nil {
		log.Errorf(ctx, "reading template: %v", err)
		return
	}
	t, err := template.New("main").Funcs(template.FuncMap{
		"css":     d.css,
		"join":    d.join,
		"mine":    d.mine,
		"muted":   d.muted,
		"old":     d.old,
		"replace": strings.Replace,
		"second":  d.second,
		"short":   d.short,
		"since":   d.since,
		"ghemail": d.ghemail,
		"release": d.release,
	}).Parse(string(tmpl))
	if err != nil {
		log.Errorf(ctx, "parsing template: %v", err)
		return
	}

	groups := data.GroupData(true, true)

	var filtered []*godash.Group
	for _, group := range groups {
		if group.Dir == "closed" || group.Dir == "proposal" {
			continue
		}
		filtered = append(filtered, group)
	}

	login, err := user.LoginURL(ctx, "/dash")
	if err != nil {
		http.Error(w, err.Error(), 500)
	}
	logout, err := user.LogoutURL(ctx, "/dash")
	if err != nil {
		http.Error(w, err.Error(), 500)
	}

	tData := struct {
		User          string
		Now           string
		Login, Logout string
		Dirs          []*godash.Group
	}{
		d.email,
		data.Now.UTC().Format(time.UnixDate),
		login, logout,
		filtered,
	}

	if err := t.Execute(w, tData); err != nil {
		log.Errorf(ctx, "execute: %v", err)
		http.Error(w, "error executing template", 500)
		return
	}
}

// display holds state needed to compute the displayed HTML.
// The methods here are turned into functions for the template to call.
// Not all methods need the display state; being methods just keeps
// them all in one place.
type display struct {
	email            string
	data             *godash.Data
	activeMilestones []string
	pref             UserPref
}

// short returns a shortened email address by removing @domain.
// Input can be string or []string; output is same.
func (d *display) short(s interface{}) interface{} {
	switch s := s.(type) {
	case string:
		return d.data.Reviewers.Shorten(s)
	case []string:
		v := make([]string, len(s))
		for i, t := range s {
			v[i] = d.short(t).(string)
		}
		return v
	default:
		return s
	}
	return s
}

// css returns name if cond is true; otherwise it returns the empty string.
// It is intended for use in generating css class names (or not).
func (d *display) css(name string, cond bool) string {
	if cond {
		return name
	}
	return ""
}

// old returns css class "old" t is too long ago.
func (d *display) old(t time.Time) string {
	return d.css("old", time.Since(t) > 7*24*time.Hour)
}

// join is like strings.Join but takes arguments in the reverse order,
// enabling {{list | join ","}}.
func (d *display) join(sep string, list []string) string {
	return strings.Join(list, sep)
}

// since returns the elapsed time since t as a number of days.
func (d *display) since(t time.Time) string {
	// NOTE(rsc): Considered changing the unit (hours, days, weeks)
	// but that made it harder to scan through the table.
	// If it's always days, that's one less thing you have to read.
	// Otherwise 1 week might be misread as worse than 6 hours.
	dt := time.Since(t)
	return fmt.Sprintf("%.1f days ago", float64(dt)/float64(24*time.Hour))
}

// second returns the css class "second" if the index is non-zero
// (so really "second" here means "not first").
func (d *display) second(index int) string {
	return d.css("second", index > 0)
}

// mine returns the css class "mine" if the email address is the logged-in user.
// It also returns "unassigned" for an unassigned reviewer.
func (d *display) mine(email string) string {
	if long := d.data.Reviewers.Resolve(email); long != "" {
		email = long
	}
	if d.data.Reviewers.Preferred(email) == d.email {
		return "mine"
	}
	if email == "" {
		return "unassigned"
	}
	return ""
}

// ghemail converts a GitHub login name into an e-mail address, or
// "@username" if the e-mail address is unknown.
func (d *display) ghemail(login string) string {
	if login == "" {
		return login
	}
	if addr := d.data.Reviewers.ResolveGitHub(login); addr != "" {
		return addr
	}
	return "@" + login
}

// muted returns the css class "muted" if the directory is muted.
func (d *display) muted(dir string) string {
	for _, m := range d.pref.Muted {
		if m == dir {
			return "muted"
		}
	}
	return ""
}

// release returns the css class "release" if the issue is related to the release.
func (d *display) release(milestone string) string {
	for _, m := range d.activeMilestones {
		if m == milestone {
			return "release"
		}
	}
	return ""
}

// UserPref holds user preferences; stored in the datastore under email address.
type UserPref struct {
	Muted []string
}
