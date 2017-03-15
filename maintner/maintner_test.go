// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"reflect"
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes"
	google_protobuf "github.com/golang/protobuf/ptypes/timestamp"
	"github.com/google/go-github/github"
	"golang.org/x/build/maintner/maintpb"
)

var u1 = &githubUser{
	Login: "gopherbot",
	ID:    100,
}
var u2 = &githubUser{
	Login: "kevinburke",
	ID:    101,
}

type dummyMutationLogger struct {
	Mutations []*maintpb.Mutation
}

func (d *dummyMutationLogger) Log(m *maintpb.Mutation) error {
	if d.Mutations == nil {
		d.Mutations = []*maintpb.Mutation{}
	}
	d.Mutations = append(d.Mutations, m)
	return nil
}

type mutationTest struct {
	corpus *Corpus
	want   *Corpus
}

func (mt mutationTest) test(t *testing.T, muts ...*maintpb.Mutation) {
	c := mt.corpus
	if c == nil {
		c = NewCorpus(&dummyMutationLogger{})
	}
	for _, m := range muts {
		c.processMutationLocked(m)
	}
	if !reflect.DeepEqual(c, mt.want) {
		t.Errorf("corpus mismatch\n got: %#v\nwant: %#v", c, mt.want)
	}
}

var t1, t2 time.Time
var tp1, tp2 *google_protobuf.Timestamp

func init() {
	t1, _ = time.Parse(time.RFC3339, "2016-01-02T15:04:00Z")
	t2, _ = time.Parse(time.RFC3339, "2016-01-02T15:30:00Z")
	tp1, _ = ptypes.TimestampProto(t1)
	tp2, _ = ptypes.TimestampProto(t2)
}

func TestProcessMutation_Github_NewIssue(t *testing.T) {
	c := NewCorpus(&dummyMutationLogger{})
	c.githubUsers = map[int64]*githubUser{
		u1.ID: u1,
	}
	c.githubIssues = map[githubRepo]map[int32]*githubIssue{
		"golang/go": map[int32]*githubIssue{
			3: &githubIssue{
				Number:    3,
				User:      u1,
				Title:     "some title",
				Body:      "some body",
				Created:   t1,
				Assignees: nil,
			},
		},
	}
	mutationTest{want: c}.test(t, &maintpb.Mutation{
		GithubIssue: &maintpb.GithubIssueMutation{
			Owner:  "golang",
			Repo:   "go",
			Number: 3,
			User: &maintpb.GithubUser{
				Login: "gopherbot",
				Id:    100,
			},
			Title:   "some title",
			Body:    "some body",
			Created: tp1,
		},
	})
}

func TestProcessMutation_OldIssue(t *testing.T) {
	// process a mutation with an Updated timestamp older than the existing
	// issue.
	c := NewCorpus(&dummyMutationLogger{})
	c.githubUsers = map[int64]*githubUser{
		u1.ID: u1,
	}
	c.githubIssues = map[githubRepo]map[int32]*githubIssue{
		"golang/go": map[int32]*githubIssue{
			3: &githubIssue{
				Number:    3,
				User:      u1,
				Body:      "some body",
				Created:   t2,
				Updated:   t2,
				Assignees: nil,
			},
		},
	}
	mutationTest{want: c}.test(t, &maintpb.Mutation{
		GithubIssue: &maintpb.GithubIssueMutation{
			Owner:  "golang",
			Repo:   "go",
			Number: 3,
			User: &maintpb.GithubUser{
				Login: "gopherbot",
				Id:    100,
			},
			Body:    "some body",
			Created: tp2,
			Updated: tp2,
		},
	}, &maintpb.Mutation{
		// The second issue is older than the first and should be ignored.
		GithubIssue: &maintpb.GithubIssueMutation{
			Owner:  "golang",
			Repo:   "go",
			Number: 3,
			User: &maintpb.GithubUser{
				Login: "gopherbot",
				Id:    100,
			},
			Body:    "issue body changed",
			Created: tp1,
			Updated: tp1,
		},
	})
}

func TestNewMutationsFromIssue(t *testing.T) {
	gh := &github.Issue{
		Number:    github.Int(5),
		CreatedAt: &t1,
		UpdatedAt: &t2,
		Body:      github.String("body of the issue"),
		State:     github.String("closed"),
	}
	is := newMutationFromIssue(nil, gh, githubRepo("golang/go"))
	want := &maintpb.Mutation{GithubIssue: &maintpb.GithubIssueMutation{
		Owner:     "golang",
		Repo:      "go",
		Number:    5,
		Body:      "body of the issue",
		Created:   tp1,
		Updated:   tp2,
		Assignees: []*maintpb.GithubUser{},
	}}
	if !reflect.DeepEqual(is, want) {
		t.Errorf("issue mismatch\n got: %#v\nwant: %#v", is, want)
	}
}

func TestNewAssigneesHandlesNil(t *testing.T) {
	users := []*github.User{
		&github.User{Login: github.String("foo"), ID: github.Int(3)},
	}
	got := newAssignees(nil, users)
	want := []*maintpb.GithubUser{&maintpb.GithubUser{
		Id:    3,
		Login: "foo",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assignee mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestAssigneesDeleted(t *testing.T) {
	c := NewCorpus(&dummyMutationLogger{})
	c.githubUsers = map[int64]*githubUser{
		u1.ID: u1,
	}
	assignees := []*githubUser{u1, u2}
	issue := &githubIssue{
		Number:    3,
		User:      u1,
		Body:      "some body",
		Created:   t2,
		Updated:   t2,
		Assignees: assignees,
	}
	c.githubIssues = map[githubRepo]map[int32]*githubIssue{
		"golang/go": map[int32]*githubIssue{
			3: issue,
		},
	}
	repo := githubRepo("golang/go")
	mutation := newMutationFromIssue(issue, &github.Issue{
		Number:    github.Int(3),
		Assignees: []*github.User{&github.User{ID: github.Int(int(u2.ID))}},
	}, repo)
	c.processMutation(mutation)
	gi, _ := c.getIssue(repo, 3)
	if len(gi.Assignees) != 1 || gi.Assignees[0].ID != u2.ID {
		t.Errorf("expected u1 to be deleted, got %v", gi.Assignees)
	}
}

func TestGithubRepoOrg(t *testing.T) {
	gr := githubRepo("golang/go")
	want := "golang"
	if org := gr.Org(); org != want {
		t.Errorf("githubRepo(\"%s\").Org(): got %s, want %s", gr, org, want)
	}
	gr = githubRepo("unknown format")
	want = ""
	if org := gr.Org(); org != want {
		t.Errorf("githubRepo(\"%s\").Org(): got %s, want %s", gr, org, want)
	}
}

func TestGithubRepo(t *testing.T) {
	gr := githubRepo("golang/go")
	want := "go"
	if repo := gr.Repo(); repo != want {
		t.Errorf("githubRepo(\"%s\").Repo(): got %s, want %s", gr, repo, want)
	}
	gr = githubRepo("bad/")
	want = ""
	if repo := gr.Repo(); repo != want {
		t.Errorf("githubRepo(\"%s\").Repo(): got %s, want %s", gr, repo, want)
	}
}
