// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/google/go-github/github"

	"golang.org/x/build/maintner/maintpb"
	"golang.org/x/oauth2"
)

// githubRepo is a github org & repo, lowercase, joined by a '/',
// such as "golang/go".
type githubRepo string

// Org finds "golang" in the githubRepo string "golang/go", or returns an empty
// string if it is malformed.
func (gr githubRepo) Org() string {
	sep := strings.IndexByte(string(gr), '/')
	if sep == -1 {
		return ""
	}
	return string(gr[:sep])
}

func (gr githubRepo) Repo() string {
	sep := strings.IndexByte(string(gr), '/')
	if sep == -1 || sep == len(gr)-1 {
		return ""
	}
	return string(gr[sep+1:])
}

func (c *Corpus) repoKey(owner, repo string) githubRepo {
	if owner == "" || repo == "" {
		return ""
	}
	// TODO: avoid garbage, use interned strings? profile later
	// once we have gigabytes of mutation logs to slurp at
	// start-up. (The same thing mattered for Camlistore start-up
	// time at least)
	return githubRepo(owner + "/" + repo)
}

// githubUser represents a github user.
// It is a subset of https://developer.github.com/v3/users/#get-a-single-user
type githubUser struct {
	ID    int64
	Login string
}

// githubIssue represents a github issue.
// See https://developer.github.com/v3/issues/#get-a-single-issue
type githubIssue struct {
	ID        int64
	Number    int32
	NotExist  bool // if true, rest of fields should be ignored.
	Closed    bool
	User      *githubUser
	Assignees []*githubUser
	Created   time.Time
	Updated   time.Time
	Title     string
	Body      string

	commentsUpdatedTil time.Time                // max comment modtime seen
	commentsSyncedAsOf time.Time                // as of server's Date header
	comments           map[int64]*githubComment // by comment.ID
}

type githubComment struct {
	ID      int64
	User    *githubUser
	Created time.Time
	Updated time.Time
	Body    string
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

func (c *Corpus) AddGithub(owner, repo, tokenFile string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watchedGithubRepos = append(c.watchedGithubRepos, watchedGithubRepo{
		name:      githubRepo(owner + "/" + repo),
		tokenFile: tokenFile,
	})
}

type watchedGithubRepo struct {
	name      githubRepo
	tokenFile string
}

// c.mu must be held
func (c *Corpus) getGithubUser(pu *maintpb.GithubUser) *githubUser {
	if pu == nil {
		return nil
	}
	if u := c.githubUsers[pu.Id]; u != nil {
		if pu.Login != "" && pu.Login != u.Login {
			u.Login = pu.Login
		}
		return u
	}
	if c.githubUsers == nil {
		c.githubUsers = make(map[int64]*githubUser)
	}
	u := &githubUser{
		ID:    pu.Id,
		Login: pu.Login,
	}
	c.githubUsers[pu.Id] = u
	return u
}

// newGithubUserProto creates a GithubUser with the minimum diff between
// existing and g. The return value is nil if there were no changes. existing
// may also be nil.
func newGithubUserProto(existing *maintpb.GithubUser, g *github.User) *maintpb.GithubUser {
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
// instructions in new (adds or modifies users in existing ), and toDelete
// (deletes them). c.mu must be held.
func (c *Corpus) setAssigneesFromProto(existing []*githubUser, new []*maintpb.GithubUser, toDelete []int64) ([]*githubUser, bool) {
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
			existing = append(existing, c.getGithubUser(u))
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
		c.debugf("deleting assignee %q", existing[idx].Login)
		existing = append(existing[:idx], existing[idx+1:]...)
	}
	return existing, len(toDelete) > 0 || len(new) > 0
}

// newMutationFromIssue generates a GithubIssueMutation using the smallest
// possible diff between ci (a corpus Issue) and gi (an external github issue).
//
// If newMutationFromIssue returns nil, the provided github.Issue is no newer
// than the data we have in the corpus. ci may be nil.
func newMutationFromIssue(ci *githubIssue, gi *github.Issue, rp githubRepo) *maintpb.Mutation {
	if gi == nil || gi.Number == nil {
		panic(fmt.Sprintf("github issue with nil number: %#v", gi))
	}
	owner, repo := rp.Org(), rp.Repo()
	// always need these fields to figure out which key to write to
	m := &maintpb.GithubIssueMutation{
		Owner:  owner,
		Repo:   repo,
		Number: int32(gi.GetNumber()),
	}
	if ci == nil {
		// We don't know about this github issue, so populate all fields in one
		// mutation.
		if gi.CreatedAt != nil {
			tproto, err := ptypes.TimestampProto(gi.GetCreatedAt())
			if err != nil {
				panic(err)
			}
			m.Created = tproto
		}
		if gi.UpdatedAt != nil {
			tproto, err := ptypes.TimestampProto(gi.GetUpdatedAt())
			if err != nil {
				panic(err)
			}
			m.Updated = tproto
		}
		m.Body = gi.GetBody()
		m.Title = gi.GetTitle()
		if gi.User != nil {
			m.User = newGithubUserProto(nil, gi.User)
		}
		m.Assignees = newAssignees(nil, gi.Assignees)
		// no deleted assignees on first run
		return &maintpb.Mutation{GithubIssue: m}
	}
	if gi.UpdatedAt != nil {
		if !gi.UpdatedAt.After(ci.Updated) {
			// This data is stale, ignore it.
			return nil
		}
		tproto, err := ptypes.TimestampProto(gi.GetUpdatedAt())
		if err != nil {
			panic(err)
		}
		m.Updated = tproto
	}
	if body := gi.GetBody(); body != ci.Body {
		m.Body = body
	}
	if title := gi.GetTitle(); title != ci.Title {
		m.Title = title
	}
	if gi.User != nil {
		m.User = newGithubUserProto(m.User, gi.User)
	}
	m.Assignees = newAssignees(ci.Assignees, gi.Assignees)
	m.DeletedAssignees = deletedAssignees(ci.Assignees, gi.Assignees)
	return &maintpb.Mutation{GithubIssue: m}
}

// getIssue finds an issue in the Corpus or returns nil, false if it is not
// present.
func (c *Corpus) getIssue(rp githubRepo, number int32) (*githubIssue, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	gi, ok := c.githubIssues[rp][number]
	return gi, ok
}

func (c *Corpus) githubMissingIssues(rp githubRepo) []int32 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	issues := c.githubIssues[rp]

	var maxNum int32
	for num := range issues {
		if num > maxNum {
			maxNum = num
		}
	}

	var missing []int32
	for num := int32(1); num < maxNum; num++ {
		if _, ok := issues[num]; !ok {
			missing = append(missing, num)
		}
	}
	return missing
}

// processGithubIssueMutation updates the corpus with the information in m, and
// returns true if the Corpus was modified.
func (c *Corpus) processGithubIssueMutation(m *maintpb.GithubIssueMutation) (changed bool) {
	if c == nil {
		panic("nil corpus")
	}
	k := c.repoKey(m.Owner, m.Repo)
	if k == "" {
		// TODO: errors? return false? skip for now.
		return
	}
	if m.Number == 0 {
		return
	}
	issueMap, ok := c.githubIssues[k]
	if !ok {
		if c.githubIssues == nil {
			c.githubIssues = make(map[githubRepo]map[int32]*githubIssue)
		}
		issueMap = make(map[int32]*githubIssue)
		c.githubIssues[k] = issueMap
	}
	gi, ok := issueMap[m.Number]
	if !ok {
		changed = true
		gi = &githubIssue{
			// User added below
			Number: m.Number,
			ID:     m.Id,
		}
		issueMap[m.Number] = gi

		if m.NotExist {
			gi.NotExist = true
			return changed
		}

		var err error
		gi.Created, err = ptypes.Timestamp(m.Created)
		if err != nil {
			panic(err)
		}
	}
	if m.NotExist != gi.NotExist {
		changed = true
		gi.NotExist = m.NotExist
	}
	if gi.NotExist {
		return changed
	}

	// Check Updated before all other fields so they don't update if this
	// Mutation is stale
	// (ignoring Created since it *should* never update)
	if m.Updated != nil {
		updated, err := ptypes.Timestamp(m.Updated)
		if err != nil {
			panic(err)
		}
		if !updated.IsZero() && updated.Before(gi.Updated) {
			// this mutation represents data older than the data we have in
			// the corpus; ignore it.
			return false
		}
		changed = changed || updated.After(gi.Updated)
		gi.Updated = updated
	}
	if m.User != nil {
		gi.User = c.getGithubUser(m.User)
	}

	gi.Assignees, ok = c.setAssigneesFromProto(gi.Assignees, m.Assignees, m.DeletedAssignees)
	changed = changed || ok

	if m.Body != "" {
		changed = changed || m.Body != gi.Body
		gi.Body = m.Body
	}
	if m.Title != "" {
		changed = changed || m.Title != gi.Title
		gi.Title = m.Title
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
			gc.User = c.getGithubUser(cmut.User)
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

	return changed
}

// PollGithubLoop checks for new changes on a single Github repository and
// updates the Corpus with any changes.
func (c *Corpus) PollGithubLoop(ctx context.Context, rp githubRepo, tokenFile string) error {
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

	p := &githubRepoPoller{
		c:   c,
		ghc: github.NewClient(tc),
		rp:  rp,
	}
	for {
		err := p.sync(ctx)
		p.logf("sync = %v; sleeping", err)
		if err == context.Canceled {
			return err
		}
		select {
		case <-time.After(30 * time.Second):
			continue
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// A githubRepoPoller updates the Corpus c to have the latest version
// of the Github repo rp, using the Github client ghc.
type githubRepoPoller struct {
	c   *Corpus
	rp  githubRepo
	ghc *github.Client
}

func (p *githubRepoPoller) logf(format string, args ...interface{}) {
	log.Printf("sync github "+string(p.rp)+": "+format, args...)
}

func (p *githubRepoPoller) sync(ctx context.Context) error {
	p.logf("Beginning sync.")
	if err := p.syncIssues(ctx); err != nil {
		return err
	}
	if err := p.syncComments(ctx); err != nil {
		return err
	}
	// TODO: events
	return nil
}

func (p *githubRepoPoller) syncIssues(ctx context.Context) error {
	c, rp := p.c, p.rp
	page := 1
	seen := make(map[int64]bool)
	keepGoing := true
	owner, repo := p.rp.Org(), p.rp.Repo()
	for keepGoing {
		// TODO: use https://godoc.org/github.com/google/go-github/github#ActivityService.ListIssueEventsForRepository probably
		issues, _, err := p.ghc.Issues.ListByRepo(ctx, owner, repo, &github.IssueListByRepoOptions{
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
		p.logf("issues: page %d, num issues %d", page, len(issues))
		if len(issues) == 0 {
			p.logf("isses: reached end.")
			break
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
			gi, _ := c.getIssue(rp, int32(*is.Number))
			mp := newMutationFromIssue(gi, is, rp)
			if mp == nil {
				continue
			}
			changes++
			p.logf("changed issue %d: %s", is.GetNumber(), is.GetTitle())
			c.processMutation(mp)
		}

		c.mu.RLock()
		num := len(c.githubIssues[rp])
		c.mu.RUnlock()
		p.logf("%v issues (%v changes this page)", num, changes)

		if changes == 0 {
			missing := c.githubMissingIssues(rp)
			p.logf("%d missing github issues.", len(missing))
			if len(missing) < 100 {
				keepGoing = false
			}
		}
		page++
	}

	missing := c.githubMissingIssues(rp)
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
			mp := newMutationFromIssue(nil, issue, rp)
			if mp == nil {
				continue
			}
			p.logf("modified issue %d: %s", issue.GetNumber(), issue.GetTitle())
			p.c.processMutation(mp)
		}
	}

	return nil
}

func (p *githubRepoPoller) issueNumbersWithStaleCommentSync() (issueNums []int32) {
	p.c.mu.RLock()
	defer p.c.mu.RUnlock()
	issues := p.c.githubIssues[p.rp]
	for n, gi := range issues {
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
			p.logf("comment sync: done.")
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
	issue := p.c.githubIssues[p.rp][issueNum]
	if issue == nil {
		p.c.mu.RUnlock()
		return fmt.Errorf("unknown issue number %v", issueNum)
	}
	since := issue.commentsUpdatedTil
	p.c.mu.RUnlock()

	owner, repo := p.rp.Org(), p.rp.Repo()
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
