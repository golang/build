// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package gerrit contains code to interact with Gerrit servers.
//
// This package doesn't intend to provide complete coverage of the Gerrit API,
// but only a small subset for the current needs of the Go project infrastructure.
// Its API is not subject to the Go 1 compatibility promise and may change at any time.
// For general-purpose Gerrit API clients, see https://pkg.go.dev/search?q=gerrit.
package gerrit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client is a Gerrit client.
type Client struct {
	url  string // URL prefix, e.g. "https://go-review.googlesource.com" (without trailing slash)
	auth Auth

	// HTTPClient optionally specifies an HTTP client to use
	// instead of http.DefaultClient.
	HTTPClient *http.Client
}

// NewClient returns a new Gerrit client with the given URL prefix
// and authentication mode.
// The url should be just the scheme and hostname. For example, "https://go-review.googlesource.com".
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

// ErrResourceNotExist is returned when the requested resource doesn't exist.
// It is only for use with errors.Is.
var ErrResourceNotExist = errors.New("gerrit: requested resource does not exist")

// ErrNotModified is returned when a modification didn't result in any change.
// It is only for use with errors.Is. Not all APIs return this error; check the documentation.
var ErrNotModified = errors.New("gerrit: requested modification resulted in no change")

// HTTPError is the error type returned when a Gerrit API call does not return
// the expected status.
type HTTPError struct {
	Res     *http.Response // non-nil
	Body    []byte         // 4KB prefix
	BodyErr error          // any error reading Body
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP status %s on request to %s; %s", e.Res.Status, e.Res.Request.URL, e.Body)
}

func (e *HTTPError) Is(target error) bool {
	switch target {
	case ErrResourceNotExist:
		return e.Res.StatusCode == http.StatusNotFound
	case ErrNotModified:
		// As of writing, this error text is the only way to distinguish different Conflict errors. See
		// https://cs.opensource.google/gerrit/gerrit/gerrit/+/master:java/com/google/gerrit/server/restapi/change/ChangeEdits.java;l=346;drc=d338da307a518f7f28b94310c1c083c997ca3c6a
		// https://cs.opensource.google/gerrit/gerrit/gerrit/+/master:java/com/google/gerrit/server/edit/ChangeEditModifier.java;l=453;drc=3bc970bb3e689d1d340382c3f5e5285d44f91dbf
		return e.Res.StatusCode == http.StatusConflict && bytes.Contains(e.Body, []byte("no changes were made"))
	default:
		return false
	}
}

// doArg is an optional argument for the Client.do method.
type doArg interface {
	isDoArg()
}

type wantResStatus int

func (wantResStatus) isDoArg() {}

// reqBodyJSON sets the request body to a JSON encoding of v,
// and the request's Content-Type header to "application/json".
type reqBodyJSON struct{ v interface{} }

func (reqBodyJSON) isDoArg() {}

// reqBodyRaw sets the request body to r,
// and the request's Content-Type header to "application/octet-stream".
type reqBodyRaw struct{ r io.Reader }

func (reqBodyRaw) isDoArg() {}

type urlValues url.Values

func (urlValues) isDoArg() {}

// respBodyRaw returns the body of the response. If set, dst is ignored.
type respBodyRaw struct{ rc *io.ReadCloser }

func (respBodyRaw) isDoArg() {}

func (c *Client) do(ctx context.Context, dst interface{}, method, path string, opts ...doArg) error {
	var arg url.Values
	var requestBody io.Reader
	var contentType string
	var wantStatus = http.StatusOK
	var responseBody *io.ReadCloser
	for _, opt := range opts {
		switch opt := opt.(type) {
		case wantResStatus:
			wantStatus = int(opt)
		case reqBodyJSON:
			b, err := json.MarshalIndent(opt.v, "", "  ")
			if err != nil {
				return err
			}
			requestBody = bytes.NewReader(b)
			contentType = "application/json"
		case reqBodyRaw:
			requestBody = opt.r
			contentType = "application/octet-stream"
		case urlValues:
			arg = url.Values(opt)
		case respBodyRaw:
			responseBody = opt.rc
		default:
			panic(fmt.Sprintf("internal error; unsupported type %T", opt))
		}
	}

	// slashA is either "/a" (for authenticated requests) or "" for unauthenticated.
	// See https://gerrit-review.googlesource.com/Documentation/rest-api.html#authentication
	slashA := "/a"
	if _, ok := c.auth.(noAuth); ok {
		slashA = ""
	}
	u := c.url + slashA + path
	if arg != nil {
		u += "?" + arg.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, requestBody)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if err := c.auth.setAuth(c, req); err != nil {
		return fmt.Errorf("setting Gerrit auth: %v", err)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if responseBody != nil && *responseBody != nil {
			// We've handed off the body to the user.
			return
		}
		res.Body.Close()
	}()

	if res.StatusCode != wantStatus {
		body, err := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return &HTTPError{res, body, err}
	}

	if responseBody != nil {
		*responseBody = res.Body
		return nil
	}

	if dst == nil {
		// Drain the response body, return an error if it's anything but empty.
		body, err := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		if err != nil || len(body) != 0 {
			return &HTTPError{res, body, err}
		}
		return nil
	}
	// The JSON response begins with an XSRF-defeating header
	// like ")]}\n". Read that and skip it.
	br := bufio.NewReader(res.Body)
	if _, err := br.ReadSlice('\n'); err != nil {
		return err
	}
	return json.NewDecoder(br).Decode(dst)
}

// Possible values for the ChangeInfo Status field.
const (
	ChangeStatusNew       = "NEW"
	ChangeStatusAbandoned = "ABANDONED"
	ChangeStatusMerged    = "MERGED"
)

// ChangeInfo is a Gerrit data structure.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-info
type ChangeInfo struct {
	// The ID of the change. Subject to a 'GerritBackendFeature__return_new_change_info_id' experiment,
	// the format is either "'<project>~<_number>'" (new format),
	// or "'<project>~<branch>~<Change-Id>'" (old format).
	// 'project', '_number', and 'branch' are URL encoded.
	// For 'branch' the refs/heads/ prefix is omitted.
	// The callers must not rely on the format.
	ID string `json:"id"`

	// ChangeNumber is a change number like "4247".
	ChangeNumber int `json:"_number"`

	// ChangeID is the Change-Id footer value like "I8473b95934b5732ac55d26311a706c9c2bde9940".
	// Note that some of the functions in this package take a changeID parameter that is a {change-id},
	// which is a distinct concept from a Change-Id footer. (See the documentation links for details,
	// including https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-id).
	ChangeID string `json:"change_id"`

	Project string `json:"project"`

	// Branch is the name of the target branch.
	// The refs/heads/ prefix is omitted.
	Branch string `json:"branch"`

	Topic    string       `json:"topic"`
	Assignee *AccountInfo `json:"assignee"`
	Hashtags []string     `json:"hashtags"`

	// Subject is the subject of the change
	// (the header line of the commit message).
	Subject string `json:"subject"`

	// Status is the status of the change (NEW, SUBMITTED, MERGED,
	// ABANDONED, DRAFT).
	Status string `json:"status"`

	Created    TimeStamp    `json:"created"`
	Updated    TimeStamp    `json:"updated"`
	Submitted  TimeStamp    `json:"submitted"`
	Submitter  *AccountInfo `json:"submitter"`
	SubmitType string       `json:"submit_type"`

	// Mergeable indicates whether the change can be merged.
	// It is not set for already-merged changes,
	// nor if the change is untested, nor if the
	// SKIP_MERGEABLE option has been set.
	Mergeable bool `json:"mergeable"`

	// Submittable indicates whether the change can be submitted.
	// It is only set if requested, using the "SUBMITTABLE" option.
	Submittable bool `json:"submittable"`

	// Insertions and Deletions count inserted and deleted lines.
	Insertions int `json:"insertions"`
	Deletions  int `json:"deletions"`

	// CurrentRevision is the commit ID of the current patch set
	// of this change.  This is only set if the current revision
	// is requested or if all revisions are requested (fields
	// "CURRENT_REVISION" or "ALL_REVISIONS").
	CurrentRevision string `json:"current_revision"`

	// Revisions maps a commit ID of the patch set to a
	// RevisionInfo entity.
	//
	// Only set if the current revision is requested (in which
	// case it will only contain a key for the current revision)
	// or if all revisions are requested.
	Revisions map[string]RevisionInfo `json:"revisions"`

	// Owner is the author of the change.
	// The details are only filled in if field "DETAILED_ACCOUNTS" is requested.
	Owner *AccountInfo `json:"owner"`

	// Messages are included if field "MESSAGES" is requested.
	Messages []ChangeMessageInfo `json:"messages"`

	// Labels maps label names to LabelInfo entries.
	Labels map[string]LabelInfo `json:"labels"`

	// ReviewerUpdates are included if field "REVIEWER_UPDATES" is requested.
	ReviewerUpdates []ReviewerUpdateInfo `json:"reviewer_updates"`

	// Reviewers maps reviewer state ("REVIEWER", "CC", "REMOVED")
	// to a list of accounts.
	// REVIEWER lists users with at least one non-zero vote on the change.
	// CC lists users added to the change who has not voted.
	// REMOVED lists users who were previously reviewers on the change
	// but who have been removed.
	// Reviewers is only included if "DETAILED_LABELS" is requested.
	Reviewers map[string][]*AccountInfo `json:"reviewers"`

	// WorkInProgress indicates that the change is marked as a work in progress.
	// (This means it is not yet ready for review, but it is still publicly visible.)
	WorkInProgress bool `json:"work_in_progress"`

	// HasReviewStarted indicates whether the change has ever been marked
	// ready for review in the past (not as a work in progress).
	HasReviewStarted bool `json:"has_review_started"`

	// RevertOf lists the numeric Change-Id of the change that this change reverts.
	RevertOf int `json:"revert_of"`

	// MoreChanges is set on the last change from QueryChanges if
	// the result set is truncated by an 'n' parameter.
	MoreChanges bool `json:"_more_changes"`

	// ContainsGitConflicts indicates if the change has merge conflicts.
	ContainsGitConflicts bool `json:"contains_git_conflicts"`
}

// ReviewerUpdateInfo is a Gerrit data structure.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#review-update-info
type ReviewerUpdateInfo struct {
	Updated   TimeStamp    `json:"updated"`
	UpdatedBy *AccountInfo `json:"updated_by"`
	Reviewer  *AccountInfo `json:"reviewer"`
	State     string       // "REVIEWER", "CC", or "REMOVED"
}

// AccountInfo is a Gerrit data structure. It's used both for getting the details
// for a single account, as well as for querying multiple accounts.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-accounts.html#account-info.
type AccountInfo struct {
	NumericID int64    `json:"_account_id"`
	Name      string   `json:"name,omitempty"`
	Email     string   `json:"email,omitempty"`
	Username  string   `json:"username,omitempty"`
	Tags      []string `json:"tags,omitempty"`

	// MoreAccounts is set on the last account from QueryAccounts if
	// the result set is truncated by an 'n' parameter (or has more).
	MoreAccounts bool `json:"_more_accounts"`
}

func (ai *AccountInfo) Equal(v *AccountInfo) bool {
	if ai == nil || v == nil {
		return false
	}
	return ai.NumericID == v.NumericID
}

type ChangeMessageInfo struct {
	ID             string       `json:"id"`
	Author         *AccountInfo `json:"author"`
	Time           TimeStamp    `json:"date"`
	Message        string       `json:"message"`
	Tag            string       `json:"tag,omitempty"`
	RevisionNumber int          `json:"_revision_number"`
}

// The LabelInfo entity contains information about a label on a
// change, always corresponding to the current patch set.
//
// There are two options that control the contents of LabelInfo:
// LABELS and DETAILED_LABELS.
//
// For a quick summary of the state of labels, use LABELS.
//
// For detailed information about labels, including exact numeric
// votes for all users and the allowed range of votes for the current
// user, use DETAILED_LABELS.
type LabelInfo struct {
	// Optional means the label may be set, but it’s neither
	// necessary for submission nor does it block submission if
	// set.
	Optional bool `json:"optional"`

	// Fields set by LABELS field option:
	Approved *AccountInfo `json:"approved"`

	// Fields set by DETAILED_LABELS option:
	All []ApprovalInfo `json:"all"`
}

type ApprovalInfo struct {
	AccountInfo
	Value int       `json:"value"`
	Date  TimeStamp `json:"date"`
}

// The RevisionInfo entity contains information about a patch set. Not
// all fields are returned by default. Additional fields can be
// obtained by adding o parameters as described at:
// https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#list-changes
type RevisionInfo struct {
	Draft             bool                  `json:"draft"`
	PatchSetNumber    int                   `json:"_number"`
	Created           TimeStamp             `json:"created"`
	Uploader          *AccountInfo          `json:"uploader"`
	Ref               string                `json:"ref"`
	Fetch             map[string]*FetchInfo `json:"fetch"`
	Commit            *CommitInfo           `json:"commit"`
	Files             map[string]*FileInfo  `json:"files"`
	CommitWithFooters string                `json:"commit_with_footers"`
	Kind              string                `json:"kind"`
	// TODO: more
}

type CommitInfo struct {
	Author    GitPersonInfo `json:"author"`
	Committer GitPersonInfo `json:"committer"`
	CommitID  string        `json:"commit"`
	Subject   string        `json:"subject"`
	Message   string        `json:"message"`
	Parents   []CommitInfo  `json:"parents"`
}

type GitPersonInfo struct {
	Name     string    `json:"name"`
	Email    string    `json:"Email"`
	Date     TimeStamp `json:"date"`
	TZOffset int       `json:"tz"`
}

func (gpi *GitPersonInfo) Equal(v *GitPersonInfo) bool {
	if gpi == nil {
		if gpi != v {
			return false
		}
		return true
	}
	return gpi.Name == v.Name && gpi.Email == v.Email && gpi.Date.Equal(v.Date) &&
		gpi.TZOffset == v.TZOffset
}

// Possible values for the FileInfo Status field.
const (
	FileInfoAdded     = "A"
	FileInfoDeleted   = "D"
	FileInfoRenamed   = "R"
	FileInfoCopied    = "C"
	FileInfoRewritten = "W"
)

type FileInfo struct {
	Status        string `json:"status"`
	Binary        bool   `json:"binary"`
	OldPath       string `json:"old_path"`
	LinesInserted int    `json:"lines_inserted"`
	LinesDeleted  int    `json:"lines_deleted"`
}

type FetchInfo struct {
	URL      string            `json:"url"`
	Ref      string            `json:"ref"`
	Commands map[string]string `json:"commands"`
}

// QueryChangesOpt are options for QueryChanges.
type QueryChangesOpt struct {
	// N is the number of results to return.
	// If 0, the 'n' parameter is not sent to Gerrit.
	N int

	// Start is the number of results to skip (useful in pagination).
	// To figure out if there are more results, the last ChangeInfo struct
	// in the last call to QueryChanges will have the field MoreAccounts=true.
	// If 0, the 'S' parameter is not sent to Gerrit.
	Start int

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

// QueryChanges queries changes. The q parameter is a Gerrit search query.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#list-changes
// For the query syntax, see https://gerrit-review.googlesource.com/Documentation/user-search.html#_search_operators
func (c *Client) QueryChanges(ctx context.Context, q string, opts ...QueryChangesOpt) ([]*ChangeInfo, error) {
	var opt QueryChangesOpt
	switch len(opts) {
	case 0:
	case 1:
		opt = opts[0]
	default:
		return nil, errors.New("only 1 option struct supported")
	}
	var changes []*ChangeInfo
	err := c.do(ctx, &changes, "GET", "/changes/", urlValues{
		"q": {q},
		"n": condInt(opt.N),
		"o": opt.Fields,
		"S": condInt(opt.Start),
	})
	return changes, err
}

// GetChange returns information about a single change.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#get-change
func (c *Client) GetChange(ctx context.Context, changeID string, opts ...QueryChangesOpt) (*ChangeInfo, error) {
	var opt QueryChangesOpt
	switch len(opts) {
	case 0:
	case 1:
		opt = opts[0]
	default:
		return nil, errors.New("only 1 option struct supported")
	}
	var change ChangeInfo
	err := c.do(ctx, &change, "GET", "/changes/"+changeID, urlValues{
		"n": condInt(opt.N),
		"o": opt.Fields,
	})
	return &change, err
}

// GetChangeDetail retrieves a change with labels, detailed labels, detailed
// accounts, and messages.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#get-change-detail
func (c *Client) GetChangeDetail(ctx context.Context, changeID string, opts ...QueryChangesOpt) (*ChangeInfo, error) {
	var opt QueryChangesOpt
	switch len(opts) {
	case 0:
	case 1:
		opt = opts[0]
	default:
		return nil, errors.New("only 1 option struct supported")
	}
	var change ChangeInfo
	err := c.do(ctx, &change, "GET", "/changes/"+changeID+"/detail", urlValues{
		"o": opt.Fields,
	})
	if err != nil {
		return nil, err
	}
	return &change, nil
}

// ListChangeComments retrieves a map of published comments for the given change ID.
// The map key is the file path (such as "maintner/git_test.go" or "/PATCHSET_LEVEL").
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#list-change-comments.
func (c *Client) ListChangeComments(ctx context.Context, changeID string) (map[string][]CommentInfo, error) {
	var m map[string][]CommentInfo
	if err := c.do(ctx, &m, "GET", "/changes/"+changeID+"/comments"); err != nil {
		return nil, err
	}
	return m, nil
}

// CommentInfo contains information about an inline comment.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#comment-info.
type CommentInfo struct {
	PatchSet   int          `json:"patch_set,omitempty"`
	ID         string       `json:"id"`
	Path       string       `json:"path,omitempty"`
	Message    string       `json:"message,omitempty"`
	Updated    TimeStamp    `json:"updated"`
	Author     *AccountInfo `json:"author,omitempty"`
	InReplyTo  string       `json:"in_reply_to,omitempty"`
	Unresolved *bool        `json:"unresolved,omitempty"`
	Tag        string       `json:"tag,omitempty"`
}

// ListFiles retrieves a map of filenames to FileInfo's for the given change ID and revision.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#list-files
func (c *Client) ListFiles(ctx context.Context, changeID, revision string) (map[string]*FileInfo, error) {
	var m map[string]*FileInfo
	if err := c.do(ctx, &m, "GET", "/changes/"+changeID+"/revisions/"+revision+"/files"); err != nil {
		return nil, err
	}
	return m, nil
}

// ReviewInput contains information for adding a review to a revision.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#review-input
type ReviewInput struct {
	Message string         `json:"message,omitempty"`
	Labels  map[string]int `json:"labels,omitempty"`
	Tag     string         `json:"tag,omitempty"`

	// Comments contains optional per-line comments to post.
	// The map key is a file path (such as "src/foo/bar.go").
	Comments map[string][]CommentInput `json:"comments,omitempty"`

	// Reviewers optionally specifies new reviewers to add to the change.
	Reviewers []ReviewerInput `json:"reviewers,omitempty"`
}

// ReviewerInput contains information for adding a reviewer to a change.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#reviewer-input
type ReviewerInput struct {
	// Reviewer is the ID of the account to be added as reviewer.
	// See https://gerrit-review.googlesource.com/Documentation/rest-api-accounts.html#account-id
	Reviewer string `json:"reviewer"`
	State    string `json:"state,omitempty"` // REVIEWER or CC (default: REVIEWER)
}

// CommentInput contains information for creating an inline comment.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#comment-input
type CommentInput struct {
	Line       int    `json:"line,omitempty"`
	Message    string `json:"message"`
	InReplyTo  string `json:"in_reply_to,omitempty"`
	Unresolved *bool  `json:"unresolved,omitempty"`

	// TODO(haya14busa): more, as needed.
}

type reviewInfo struct {
	Labels map[string]int `json:"labels,omitempty"`
}

// SetReview leaves a message on a change and/or modifies labels.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#set-review
// The changeID is https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-id
// The revision is https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#revision-id
func (c *Client) SetReview(ctx context.Context, changeID, revision string, review ReviewInput) error {
	var res reviewInfo
	return c.do(ctx, &res, "POST", fmt.Sprintf("/changes/%s/revisions/%s/review", changeID, revision),
		reqBodyJSON{&review})
}

// ReviewerInfo contains information about reviewers of a change.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#reviewer-info
type ReviewerInfo struct {
	AccountInfo
	Approvals map[string]string `json:"approvals"`
}

// ListReviewers returns all reviewers on a change.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#list-reviewers
// The changeID is https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-id
func (c *Client) ListReviewers(ctx context.Context, changeID string) ([]ReviewerInfo, error) {
	var res []ReviewerInfo
	if err := c.do(ctx, &res, "GET", fmt.Sprintf("/changes/%s/reviewers", changeID)); err != nil {
		return nil, err
	}
	return res, nil
}

// HashtagsInput is the request body used when modifying a CL's hashtags.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#hashtags-input
type HashtagsInput struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// SetHashtags modifies the hashtags for a CL, supporting both adding
// and removing hashtags in one request. On success it returns the new
// set of hashtags.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#set-hashtags
func (c *Client) SetHashtags(ctx context.Context, changeID string, hashtags HashtagsInput) ([]string, error) {
	var res []string
	err := c.do(ctx, &res, "POST", fmt.Sprintf("/changes/%s/hashtags", changeID), reqBodyJSON{&hashtags})
	return res, err
}

// AddHashtags is a wrapper around SetHashtags that only supports adding tags.
func (c *Client) AddHashtags(ctx context.Context, changeID string, tags ...string) ([]string, error) {
	return c.SetHashtags(ctx, changeID, HashtagsInput{Add: tags})
}

// RemoveHashtags is a wrapper around SetHashtags that only supports removing tags.
func (c *Client) RemoveHashtags(ctx context.Context, changeID string, tags ...string) ([]string, error) {
	return c.SetHashtags(ctx, changeID, HashtagsInput{Remove: tags})
}

// GetHashtags returns a CL's current hashtags.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#get-hashtags
func (c *Client) GetHashtags(ctx context.Context, changeID string) ([]string, error) {
	var res []string
	err := c.do(ctx, &res, "GET", fmt.Sprintf("/changes/%s/hashtags", changeID))
	return res, err
}

// DeleteTopic deletes the topic of a change.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#delete-topic.
func (c *Client) DeleteTopic(ctx context.Context, changeID string) error {
	return c.do(ctx, nil, "DELETE", "/changes/"+changeID+"/topic", wantResStatus(http.StatusNoContent))
}

// AbandonChange abandons the given change.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#abandon-change
// The changeID is https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-id
// The input for the call is https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#abandon-input
func (c *Client) AbandonChange(ctx context.Context, changeID string, message ...string) error {
	var msg string
	if len(message) > 1 {
		panic("invalid use of multiple message inputs")
	}
	if len(message) == 1 {
		msg = message[0]
	}
	b := struct {
		Message string `json:"message,omitempty"`
	}{msg}
	var change ChangeInfo
	return c.do(ctx, &change, "POST", "/changes/"+changeID+"/abandon", reqBodyJSON{&b})
}

// ProjectInput contains the options for creating a new project.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#project-input
type ProjectInput struct {
	Parent      string `json:"parent,omitempty"`
	Description string `json:"description,omitempty"`
	SubmitType  string `json:"submit_type,omitempty"`

	CreateNewChangeForAllNotInTarget string `json:"create_new_change_for_all_not_in_target,omitempty"`

	// TODO(bradfitz): more, as needed.
}

// ProjectInfo is information about a Gerrit project.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#project-info
type ProjectInfo struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Parent      string            `json:"parent"`
	CloneURL    string            `json:"clone_url"`
	Description string            `json:"description"`
	State       string            `json:"state"`
	Branches    map[string]string `json:"branches"`
	WebLinks    []WebLinkInfo     `json:"web_links,omitempty"`
}

// ListProjects returns the server's active projects.
//
// The returned slice is sorted by project ID and excludes the "All-Projects" and "All-Users" projects.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#list-projects.
func (c *Client) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	var res map[string]ProjectInfo
	err := c.do(ctx, &res, "GET", "/projects/")
	if err != nil {
		return nil, err
	}
	var ret []ProjectInfo
	for name, pi := range res {
		if name == "All-Projects" || name == "All-Users" {
			continue
		}
		if pi.State != "ACTIVE" {
			continue
		}
		// https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#project-info:
		// "name not set if returned in a map where the project name is used as map key"
		pi.Name = name
		ret = append(ret, pi)
	}
	sort.Slice(ret, func(i, j int) bool { return ret[i].ID < ret[j].ID })
	return ret, nil
}

// CreateProject creates a new project.
func (c *Client) CreateProject(ctx context.Context, name string, p ...ProjectInput) (ProjectInfo, error) {
	var pi ProjectInput
	if len(p) > 1 {
		panic("invalid use of multiple project inputs")
	}
	if len(p) == 1 {
		pi = p[0]
	}
	var res ProjectInfo
	err := c.do(ctx, &res, "PUT", fmt.Sprintf("/projects/%s", url.PathEscape(name)), reqBodyJSON{&pi}, wantResStatus(http.StatusCreated))
	return res, err
}

// CreateChange creates a new change.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#create-change.
func (c *Client) CreateChange(ctx context.Context, ci ChangeInput) (ChangeInfo, error) {
	var res ChangeInfo
	err := c.do(ctx, &res, "POST", "/changes/", reqBodyJSON{&ci}, wantResStatus(http.StatusCreated))
	return res, err
}

// ChangeInput contains the options for creating a new change.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-input.
type ChangeInput struct {
	Project string `json:"project"`
	Branch  string `json:"branch"`
	Subject string `json:"subject"`
}

// ChangeFileContentInChangeEdit puts content of a file to a change edit.
// If no change is made, an error that matches ErrNotModified is returned.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#put-edit-file.
func (c *Client) ChangeFileContentInChangeEdit(ctx context.Context, changeID string, path string, content string) error {
	return c.do(ctx, nil, "PUT", "/changes/"+changeID+"/edit/"+url.PathEscape(path),
		reqBodyRaw{strings.NewReader(content)}, wantResStatus(http.StatusNoContent))
}

// DeleteFileInChangeEdit deletes a file from a change edit.
// If no change is made, an error that matches ErrNotModified is returned.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#delete-edit-file.
func (c *Client) DeleteFileInChangeEdit(ctx context.Context, changeID string, path string) error {
	return c.do(ctx, nil, "DELETE", "/changes/"+changeID+"/edit/"+url.PathEscape(path), wantResStatus(http.StatusNoContent))
}

// PublishChangeEdit promotes the change edit to a regular patch set.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#publish-edit.
func (c *Client) PublishChangeEdit(ctx context.Context, changeID string) error {
	return c.do(ctx, nil, "POST", "/changes/"+changeID+"/edit:publish", wantResStatus(http.StatusNoContent))
}

// GetProjectInfo returns info about a project.
func (c *Client) GetProjectInfo(ctx context.Context, name string) (ProjectInfo, error) {
	var res ProjectInfo
	err := c.do(ctx, &res, "GET", fmt.Sprintf("/projects/%s", url.PathEscape(name)))
	return res, err
}

// BranchInfo is information about a branch.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#branch-info
type BranchInfo struct {
	Ref       string `json:"ref"`
	Revision  string `json:"revision"`
	CanDelete bool   `json:"can_delete"`
}

// The BranchInput entity contains information for the creation of a new branch.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#branch-input
type BranchInput struct {
	Ref      string `json:"ref,omitempty"`
	Revision string `json:"revision,omitempty"`
	// ValidationOptions is optional.
}

// GetProjectBranches returns the branches for the project name. The branches are stored in a map
// keyed by reference.
func (c *Client) GetProjectBranches(ctx context.Context, name string) (map[string]BranchInfo, error) {
	var res []BranchInfo
	err := c.do(ctx, &res, "GET", fmt.Sprintf("/projects/%s/branches/", url.PathEscape(name)))
	if err != nil {
		return nil, err
	}
	m := map[string]BranchInfo{}
	for _, bi := range res {
		m[bi.Ref] = bi
	}
	return m, nil
}

// ListBranches lists all branches in the project.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#list-branches.
func (c *Client) ListBranches(ctx context.Context, project string) ([]BranchInfo, error) {
	var res []BranchInfo
	err := c.do(ctx, &res, "GET", fmt.Sprintf("/projects/%s/branches", url.PathEscape(project)))
	return res, err
}

// GetBranch gets a particular branch in project.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#get-branch.
func (c *Client) GetBranch(ctx context.Context, project, branch string) (BranchInfo, error) {
	var res BranchInfo
	err := c.do(ctx, &res, "GET", fmt.Sprintf("/projects/%s/branches/%s", url.PathEscape(project), branch))
	return res, err
}

// CreateBranch create a new branch in the project.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#create-branch
func (c *Client) CreateBranch(ctx context.Context, project, branch string, input BranchInput) (BranchInfo, error) {
	var res BranchInfo
	err := c.do(ctx, &res, "PUT", fmt.Sprintf("/projects/%s/branches/%s", url.PathEscape(project), url.PathEscape(branch)), reqBodyJSON{&input}, wantResStatus(http.StatusCreated))
	return res, err
}

// GetFileContent gets a file's contents at a particular commit.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#get-content-from-commit.
func (c *Client) GetFileContent(ctx context.Context, project, commit, path string) (io.ReadCloser, error) {
	var body io.ReadCloser
	err := c.do(ctx, nil, "GET", fmt.Sprintf("/projects/%s/commits/%s/files/%s/content", url.PathEscape(project), commit, url.PathEscape(path)), respBodyRaw{&body})
	if err != nil {
		return nil, err
	}
	return readCloser{
		Reader: base64.NewDecoder(base64.StdEncoding, body),
		Closer: body,
	}, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// WebLinkInfo is information about a web link.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#web-link-info
type WebLinkInfo struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	ImageURL string `json:"image_url"`
}

func (wli *WebLinkInfo) Equal(v *WebLinkInfo) bool {
	if wli == nil || v == nil {
		return false
	}
	return wli.Name == v.Name && wli.URL == v.URL && wli.ImageURL == v.ImageURL
}

// TagInfo is information about a tag.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#tag-info
type TagInfo struct {
	Ref       string         `json:"ref"`
	Revision  string         `json:"revision"`
	Object    string         `json:"object,omitempty"`
	Message   string         `json:"message,omitempty"`
	Tagger    *GitPersonInfo `json:"tagger,omitempty"`
	Created   TimeStamp      `json:"created,omitempty"`
	CanDelete bool           `json:"can_delete"`
	WebLinks  []WebLinkInfo  `json:"web_links,omitempty"`
}

func (ti *TagInfo) Equal(v *TagInfo) bool {
	if ti == nil || v == nil {
		return false
	}
	if ti.Ref != v.Ref || ti.Revision != v.Revision || ti.Object != v.Object ||
		ti.Message != v.Message || !ti.Created.Equal(v.Created) || ti.CanDelete != v.CanDelete {
		return false
	}
	if !ti.Tagger.Equal(v.Tagger) {
		return false
	}
	if len(ti.WebLinks) != len(v.WebLinks) {
		return false
	}
	for i := range ti.WebLinks {
		if !ti.WebLinks[i].Equal(&v.WebLinks[i]) {
			return false
		}
	}
	return true
}

// GetProjectTags returns the tags for the project name. The tags are stored in a map keyed by
// reference.
func (c *Client) GetProjectTags(ctx context.Context, name string) (map[string]TagInfo, error) {
	var res []TagInfo
	err := c.do(ctx, &res, "GET", fmt.Sprintf("/projects/%s/tags/", url.PathEscape(name)))
	if err != nil {
		return nil, err
	}
	m := map[string]TagInfo{}
	for _, ti := range res {
		m[ti.Ref] = ti
	}
	return m, nil
}

// GetTag returns a particular tag on project.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#get-tag.
func (c *Client) GetTag(ctx context.Context, project, tag string) (TagInfo, error) {
	var res TagInfo
	err := c.do(ctx, &res, "GET", fmt.Sprintf("/projects/%s/tags/%s", url.PathEscape(project), url.PathEscape(tag)))
	return res, err
}

// TagInput contains information for creating a tag.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#tag-input
type TagInput struct {
	// Ref is optional, and when present must be equal to the URL parameter. Removed.
	Revision string `json:"revision,omitempty"`
	Message  string `json:"message,omitempty"`
}

// CreateTag creates a tag on project.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#create-tag.
func (c *Client) CreateTag(ctx context.Context, project, tag string, input TagInput) (TagInfo, error) {
	var res TagInfo
	err := c.do(ctx, &res, "PUT", fmt.Sprintf("/projects/%s/tags/%s", project, url.PathEscape(tag)), reqBodyJSON{&input}, wantResStatus(http.StatusCreated))
	return res, err
}

// DeleteTag deletes a tag on project.
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#delete-tag.
func (c *Client) DeleteTag(ctx context.Context, project, tag string) error {
	return c.do(ctx, nil, http.MethodDelete, fmt.Sprintf("/projects/%s/tags/%s", project, url.PathEscape(tag)), wantResStatus(http.StatusNoContent))
}

// GetAccountInfo gets the specified account's information from Gerrit.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-accounts.html#get-account
// The accountID is https://gerrit-review.googlesource.com/Documentation/rest-api-accounts.html#account-id
//
// Note that getting "self" is a good way to validate host access, since it only requires peeker
// access to the host, not to any particular repository.
func (c *Client) GetAccountInfo(ctx context.Context, accountID string) (AccountInfo, error) {
	var res AccountInfo
	err := c.do(ctx, &res, "GET", fmt.Sprintf("/accounts/%s", accountID))
	return res, err
}

// QueryAccountsOpt are options for QueryAccounts.
type QueryAccountsOpt struct {
	// N is the number of results to return.
	// If 0, the 'n' parameter is not sent to Gerrit.
	N int

	// Start is the number of results to skip (useful in pagination).
	// To figure out if there are more results, the last AccountInfo struct
	// in the last call to QueryAccounts will have the field MoreAccounts=true.
	// If 0, the 'S' parameter is not sent to Gerrit.
	Start int

	// Fields are optional fields to also return.
	// Example strings include "DETAILS", "ALL_EMAILS".
	// By default, only the account IDs are returned.
	// For a complete list, see:
	// https://gerrit-review.googlesource.com/Documentation/rest-api-accounts.html#query-account
	Fields []string
}

// QueryAccounts queries accounts. The q parameter is a Gerrit search query.
// For the API call and query syntax, see https://gerrit-review.googlesource.com/Documentation/rest-api-accounts.html#query-account
func (c *Client) QueryAccounts(ctx context.Context, q string, opts ...QueryAccountsOpt) ([]*AccountInfo, error) {
	var opt QueryAccountsOpt
	switch len(opts) {
	case 0:
	case 1:
		opt = opts[0]
	default:
		return nil, errors.New("only 1 option struct supported")
	}
	var changes []*AccountInfo
	err := c.do(ctx, &changes, "GET", "/accounts/", urlValues{
		"q": {q},
		"n": condInt(opt.N),
		"o": opt.Fields,
		"S": condInt(opt.Start),
	})
	return changes, err
}

type TimeStamp time.Time

func (ts TimeStamp) Equal(v TimeStamp) bool {
	return ts.Time().Equal(v.Time())
}

// Gerrit's timestamp layout is like time.RFC3339Nano, but with a space instead of the "T",
// and without a timezone (it's always in UTC).
const timeStampLayout = "2006-01-02 15:04:05.999999999"

func (ts TimeStamp) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"%s"`, ts.Time().UTC().Format(timeStampLayout))), nil
}

func (ts *TimeStamp) UnmarshalJSON(p []byte) error {
	if len(p) < 2 {
		return errors.New("timestamp too short")
	}
	if p[0] != '"' || p[len(p)-1] != '"' {
		return errors.New("not double-quoted")
	}
	s := strings.Trim(string(p), "\"")
	t, err := time.Parse(timeStampLayout, s)
	if err != nil {
		return err
	}
	*ts = TimeStamp(t)
	return nil
}

func (ts TimeStamp) Time() time.Time { return time.Time(ts) }

// GroupInfo contains information about a group.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-groups.html#group-info.
type GroupInfo struct {
	ID      string           `json:"id"`
	URL     string           `json:"url"`
	Name    string           `json:"name"`
	GroupID int64            `json:"group_id"`
	Options GroupOptionsInfo `json:"options"`
	Owner   string           `json:"owner"`
	OwnerID string           `json:"owner_id"`
}

type GroupOptionsInfo struct {
	VisibleToAll bool `json:"visible_to_all"`
}

func (c *Client) GetGroups(ctx context.Context) (map[string]*GroupInfo, error) {
	res := make(map[string]*GroupInfo)
	err := c.do(ctx, &res, "GET", "/groups/")
	for k, gi := range res {
		if gi != nil && gi.Name == "" {
			gi.Name = k
		}
	}
	return res, err
}

func (c *Client) GetGroupMembers(ctx context.Context, groupID string) ([]AccountInfo, error) {
	var ais []AccountInfo
	err := c.do(ctx, &ais, "GET", "/groups/"+groupID+"/members")
	return ais, err
}

// SubmitChange submits the given change.
// For the API call, see https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#submit-change
// The changeID is https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#change-id
func (c *Client) SubmitChange(ctx context.Context, changeID string) (ChangeInfo, error) {
	var change ChangeInfo
	err := c.do(ctx, &change, "POST", "/changes/"+changeID+"/submit")
	return change, err
}

// MergeableInfo contains information about the mergeability of a change.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#mergeable-info.
type MergeableInfo struct {
	SubmitType   string `json:"submit_type"`
	Strategy     string `json:"strategy"`
	Mergeable    bool   `json:"mergeable"`
	CommitMerged bool   `json:"commit_merged"`
}

// GetMergeable retrieves mergeability information for a change at a specific revision.
func (c *Client) GetMergeable(ctx context.Context, changeID, revision string) (MergeableInfo, error) {
	var mergeable MergeableInfo
	err := c.do(ctx, &mergeable, "GET", "/changes/"+changeID+"/revisions/"+revision+"/mergeable")
	return mergeable, err
}

// ActionInfo contains information about actions a client can make to
// manipulate a resource.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#action-info.
type ActionInfo struct {
	Method  string `json:"method"`
	Label   string `json:"label"`
	Title   string `json:"title"`
	Enabled bool   `json:"enabled"`
}

// GetRevisionActions retrieves revision actions.
func (c *Client) GetRevisionActions(ctx context.Context, changeID, revision string) (map[string]*ActionInfo, error) {
	var actions map[string]*ActionInfo
	err := c.do(ctx, &actions, "GET", "/changes/"+changeID+"/revisions/"+revision+"/actions")
	return actions, err
}

// RelatedChangeAndCommitInfo contains information about a particular
// change at a particular commit.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#related-change-and-commit-info.
type RelatedChangeAndCommitInfo struct {
	Project      string     `json:"project"`
	ChangeID     string     `json:"change_id"`
	ChangeNumber int32      `json:"_change_number"`
	Commit       CommitInfo `json:"commit"`
	Status       string     `json:"status"`
}

// RelatedChangesInfo contains information about a set of related changes.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#related-changes-info.
type RelatedChangesInfo struct {
	Changes []RelatedChangeAndCommitInfo `json:"changes"`
}

// GetRelatedChanges retrieves information about a set of related changes.
func (c *Client) GetRelatedChanges(ctx context.Context, changeID, revision string) (*RelatedChangesInfo, error) {
	var changes *RelatedChangesInfo
	err := c.do(ctx, &changes, "GET", "/changes/"+changeID+"/revisions/"+revision+"/related")
	return changes, err
}

// GetCommitsInRefs gets refs in which the specified commits were merged into.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-projects.html#commits-included-in.
func (c *Client) GetCommitsInRefs(ctx context.Context, project string, commits, refs []string) (map[string][]string, error) {
	result := map[string][]string{}
	vals := url.Values{}
	vals["commit"] = commits
	vals["ref"] = refs
	err := c.do(ctx, &result, "GET", "/projects/"+url.PathEscape(project)+"/commits:in", urlValues(vals))
	return result, err
}

// CherryPickRevision cherry picks a change revision to a destination branch.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#cherry-pick.
func (c *Client) CherryPickRevision(ctx context.Context, changeID, revisionID string, cpi CherryPickInput) (ChangeInfo, error) {
	var change ChangeInfo
	err := c.do(ctx, &change, "POST", "/changes/"+changeID+"/revisions/"+revisionID+"/cherrypick", reqBodyJSON{&cpi}, wantResStatus(http.StatusOK))
	return change, err
}

// CherryPickInput contains the options for creating a new cherry-pick.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#cherrypick-input.
type CherryPickInput struct {
	Destination    string `json:"destination"`
	KeepReviewers  bool   `json:"keep_reviewers"`
	AllowConflicts bool   `json:"allow_conflicts"`
	Message        string `json:"message,omitempty"`
}

// MoveChange moves a change onto a destination branch.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#move-change.
func (c *Client) MoveChange(ctx context.Context, changeID string, mi MoveInput) (ChangeInfo, error) {
	var change ChangeInfo
	err := c.do(ctx, &change, "POST", "/changes/"+changeID+"/move", reqBodyJSON{&mi}, wantResStatus(http.StatusOK))
	return change, err
}

// MoveInput contains the options for moving a change.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#move-input.
type MoveInput struct {
	DestinationBranch string `json:"destination_branch"`
	KeepAllVotes      bool   `json:"keep_all_votes"`
}

// RebaseChange rebases a change onto a new base revision, or directly on top of
// the target branch.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#rebase-change.
func (c *Client) RebaseChange(ctx context.Context, changeID string, ri RebaseInput) (ChangeInfo, error) {
	var change ChangeInfo
	err := c.do(ctx, &change, "POST", "/changes/"+changeID+"/rebase", reqBodyJSON{&ri}, wantResStatus(http.StatusOK))
	return change, err
}

// RebaseInput contains the options for rebasing a change.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#rebase-input.
type RebaseInput struct {
	Base           string `json:"base,omitempty"`
	AllowConflicts bool   `json:"allow_conflicts"`
}

// GetCommitMessage retrieves the commit message for a change.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#get-message.
func (c *Client) GetCommitMessage(ctx context.Context, changeID string) (CommitMessageInfo, error) {
	var cmi CommitMessageInfo
	err := c.do(ctx, &cmi, "GET", "/changes/"+changeID+"/message")
	return cmi, err
}

// CommitMessageInfo contains information about a commit message.
//
// See https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#commit-message-info.
type CommitMessageInfo struct {
	Subject     string            `json:"subject"`
	FullMessage string            `json:"full_message"`
	Footers     map[string]string `json:"footers"`
}
