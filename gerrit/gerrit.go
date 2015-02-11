// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package gerrit contains code to interact with Gerrit servers.
package gerrit

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client is a Gerrit client.
type Client struct {
	url  string // URL prefix, e.g. "https://go-review.googlesource.com/a" (without trailing slash)
	auth Auth

	// HTTPClient optionally specifies an HTTP client to use
	// instead of http.DefaultClient.
	HTTPClient *http.Client
}

// NewClient returns a new Gerrit client with the given URL prefix
// and authentication mode.
// If auth is nil, a default is used, or requests are made unauthenticated.
func NewClient(url string, auth Auth) *Client {
	if auth == nil {
		// TODO(bradfitz): use GitCookies auth, once that exists
		auth = NoAuth
	}
	return &Client{
		url:  strings.TrimSuffix(url, "/"),
		auth: auth,
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) doGet(dst interface{}, path string, arg url.Values) error {
	var err error
	req, err := http.NewRequest("GET", c.url+path+"?"+arg.Encode(), nil)
	if err != nil {
		return err
	}
	c.auth.setAuth(c, req)
	res, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// The JSON response begins with an XSRF-defeating header
	// like ")]}\n". Read that and skip it.
	br := bufio.NewReader(res.Body)
	if _, err := br.ReadSlice('\n'); err != nil {
		return err
	}
	return json.NewDecoder(br).Decode(dst)
}

// ChangeInfo is a Gerrit data structure.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-info
type ChangeInfo struct {
	// ID is the ID of the change in the format
	// "'<project>~<branch>~<Change-Id>'", where 'project',
	// 'branch' and 'Change-Id' are URL encoded. For 'branch' the
	// refs/heads/ prefix is omitted.
	ID string `json:"id"`

	Project string `json:"project"`

	// Branch is the name of the target branch.
	// The refs/heads/ prefix is omitted.
	Branch string `json:"branch"`

	ChangeID string `json:"change_id"`

	Subject string `json:"subject"`

	// Status is the status of the change (NEW, SUBMITTED, MERGED,
	// ABANDONED, DRAFT).
	Status string `json:"status"`

	// CurrentRevision is the commit ID of the current patch set
	// of this change.  This is only set if the current revision
	// is requested or if all revisions are requested.
	CurrentRevision string `json:"current_revision"`

	// TODO: more as needed

	// MoreChanges is set on the last change from QueryChanges if
	// the result set is truncated by an 'n' parameter.
	MoreChanges bool `json:"_more_changes"`
}

// QueryChangesOpt are options for QueryChanges.
type QueryChangesOpt struct {
	// N is the number of results to return.
	// If 0, the 'n' parameter is not sent to Gerrit.
	N int

	// Fields are optional fields to also return.
	// Example strings include "ALL_REVISIONS", "LABELS", "MESSAGES".
	// For a complete list, see:
	// https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-info
	Fields []string
}

func condInt(n int) []string {
	if n != 0 {
		return []string{strconv.Itoa(n)}
	}
	return nil
}

func (c *Client) QueryChanges(q string, opts ...QueryChangesOpt) ([]*ChangeInfo, error) {
	var opt QueryChangesOpt
	switch len(opts) {
	case 0:
	case 1:
		opt = opts[0]
	default:
		return nil, errors.New("only 1 option struct supported")
	}
	var changes []*ChangeInfo
	err := c.doGet(&changes, "/changes/", url.Values{
		"q": {q},
		"n": condInt(opt.N),
		"o": opt.Fields,
	})
	return changes, err
}
