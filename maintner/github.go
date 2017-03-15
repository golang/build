// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/google/go-github/github"
	"github.com/gregjones/httpcache"

	"golang.org/x/build/maintner/maintpb"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
)

// xFromCache is the synthetic response header added by the httpcache
// package for responses fulfilled from cache due to a 304 from the server.
const xFromCache = "X-From-Cache"

// githubRepoID is a github org & repo, lowercase.
type githubRepoID struct {
	Owner, Repo string
}

func (id githubRepoID) String() string { return id.Owner + "/" + id.Repo }

func (id githubRepoID) valid() bool {
	if id.Owner == "" || id.Repo == "" {
		// TODO: more validation. whatever github requires.
		return false
	}
	return true
}

type githubGlobal struct {
	c     *Corpus
	users map[int64]*githubUser
	repos map[githubRepoID]*githubRepo
}

func (g *githubGlobal) getOrCreateRepo(owner, repo string) *githubRepo {
	id := githubRepoID{owner, repo}
	if !id.valid() {
		return nil
	}
	r, ok := g.repos[id]
	if ok {
		return r
	}
	r = &githubRepo{
		github: g,
		id:     id,
		issues: map[int32]*githubIssue{},
	}
	g.repos[id] = r
	return r
}

type githubRepo struct {
	github     *githubGlobal
	id         githubRepoID
	issues     map[int32]*githubIssue // num -> issue
	milestones map[int64]*githubMilestone
	labels     map[int64]*githubLabel
}

func (g *githubRepo) getOrCreateMilestone(id int64, num int32, title string) *githubMilestone {
	if id == 0 {
		panic("zero id")
	}
	m, ok := g.milestones[id]
	if ok {
		return m
	}
	if g.milestones == nil {
		g.milestones = map[int64]*githubMilestone{}
	}
	m = &githubMilestone{
		ID: id,
		// TODO: use num?
		Title: title,
	}
	g.milestones[id] = m
	return m
}

func (g *githubRepo) getOrCreateLabel(lp *maintpb.GithubLabel) *githubLabel {
	id := lp.Id
	if id == 0 {
		panic("zero id")
	}
	lb, ok := g.labels[id]
	if ok {
		return lb
	}
	if g.labels == nil {
		g.labels = map[int64]*githubLabel{}
	}
	lb = &githubLabel{
		ID:   id,
		Name: lp.Name,
	}
	g.labels[id] = lb
	return lb
}

// githubUser represents a github user.
// It is a subset of https://developer.github.com/v3/users/#get-a-single-user
type githubUser struct {
	ID    int64
	Login string
}

// githubIssue represents a github issue.
// This is maintner's in-memory representation. It differs slightly
// from the API's *github.Issue type, notably in the lack of pointers
// for all fields.
// See https://developer.github.com/v3/issues/#get-a-single-issue
type githubIssue struct {
	ID        int64
	Number    int32
	NotExist  bool // if true, rest of fields should be ignored.
	Closed    bool
	Locked    bool
	User      *githubUser
	Assignees []*githubUser
	Created   time.Time
	Updated   time.Time
	ClosedAt  time.Time
	ClosedBy  *githubUser
	Title     string
	Body      string
	Milestone *githubMilestone       // nil for unknown, noMilestone for none
	Labels    map[int64]*githubLabel // label ID => label

	commentsUpdatedTil time.Time                // max comment modtime seen
	commentsSyncedAsOf time.Time                // as of server's Date header
	comments           map[int64]*githubComment // by comment.ID
	eventsSyncedAsOf   time.Time                // as of server's Date header
	events             map[int64]*githubEvent   // by event.ID
}

func (gi *githubIssue) getCreatedAt() time.Time {
	if gi == nil {
		return time.Time{}
	}
	return gi.Created
}

func (gi *githubIssue) getUpdatedAt() time.Time {
	if gi == nil {
		return time.Time{}
	}
	return gi.Updated
}

func (gi *githubIssue) getClosedAt() time.Time {
	if gi == nil {
		return time.Time{}
	}
	return gi.ClosedAt
}

// noMilestone is a sentinel value to explicitly mean no milestone.
var noMilestone = new(githubMilestone)

type githubLabel struct {
	ID   int64
	Name string
	// TODO: color?
}

type githubMilestone struct {
	ID    int64
	Title string
	// TODO: due date, etc?
}

type githubComment struct {
	ID      int64
	User    *githubUser
	Created time.Time
	Updated time.Time
	Body    string
}

// TODO: this struct is a little wide. change it to an interface
// instead?  Maybe later, if memory profiling suggests it would help.
type githubEvent struct {
	ID int64

	// Type is one of:
	// * labeled, unlabeled
	// * milestoned, demilestoned
	// * assigned, unassigned
	// * locked, unlocked
	// * closed
	// * referenced
	// * renamed
	Type string

	// OtherJSON optionally contains a JSON object of Github's API
	// response for any fields maintner was unable to extract at
	// the time. It is empty if maintner supported all the fields
	// when the mutation was created.
	OtherJSON string

	Created time.Time
	Actor   *githubUser

	Label               string      // for type: "unlabeled", "labeled"
	Assignee            *githubUser // for type "assigned", "unassigned"
	Assigner            *githubUser // for type "assigned", "unassigned"
	Milestone           string      // for type: "milestoned", "demilestoned"
	From, To            string      // for type: "renamed"
	CommitID, CommitURL string      // for type: "closed", "referenced" ... ?
}

// (requires corpus be locked for reads)
func (gi *githubIssue) commentsSynced() bool {
	if gi.NotExist {
		// Issue doesn't exist, so can't sync its non-issues,
		// so consider it done.
		return true
	}
	return gi.commentsSyncedAsOf.After(gi.Updated)
}

// (requires corpus be locked for reads)
func (gi *githubIssue) eventsSynced() bool {
	if gi.NotExist {
		// Issue doesn't exist, so can't sync its non-issues,
		// so consider it done.
		return true
	}
	return gi.eventsSyncedAsOf.After(gi.Updated)
}

func (c *Corpus) initGithub() {
	if c.github != nil {
		return
	}
	c.github = &githubGlobal{
		c:     c,
		repos: map[githubRepoID]*githubRepo{},
	}
}

func (c *Corpus) AddGithub(owner, repo, tokenFile string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initGithub()
	gr := c.github.getOrCreateRepo(owner, repo)
	if gr == nil {
		log.Fatalf("invalid github owner/repo %q/%q", owner, repo)
	}
	c.watchedGithubRepos = append(c.watchedGithubRepos, watchedGithubRepo{
		gr:        gr,
		tokenFile: tokenFile,
	})
}

type watchedGithubRepo struct {
	gr        *githubRepo
	tokenFile string
}

// g.c.mu must be held
func (g *githubGlobal) getUser(pu *maintpb.GithubUser) *githubUser {
	if pu == nil {
		return nil
	}
	if u := g.users[pu.Id]; u != nil {
		if pu.Login != "" && pu.Login != u.Login {
			u.Login = pu.Login
		}
		return u
	}
	if g.users == nil {
		g.users = make(map[int64]*githubUser)
	}
	u := &githubUser{
		ID:    pu.Id,
		Login: pu.Login,
	}
	g.users[pu.Id] = u
	return u
}

// newGithubUserProto creates a GithubUser with the minimum diff between
// existing and g. The return value is nil if there were no changes. existing
// may also be nil.
func newGithubUserProto(existing *githubUser, g *github.User) *maintpb.GithubUser {
	if g == nil {
		return nil
	}
	id := int64(g.GetID())
	if existing == nil {
		return &maintpb.GithubUser{
			Id:    id,
			Login: g.GetLogin(),
		}
	}
	hasChanges := false
	u := &maintpb.GithubUser{Id: id}
	if login := g.GetLogin(); existing.Login != login {
		u.Login = login
		hasChanges = true
	}
	// Add more fields here
	if hasChanges {
		return u
	}
	return nil
}

// deletedAssignees returns an array of user ID's that are present in existing
// but not present in new.
func deletedAssignees(existing []*githubUser, new []*github.User) []int64 {
	mp := make(map[int64]bool, len(existing))
	for _, u := range new {
		id := int64(u.GetID())
		mp[id] = true
	}
	toDelete := []int64{}
	for _, u := range existing {
		if _, ok := mp[u.ID]; !ok {
			toDelete = append(toDelete, u.ID)
		}
	}
	return toDelete
}

// newAssignees returns an array of diffs between existing and new. New users in
// new will be present in the returned array in their entirety. Modified users
// will appear containing only the ID field and changed fields. Unmodified users
// will not appear in the returned array.
func newAssignees(existing []*githubUser, new []*github.User) []*maintpb.GithubUser {
	mp := make(map[int64]*githubUser, len(existing))
	for _, u := range existing {
		mp[u.ID] = u
	}
	changes := []*maintpb.GithubUser{}
	for _, u := range new {
		if existingUser, ok := mp[int64(u.GetID())]; ok {
			diffUser := &maintpb.GithubUser{
				Id: int64(u.GetID()),
			}
			hasDiff := false
			if login := u.GetLogin(); existingUser.Login != login {
				diffUser.Login = login
				hasDiff = true
			}
			// check more User fields for diffs here, as we add them to the proto

			if hasDiff {
				changes = append(changes, diffUser)
			}
		} else {
			changes = append(changes, &maintpb.GithubUser{
				Id:    int64(u.GetID()),
				Login: u.GetLogin(),
			})
		}
	}
	return changes
}

// setAssigneesFromProto returns a new array of assignees according to the
// instructions in new (adds or modifies users in existing), and toDelete
// (deletes them). c.mu must be held.
func (g *githubGlobal) setAssigneesFromProto(existing []*githubUser, new []*maintpb.GithubUser, toDelete []int64) []*githubUser {
	c := g.c
	mp := make(map[int64]*githubUser)
	for _, u := range existing {
		mp[u.ID] = u
	}
	for _, u := range new {
		if existingUser, ok := mp[u.Id]; ok {
			if u.Login != "" {
				existingUser.Login = u.Login
			}
			// TODO: add other fields here when we add them for user.
		} else {
			c.debugf("adding assignee %q", u.Login)
			existing = append(existing, g.getUser(u))
		}
	}
	// IDs to delete, in descending order
	idxsToDelete := []int{}
	// this is quadratic but the number of assignees is very unlikely to exceed,
	// say, 5.
	for _, id := range toDelete {
		for i, u := range existing {
			if u.ID == id {
				idxsToDelete = append([]int{i}, idxsToDelete...)
			}
		}
	}
	for _, idx := range idxsToDelete {
		existing = append(existing[:idx], existing[idx+1:]...)
	}
	return existing
}

// githubIssueDiffer generates a minimal diff (protobuf mutation) to
// get a Github Issue from its in-memory state 'a' to the current
// github API state 'b'.
type githubIssueDiffer struct {
	gr *githubRepo
	a  *githubIssue  // may be nil if no current state
	b  *github.Issue // may NOT be nil
}

func (d githubIssueDiffer) verbose() bool {
	return d.gr.github != nil && d.gr.github.c != nil && d.gr.github.c.Verbose
}

// returns nil if no changes.
func (d githubIssueDiffer) Diff() *maintpb.GithubIssueMutation {
	var changed bool
	m := &maintpb.GithubIssueMutation{
		Owner:  d.gr.id.Owner,
		Repo:   d.gr.id.Repo,
		Number: int32(d.b.GetNumber()),
	}
	for _, f := range issueDiffMethods {
		if f(d, m) {
			if d.verbose() {
				fname := strings.TrimPrefix(runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name(), "golang.org/x/build/maintner.githubIssueDiffer.")
				log.Printf("Issue %d changed: %v", d.b.GetNumber(), fname)
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return m
}

// issueDiffMethods are the different steps githubIssueDiffer.Diff
// goes through to compute a diff. The methods should return true if
// any change was made. The order is irrelevant unless otherwise
// documented in comments in the list below.
var issueDiffMethods = []func(githubIssueDiffer, *maintpb.GithubIssueMutation) bool{
	githubIssueDiffer.diffCreatedAt,
	githubIssueDiffer.diffUpdatedAt,
	githubIssueDiffer.diffUser,
	githubIssueDiffer.diffBody,
	githubIssueDiffer.diffTitle,
	githubIssueDiffer.diffMilestone,
	githubIssueDiffer.diffAssignees,
	githubIssueDiffer.diffClosedState,
	githubIssueDiffer.diffClosedAt,
	githubIssueDiffer.diffClosedBy,
	githubIssueDiffer.diffLockedState,
	githubIssueDiffer.diffLabels,
}

func (d githubIssueDiffer) diffCreatedAt(m *maintpb.GithubIssueMutation) bool {
	return d.diffTimeField(&m.Created, d.a.getCreatedAt(), d.b.GetCreatedAt())
}

func (d githubIssueDiffer) diffUpdatedAt(m *maintpb.GithubIssueMutation) bool {
	return d.diffTimeField(&m.Updated, d.a.getUpdatedAt(), d.b.GetUpdatedAt())
}

func (d githubIssueDiffer) diffClosedAt(m *maintpb.GithubIssueMutation) bool {
	return d.diffTimeField(&m.ClosedAt, d.a.getClosedAt(), d.b.GetClosedAt())
}

func (d githubIssueDiffer) diffTimeField(dst **timestamp.Timestamp, memTime, githubTime time.Time) bool {
	if githubTime.IsZero() || memTime.Equal(githubTime) {
		return false
	}
	tproto, err := ptypes.TimestampProto(githubTime)
	if err != nil {
		panic(err)
	}
	*dst = tproto
	return true
}

func (d githubIssueDiffer) diffUser(m *maintpb.GithubIssueMutation) bool {
	var existing *githubUser
	if d.a != nil {
		existing = d.a.User
	}
	m.User = newGithubUserProto(existing, d.b.User)
	return m.User != nil
}

func (d githubIssueDiffer) diffClosedBy(m *maintpb.GithubIssueMutation) bool {
	var existing *githubUser
	if d.a != nil {
		existing = d.a.ClosedBy
	}
	m.ClosedBy = newGithubUserProto(existing, d.b.ClosedBy)
	return m.ClosedBy != nil
}

func (d githubIssueDiffer) diffBody(m *maintpb.GithubIssueMutation) bool {
	if d.a != nil && d.a.Body == d.b.GetBody() {
		return false
	}
	m.Body = d.b.GetBody()
	return true
}

func (d githubIssueDiffer) diffTitle(m *maintpb.GithubIssueMutation) bool {
	if d.a != nil && d.a.Title == d.b.GetTitle() {
		return false
	}
	m.Title = d.b.GetTitle()
	return true
}

func (d githubIssueDiffer) diffMilestone(m *maintpb.GithubIssueMutation) bool {
	if d.a != nil && d.a.Milestone != nil {
		ma, mb := d.a.Milestone, d.b.Milestone
		if ma == noMilestone && d.b.Milestone == nil {
			// Unchanged. Still no milestone.
			return false
		}
		if mb != nil && ma.ID == int64(mb.GetID()) {
			// Unchanged. Same milestone.
			// TODO: detect milestone renames and emit mutation for that?
			return false
		}

	}
	if mb := d.b.Milestone; mb != nil {
		m.MilestoneId = int64(mb.GetID())
		m.MilestoneNum = int64(mb.GetNumber())
		m.MilestoneTitle = mb.GetTitle()
	} else {
		m.NoMilestone = true
	}
	return true
}

func (d githubIssueDiffer) diffAssignees(m *maintpb.GithubIssueMutation) bool {
	if d.a == nil {
		m.Assignees = newAssignees(nil, d.b.Assignees)
		return true
	}
	m.Assignees = newAssignees(d.a.Assignees, d.b.Assignees)
	m.DeletedAssignees = deletedAssignees(d.a.Assignees, d.b.Assignees)
	return len(m.Assignees) > 0 || len(m.DeletedAssignees) > 0
}

func (d githubIssueDiffer) diffLabels(m *maintpb.GithubIssueMutation) bool {
	// Common case: no changes. Return false quickly without allocations.
	if d.a != nil && len(d.a.Labels) == len(d.b.Labels) {
		missing := false
		for _, gl := range d.b.Labels {
			if _, ok := d.a.Labels[int64(gl.GetID())]; !ok {
				missing = true
				break
			}
		}
		if !missing {
			return false
		}
	}

	toAdd := map[int64]*maintpb.GithubLabel{}
	for _, gl := range d.b.Labels {
		id := int64(gl.GetID())
		if id == 0 {
			panic("zero label ID")
		}
		toAdd[id] = &maintpb.GithubLabel{Id: id, Name: gl.GetName()}
	}

	var toDelete []int64
	if d.a != nil {
		for id := range d.a.Labels {
			if _, ok := toAdd[id]; ok {
				// Already had it.
				delete(toAdd, id)
			} else {
				// We had it, but no longer.
				toDelete = append(toDelete, id)
			}
		}
	}

	m.RemoveLabel = toDelete
	for _, labpb := range toAdd {
		m.AddLabel = append(m.AddLabel, labpb)
	}

	return len(m.RemoveLabel) > 0 || len(m.AddLabel) > 0
}

func (d githubIssueDiffer) diffClosedState(m *maintpb.GithubIssueMutation) bool {
	bclosed := d.b.GetState() == "closed"
	if d.a != nil && d.a.Closed == bclosed {
		return false
	}
	m.Closed = &maintpb.BoolChange{Val: bclosed}
	return true
}

func (d githubIssueDiffer) diffLockedState(m *maintpb.GithubIssueMutation) bool {
	if d.a != nil && d.a.Locked == d.b.GetLocked() {
		return false
	}
	if d.a == nil && !d.b.GetLocked() {
		return false
	}
	m.Locked = &maintpb.BoolChange{Val: d.b.GetLocked()}
	return true
}

// newMutationFromIssue generates a GithubIssueMutation using the
// smallest possible diff between a (the state we have in memory in
// the corpus) and b (the current github API state).
//
// If newMutationFromIssue returns nil, the provided github.Issue is no newer
// than the data we have in the corpus. 'a'. may be nil.
func (r *githubRepo) newMutationFromIssue(a *githubIssue, b *github.Issue) *maintpb.Mutation {
	if b == nil || b.Number == nil {
		panic(fmt.Sprintf("github issue with nil number: %#v", b))
	}
	gim := githubIssueDiffer{gr: r, a: a, b: b}.Diff()
	if gim == nil {
		// No changes.
		return nil
	}
	return &maintpb.Mutation{GithubIssue: gim}
}

func (r *githubRepo) missingIssues() []int32 {
	c := r.github.c
	c.mu.RLock()
	defer c.mu.RUnlock()

	var maxNum int32
	for num := range r.issues {
		if num > maxNum {
			maxNum = num
		}
	}

	var missing []int32
	for num := int32(1); num < maxNum; num++ {
		if _, ok := r.issues[num]; !ok {
			missing = append(missing, num)
		}
	}
	return missing
}

// processGithubIssueMutation updates the corpus with the information in m, and
// returns true if the Corpus was modified.
func (c *Corpus) processGithubIssueMutation(m *maintpb.GithubIssueMutation) {
	if c == nil {
		panic("nil corpus")
	}
	c.initGithub()
	gr := c.github.getOrCreateRepo(m.Owner, m.Repo)
	if gr == nil {
		log.Printf("bogus Owner/Repo %q/%q in mutation: %v", m.Owner, m.Repo, m)
		return
	}
	if m.Number == 0 {
		log.Printf("bogus zero Number in mutation: %v", m)
		return
	}
	gi, ok := gr.issues[m.Number]
	if !ok {
		gi = &githubIssue{
			// User added below
			Number: m.Number,
			ID:     m.Id,
		}
		if gr.issues == nil {
			gr.issues = make(map[int32]*githubIssue)
		}
		gr.issues[m.Number] = gi

		if m.NotExist {
			gi.NotExist = true
			return
		}

		var err error
		gi.Created, err = ptypes.Timestamp(m.Created)
		if err != nil {
			panic(err)
		}
	}
	if m.NotExist != gi.NotExist {
		gi.NotExist = m.NotExist
	}
	if gi.NotExist {
		return
	}

	// Check Updated before all other fields so they don't update if this
	// Mutation is stale
	// (ignoring Created since it *should* never update)
	if m.Updated != nil {
		t, err := ptypes.Timestamp(m.Updated)
		if err != nil {
			panic(err)
		}
		gi.Updated = t
	}
	if m.ClosedAt != nil {
		t, err := ptypes.Timestamp(m.ClosedAt)
		if err != nil {
			panic(err)
		}
		gi.ClosedAt = t
	}
	if m.User != nil {
		gi.User = c.github.getUser(m.User)
	}
	if m.NoMilestone {
		gi.Milestone = noMilestone
	} else if m.MilestoneId != 0 {
		gi.Milestone = gr.getOrCreateMilestone(m.MilestoneId, int32(m.MilestoneNum), m.MilestoneTitle)
	}
	if m.ClosedBy != nil {
		gi.ClosedBy = c.github.getUser(m.ClosedBy)
	}
	if b := m.Closed; b != nil {
		gi.Closed = b.Val
	}
	if b := m.Locked; b != nil {
		gi.Locked = b.Val
	}

	gi.Assignees = c.github.setAssigneesFromProto(gi.Assignees, m.Assignees, m.DeletedAssignees)

	if m.Body != "" {
		gi.Body = m.Body
	}
	if m.Title != "" {
		gi.Title = m.Title
	}
	if len(m.RemoveLabel) > 0 || len(m.AddLabel) > 0 {
		if gi.Labels == nil {
			gi.Labels = make(map[int64]*githubLabel)
		}
		for _, lid := range m.RemoveLabel {
			delete(gi.Labels, lid)
		}
		for _, lpb := range m.AddLabel {
			gi.Labels[lpb.Id] = gr.getOrCreateLabel(lpb)
		}
	}

	for _, cmut := range m.Comment {
		if cmut.Id == 0 {
			log.Printf("Ignoring bogus comment mutation lacking Id: %v", cmut)
			continue
		}
		gc, ok := gi.comments[cmut.Id]
		if !ok {
			if gi.comments == nil {
				gi.comments = make(map[int64]*githubComment)
			}
			gc = &githubComment{ID: cmut.Id}
			gi.comments[gc.ID] = gc
		}
		if cmut.User != nil {
			gc.User = c.github.getUser(cmut.User)
		}
		if cmut.Created != nil {
			gc.Created, _ = ptypes.Timestamp(cmut.Created)
			gc.Created = gc.Created.UTC()
		}
		if cmut.Updated != nil {
			gc.Updated, _ = ptypes.Timestamp(cmut.Updated)
			gc.Created = gc.Created.UTC()
		}
		if cmut.Body != "" {
			gc.Body = cmut.Body
		}
	}
	if m.CommentStatus != nil && m.CommentStatus.ServerDate != nil {
		if serverDate, err := ptypes.Timestamp(m.CommentStatus.ServerDate); err == nil {
			gi.commentsSyncedAsOf = serverDate.UTC()
		}
	}
}

// githubCache is an httpcache.Cache wrapper that only
// stores responses for:
//   * https://api.github.com/repos/$OWNER/$REPO/issues?direction=desc&page=1&sort=updated
//   * https://api.github.com/repos/$OWNER/$REPO/milestones?page=1
//   * https://api.github.com/repos/$OWNER/$REPO/labels?page=1
type githubCache struct {
	httpcache.Cache
}

var rxGithubCacheURLs = regexp.MustCompile(`^https://api.github.com/repos/\w+/\w+/(issues|milestones|labels)\?(.+)`)

func cacheableURL(urlStr string) bool {
	m := rxGithubCacheURLs.FindStringSubmatch(urlStr)
	if m == nil {
		return false
	}
	v, _ := url.ParseQuery(m[2])
	if v.Get("page") != "1" {
		return false
	}
	switch m[1] {
	case "issues":
		return v.Get("sort") == "updated" && v.Get("direction") == "desc"
	case "milestones", "labels":
		return true
	default:
		panic("unexpected cache key base " + m[1])
	}
}

func (c *githubCache) Set(urlKey string, res []byte) {
	// TODO: verify that the httpcache package guarantees that the
	// first string parameter to Set here is actually a
	// URL. Empirically they appear to be.
	if cacheableURL(urlKey) {
		c.Cache.Set(urlKey, res)
	}
}

// PollGithubLoop checks for new changes on a single Github repository and
// updates the Corpus with any changes.
func (gr *githubRepo) PollGithubLoop(ctx context.Context, tokenFile string) error {
	slurp, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		return err
	}
	f := strings.SplitN(strings.TrimSpace(string(slurp)), ":", 2)
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return fmt.Errorf("Expected token file %s to be of form <username>:<token>", tokenFile)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: f[1]})
	tc := oauth2.NewClient(ctx, ts)
	tc.Transport = &httpcache.Transport{
		Transport:           tc.Transport, // underlying oauth2 transport
		Cache:               &githubCache{Cache: httpcache.NewMemoryCache()},
		MarkCachedResponses: true, // adds "X-From-Cache: 1" response header.
	}

	p := &githubRepoPoller{
		c:   gr.github.c,
		gr:  gr,
		ghc: github.NewClient(tc),
	}
	for {
		err := p.sync(ctx)
		p.logf("sync = %v; sleeping", err)
		if err == context.Canceled {
			return err
		}
		select {
		case <-time.After(10 * time.Second):
			continue
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// A githubRepoPoller updates the Corpus (gr.c) to have the latest
// version of the Github repo rp, using the Github client ghc.
type githubRepoPoller struct {
	c   *Corpus // shortcut for gr.github.c
	gr  *githubRepo
	ghc *github.Client
}

func (p *githubRepoPoller) Owner() string { return p.gr.id.Owner }
func (p *githubRepoPoller) Repo() string  { return p.gr.id.Repo }

func (p *githubRepoPoller) logf(format string, args ...interface{}) {
	log.Printf("sync github "+p.gr.id.String()+": "+format, args...)
}

func (p *githubRepoPoller) sync(ctx context.Context) error {
	p.logf("Beginning sync.")
	if err := p.syncIssues(ctx); err != nil {
		return err
	}
	if err := p.syncComments(ctx); err != nil {
		return err
	}
	if err := p.syncEvents(ctx); err != nil {
		return err
	}
	return nil
}

func (p *githubRepoPoller) syncMilestones(ctx context.Context) error {
	return p.foreachItem(ctx, p.getMilestonePage, func(e interface{}) error {
		m := e.(*github.Milestone)
		log.Printf("Milestone %v: %s", m.GetID(), m)
		return nil
	})
}

func (p *githubRepoPoller) syncLabels(ctx context.Context) error {
	return p.foreachItem(ctx, p.getLabelPage, func(e interface{}) error {
		lb := e.(*github.Label)
		log.Printf("Label %v: %v", lb.GetID(), lb.GetName())
		return nil
	})
}

func (p *githubRepoPoller) getMilestonePage(ctx context.Context, page int) ([]interface{}, *github.Response, error) {
	ms, res, err := p.ghc.Issues.ListMilestones(ctx, p.Owner(), p.Repo(), &github.MilestoneListOptions{
		State:       "all",
		ListOptions: github.ListOptions{Page: page},
	})
	if err != nil {
		return nil, nil, err
	}
	its := make([]interface{}, len(ms))
	for i, m := range ms {
		its[i] = m
	}
	return its, res, err
}

func (p *githubRepoPoller) getLabelPage(ctx context.Context, page int) ([]interface{}, *github.Response, error) {
	ls, res, err := p.ghc.Issues.ListLabels(ctx, p.Owner(), p.Repo(), &github.ListOptions{
		Page: page,
	})
	if err != nil {
		return nil, nil, err
	}
	its := make([]interface{}, len(ls))
	for i, lb := range ls {
		its[i] = lb
	}
	return its, res, err
}

// foreach walks over all pages of items from getPage and calls fn for each item.
// If the first page's response was cached, fn is never called.
func (p *githubRepoPoller) foreachItem(
	ctx context.Context,
	getPage func(ctx context.Context, page int) ([]interface{}, *github.Response, error),
	fn func(interface{}) error) error {
	page := 1
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		items, res, err := getPage(ctx, page)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		fromCache := page == 1 && res.Response.Header.Get(xFromCache) == "1"
		if fromCache {
			log.Printf("no new items of type %T", items[0])
			// No need to walk over these again.
			return nil
		}
		// TODO: use res.Rate (sleep until Reset if Limit == 0)
		for _, it := range items {
			if err := fn(it); err != nil {
				return err
			}
		}
		if res.NextPage == 0 {
			return nil
		}
		page = res.NextPage
	}
}

func (p *githubRepoPoller) syncIssues(ctx context.Context) error {
	c := p.gr.github.c
	page := 1
	seen := make(map[int64]bool)
	keepGoing := true
	owner, repo := p.gr.id.Owner, p.gr.id.Repo
	for keepGoing {
		issues, res, err := p.ghc.Issues.ListByRepo(ctx, owner, repo, &github.IssueListByRepoOptions{
			State:     "all",
			Sort:      "updated",
			Direction: "desc",
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: 100,
			},
		})
		if err != nil {
			return err
		}
		// See https://developer.github.com/v3/activity/events/ for X-Poll-Interval:
		if pi := res.Response.Header.Get("X-Poll-Interval"); pi != "" {
			nsec, _ := strconv.Atoi(pi)
			d := time.Duration(nsec) * time.Second
			p.logf("Requested to adjust poll interval to %v", d)
			// TODO: return an error type up that the sync loop can use
			// to adjust its default interval.
			// For now, ignore.
		}
		fromCache := res.Response.Header.Get(xFromCache) == "1"
		if len(issues) == 0 {
			p.logf("issues: reached end.")
			break
		}

		// If there's something new (not a cached response),
		// then check for updated milestones and labels before
		// creating issue mutations below. Doesn't matter
		// much, but helps to have it all loaded.
		if !fromCache {
			group, ctx := errgroup.WithContext(ctx)
			group.Go(func() error { return p.syncMilestones(ctx) })
			group.Go(func() error { return p.syncLabels(ctx) })
			if err := group.Wait(); err != nil {
				return err
			}
		}

		changes := 0
		for _, is := range issues {
			id := int64(is.GetID())
			if seen[id] {
				// If an issue gets updated (and bumped to the top) while we
				// are paging, it's possible the last issue from page N can
				// appear as the first issue on page N+1. Don't process that
				// issue twice.
				// https://github.com/google/go-github/issues/566
				continue
			}
			seen[id] = true

			var mp *maintpb.Mutation
			c.mu.RLock()
			{
				gi := p.gr.issues[int32(*is.Number)]
				mp = p.gr.newMutationFromIssue(gi, is)
			}
			c.mu.RUnlock()

			if mp == nil {
				continue
			}
			changes++
			p.logf("changed issue %d: %s", is.GetNumber(), is.GetTitle())
			c.processMutation(mp)
		}

		if changes == 0 {
			missing := p.gr.missingIssues()
			if len(missing) == 0 {
				p.logf("no changed issues; cached=%v", fromCache)
				return nil
			}
			if len(missing) > 0 {
				p.logf("%d missing github issues.", len(missing))
			}
			if len(missing) < 100 {
				keepGoing = false
			}
		}

		c.mu.RLock()
		num := len(p.gr.issues)
		c.mu.RUnlock()
		p.logf("After page %d: %v issues, %v changes, %v issues in memory", page, len(issues), changes, num)

		page++
	}

	missing := p.gr.missingIssues()
	if len(missing) > 0 {
		p.logf("remaining issues: %v", missing)
		for _, num := range missing {
			p.logf("getting issue %v ...", num)
			issue, _, err := p.ghc.Issues.Get(ctx, owner, repo, int(num))
			if ge, ok := err.(*github.ErrorResponse); ok && ge.Message == "Not Found" {
				mp := &maintpb.Mutation{
					GithubIssue: &maintpb.GithubIssueMutation{
						Owner:    owner,
						Repo:     repo,
						Number:   num,
						NotExist: true,
					},
				}
				c.processMutation(mp)
				continue
			} else if err != nil {
				return err
			}
			mp := p.gr.newMutationFromIssue(nil, issue)
			if mp == nil {
				continue
			}
			p.logf("modified issue %d: %s", issue.GetNumber(), issue.GetTitle())
			c.processMutation(mp)
		}
	}

	return nil
}

func (p *githubRepoPoller) issueNumbersWithStaleCommentSync() (issueNums []int32) {
	p.c.mu.RLock()
	defer p.c.mu.RUnlock()

	for n, gi := range p.gr.issues {
		if !gi.commentsSynced() {
			issueNums = append(issueNums, n)
		}
	}
	sort.Slice(issueNums, func(i, j int) bool {
		return issueNums[i] < issueNums[j]
	})
	return issueNums
}

func (p *githubRepoPoller) syncComments(ctx context.Context) error {
	for {
		nums := p.issueNumbersWithStaleCommentSync()
		if len(nums) == 0 {
			return nil
		}
		remain := len(nums)
		for _, num := range nums {
			p.logf("comment sync: %d issues remaining; syncing issue %v", remain, num)
			if err := p.syncCommentsOnIssue(ctx, num); err != nil {
				p.logf("comment sync on issue %d: %v", num, err)
				return err
			}
			remain--
		}
	}
	return nil
}

func (p *githubRepoPoller) syncCommentsOnIssue(ctx context.Context, issueNum int32) error {
	p.c.mu.RLock()
	issue := p.gr.issues[issueNum]
	if issue == nil {
		p.c.mu.RUnlock()
		return fmt.Errorf("unknown issue number %v", issueNum)
	}
	since := issue.commentsUpdatedTil
	p.c.mu.RUnlock()

	owner, repo := p.gr.id.Owner, p.gr.id.Repo
	morePages := true // at least try the first. might be empty.
	for morePages {
		ics, res, err := p.ghc.Issues.ListComments(ctx, owner, repo, int(issueNum), &github.IssueListCommentsOptions{
			Since:       since,
			Direction:   "asc",
			Sort:        "updated",
			ListOptions: github.ListOptions{PerPage: 100},
		})
		// TODO: use res.Rate.* (https://godoc.org/github.com/google/go-github/github#Rate) to sleep
		// and retry if we're out of tokens. Probably need to make an HTTP RoundTripper that does
		// that automatically.
		if err != nil {
			return err
		}
		serverDate, err := http.ParseTime(res.Header.Get("Date"))
		if err != nil {
			return fmt.Errorf("invalid server Date response: %v", err)
		}
		serverDate = serverDate.UTC()
		p.logf("Number of issue comments since %v: %v (res=%#v)", since, len(ics), res)

		mut := &maintpb.Mutation{
			GithubIssue: &maintpb.GithubIssueMutation{
				Owner:  owner,
				Repo:   repo,
				Number: issueNum,
			},
		}

		p.c.mu.RLock()
		for _, ic := range ics {
			if ic.ID == nil || ic.Body == nil || ic.User == nil || ic.CreatedAt == nil || ic.UpdatedAt == nil {
				// Bogus.
				p.logf("bogus comment: %v", ic)
				continue
			}
			created, err := ptypes.TimestampProto(*ic.CreatedAt)
			if err != nil {
				continue
			}
			updated, err := ptypes.TimestampProto(*ic.UpdatedAt)
			if err != nil {
				continue
			}
			since = *ic.UpdatedAt // for next round

			id := int64(*ic.ID)
			cur := issue.comments[id]

			// TODO: does a reaction update a comment's UpdatedAt time?
			var cmut *maintpb.GithubIssueCommentMutation
			if cur == nil {
				cmut = &maintpb.GithubIssueCommentMutation{
					Id: id,
					User: &maintpb.GithubUser{
						Id:    int64(*ic.User.ID),
						Login: *ic.User.Login,
					},
					Body:    *ic.Body,
					Created: created,
					Updated: updated,
				}
			} else if !cur.Updated.Equal(*ic.UpdatedAt) || cur.Body != *ic.Body {
				cmut = &maintpb.GithubIssueCommentMutation{
					Id: id,
				}
				if !cur.Updated.Equal(*ic.UpdatedAt) {
					cmut.Updated = updated
				}
				if cur.Body != *ic.Body {
					cmut.Body = *ic.Body
				}
			}
			if cmut != nil {
				mut.GithubIssue.Comment = append(mut.GithubIssue.Comment, cmut)
			}
		}
		p.c.mu.RUnlock()

		if res.NextPage == 0 {
			sdp, _ := ptypes.TimestampProto(serverDate)
			mut.GithubIssue.CommentStatus = &maintpb.GithubIssueCommentSyncStatus{ServerDate: sdp}
			morePages = false
		}

		p.c.processMutation(mut)
	}
	return nil
}

func (p *githubRepoPoller) issueNumbersWithStaleEventSync() (issueNums []int32) {
	p.c.mu.RLock()
	defer p.c.mu.RUnlock()

	for n, gi := range p.gr.issues {
		if !gi.eventsSynced() {
			issueNums = append(issueNums, n)
		}
	}
	sort.Slice(issueNums, func(i, j int) bool {
		return issueNums[i] < issueNums[j]
	})
	return issueNums
}

func (p *githubRepoPoller) syncEvents(ctx context.Context) error {
	return nil // TODO: enable

	for {
		nums := p.issueNumbersWithStaleEventSync()
		if len(nums) == 0 {
			p.logf("event sync: done.")
			return nil
		}
		remain := len(nums)
		for _, num := range nums {
			p.logf("event sync: %d issues remaining; syncing issue %v", remain, num)
			if err := p.syncEventsOnIssue(ctx, num); err != nil {
				p.logf("event sync on issue %d: %v", num, err)
				return err
			}
			remain--
		}
	}
	return nil
}

func (p *githubRepoPoller) syncEventsOnIssue(ctx context.Context, issueNum int32) error {

	return nil
}

// parseGithubEvents parses the JSON array of github events in r.  It
// does this the very manual way (using map[string]interface{})
// instead of using nice types because https://golang.org/issue/15314
// isn't implemented yet and also because even if it were implemented,
// this code still wants to preserve any unknown fields to store in
// the "OtherJSON" field for future updates of the code to parse. (If
// Github adds new Event types in the future, we want to archive them,
// even if we don't understand them)
func parseGithubEvents(r io.Reader) ([]*githubEvent, error) {
	var jevents []map[string]interface{}
	jd := json.NewDecoder(r)
	jd.UseNumber()
	if err := jd.Decode(&jevents); err != nil {
		return nil, err
	}
	var evts []*githubEvent
	for _, em := range jevents {
		for k, v := range em {
			if v == nil {
				delete(em, k)
			}
		}
		delete(em, "url")

		e := &githubEvent{}

		e.Type, _ = em["event"].(string)
		delete(em, "event")

		e.ID = jint64(em["id"])
		delete(em, "id")

		// TODO: store these two more compactly:
		e.CommitID, _ = em["commit_id"].(string) // "5383ecf5a0824649ffcc0349f00f0317575753d0"
		delete(em, "commit_id")
		e.CommitURL, _ = em["commit_url"].(string) // "https://api.github.com/repos/bradfitz/go-issue-mirror/commits/5383ecf5a0824649ffcc0349f00f0317575753d0"
		delete(em, "commit_url")

		getUser := func(field string, gup **githubUser) {
			am, ok := em[field].(map[string]interface{})
			if !ok {
				return
			}
			delete(em, field)
			gu := &githubUser{ID: jint64(am["id"])}
			gu.Login, _ = am["login"].(string)
			*gup = gu
		}

		getUser("actor", &e.Actor)
		getUser("assignee", &e.Assignee)
		getUser("assigner", &e.Assigner)

		if lm, ok := em["label"].(map[string]interface{}); ok {
			delete(em, "label")
			e.Label, _ = lm["name"].(string)
		}

		if mm, ok := em["milestone"].(map[string]interface{}); ok {
			delete(em, "milestone")
			e.Milestone, _ = mm["title"].(string)
		}

		if rm, ok := em["rename"].(map[string]interface{}); ok {
			delete(em, "rename")
			e.From, _ = rm["from"].(string)
			e.To, _ = rm["to"].(string)
		}

		if createdStr, ok := em["created_at"].(string); ok {
			delete(em, "created_at")
			var err error
			e.Created, err = time.Parse(time.RFC3339, createdStr)
			if err != nil {
				return nil, err
			}
			e.Created = e.Created.UTC()
		}

		otherJSON, _ := json.Marshal(em)
		e.OtherJSON = string(otherJSON)
		if e.OtherJSON == "{}" {
			e.OtherJSON = ""
		}
		if e.OtherJSON != "" {
			log.Fatalf("Unknown fields in event: %s", e.OtherJSON)
		}
		evts = append(evts, e)
	}
	return evts, nil
}

// jint64 return an int64 from the provided JSON object value v.
func jint64(v interface{}) int64 {
	switch v := v.(type) {
	case nil:
		return 0
	case json.Number:
		n, _ := strconv.ParseInt(string(v), 10, 64)
		return n
	default:
		panic(fmt.Sprintf("unexpected type %T", v))
	}
}
