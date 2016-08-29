// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package devapp

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/build/godash"

	"golang.org/x/net/context"
	"google.golang.org/appengine/urlfetch"
)

func updateStats(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	r.ParseForm()

	caches := getCaches(ctx, "github-token", "gzstats")

	log := logFn(ctx, w)

	stats := &godash.Stats{}
	if err := unpackCache(caches["gzstats"], stats); err != nil {
		return err
	}

	transport := &urlfetch.Transport{Context: ctx}
	gh := godash.NewGitHubClient("golang/go", string(caches["github-token"].Value), transport)

	if r.Form.Get("reset_detail") != "" {
		stats.IssueDetailSince = time.Time{}
	}

	start := time.Now()
	if issue := r.Form.Get("issue"); issue != "" {
		num, err := strconv.Atoi(issue)
		if err != nil {
			return err
		}
		err = stats.UpdateIssue(gh, num, log)
		if err != nil {
			return err
		}
		json.NewEncoder(w).Encode(stats.Issues[num])
	} else {
		if err := stats.Update(ctx, gh, log); err != nil {
			return err
		}
	}
	log("Have data about %d issues", len(stats.Issues))
	log("Updated issue stats to %v (detail to %v) in %.3f seconds", stats.Since, stats.IssueDetailSince, time.Now().Sub(start).Seconds())
	return writeCache(ctx, "gzstats", stats)
}
