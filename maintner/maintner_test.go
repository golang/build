// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"reflect"
	"testing"

	"golang.org/x/build/maintner/maintpb"
)

type mutationTest struct {
	corpus *Corpus
	want   *Corpus
}

func (mt mutationTest) test(t *testing.T, muts ...*maintpb.Mutation) {
	c := mt.corpus
	for _, m := range muts {
		c.processMutationLocked(m)
	}
	if !reflect.DeepEqual(c, mt.want) {
		t.Errorf("corpus mismatch\n got: %#v\nwant: %#v", c, mt.want)
	}
}

func TestProcessMutation_Github_NewIssue(t *testing.T) {
	mutationTest{
		want: &Corpus{
			githubUsers: map[int64]*githubUser{
				100: &githubUser{
					Login: "gopherbot",
					ID:    100,
				},
			},
			githubIssues: map[githubRepo]map[int32]*githubIssue{
				"golang/go": map[int32]*githubIssue{
					3: &githubIssue{
						Number: 3,
						User:   &githubUser{ID: 100, Login: "gopherbot"},
						Body:   "some body",
					},
				},
			},
		},
	}.test(t, &maintpb.Mutation{
		GithubIssue: &maintpb.GithubIssueMutation{
			Owner:  "golang",
			Repo:   "go",
			Number: 3,
			User: &maintpb.GithubUser{
				Login: "gopherbot",
				Id:    100,
			},
			Body: "some body",
		},
	})
}
