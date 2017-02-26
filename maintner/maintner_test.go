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

type mutationTest struct {
	corpus *Corpus
	want   *Corpus
}

func (mt mutationTest) test(t *testing.T, muts ...*maintpb.Mutation) {
	c := mt.corpus
	if c == nil {
		c = NewCorpus()
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
	c := NewCorpus()
	c.githubUsers = map[int64]*githubUser{
		100: &githubUser{
			Login: "gopherbot",
			ID:    100,
		},
	}
	c.githubIssues = map[githubRepo]map[int32]*githubIssue{
		"golang/go": map[int32]*githubIssue{
			3: &githubIssue{
				Number:  3,
				User:    &githubUser{ID: 100, Login: "gopherbot"},
				Body:    "some body",
				Created: t1,
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
			Created: tp1,
		},
	})
}

func TestProcessMutation_OldIssue(t *testing.T) {
	// process a mutation with an Updated timestamp older than the existing
	// issue.
	c := NewCorpus()
	c.githubUsers = map[int64]*githubUser{
		100: &githubUser{
			Login: "gopherbot",
			ID:    100,
		},
	}
	c.githubIssues = map[githubRepo]map[int32]*githubIssue{
		"golang/go": map[int32]*githubIssue{
			3: &githubIssue{
				Number:  3,
				User:    &githubUser{ID: 100, Login: "gopherbot"},
				Body:    "some body",
				Created: t2,
				Updated: t2,
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
		// The second issue is older than the first.
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
	want := &maintpb.GithubIssueMutation{
		Owner:   "golang",
		Repo:    "go",
		Number:  5,
		Body:    "body of the issue",
		Created: tp1,
		Updated: tp2,
	}
	if !reflect.DeepEqual(is, want) {
		t.Errorf("issue mismatch\n got: %#v\nwant: %#v", is, want)
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
