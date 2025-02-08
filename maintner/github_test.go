// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v48/github"
	"golang.org/x/build/maintner/maintpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestParseGithubEvents(t *testing.T) {
	tests := []struct {
		name string                    // test
		j    string                    // JSON from Github API
		e    *GitHubIssueEvent         // in-memory
		p    *maintpb.GithubIssueEvent // on disk
	}{
		{
			name: "labeled",
			j: `{
    "id": 998144526,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144526",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "labeled",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "label": {
      "name": "enhancement",
      "color": "84b6eb"
    }
  }
`,
			e: &GitHubIssueEvent{
				ID:      998144526,
				Type:    "labeled",
				Created: t3339("2017-03-13T22:39:28Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Label: "enhancement",
			},
			p: &maintpb.GithubIssueEvent{
				Id:        998144526,
				EventType: "labeled",
				ActorId:   2621,
				Created:   p3339("2017-03-13T22:39:28Z"),
				Label:     &maintpb.GithubLabel{Name: "enhancement"},
			},
		},

		{
			name: "unlabeled",
			j: `{
    "id": 998144526,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144526",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "unlabeled",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "label": {
      "name": "enhancement",
      "color": "84b6eb"
    }
  }
`,
			e: &GitHubIssueEvent{
				ID:      998144526,
				Type:    "unlabeled",
				Created: t3339("2017-03-13T22:39:28Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Label: "enhancement",
			},
			p: &maintpb.GithubIssueEvent{
				Id:        998144526,
				EventType: "unlabeled",
				ActorId:   2621,
				Created:   p3339("2017-03-13T22:39:28Z"),
				Label:     &maintpb.GithubLabel{Name: "enhancement"},
			},
		},

		{
			name: "milestoned",
			j: `{
    "id": 998144529,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144529",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "milestoned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "milestone": {
      "title": "World Domination"
    }}`,
			e: &GitHubIssueEvent{
				ID:      998144529,
				Type:    "milestoned",
				Created: t3339("2017-03-13T22:39:28Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Milestone: "World Domination",
			},
			p: &maintpb.GithubIssueEvent{
				Id:        998144529,
				EventType: "milestoned",
				ActorId:   2621,
				Created:   p3339("2017-03-13T22:39:28Z"),
				Milestone: &maintpb.GithubMilestone{Title: "World Domination"},
			},
		},

		{
			name: "demilestoned",
			j: `{
    "id": 998144529,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144529",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "demilestoned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "milestone": {
      "title": "World Domination"
    }}`,
			e: &GitHubIssueEvent{
				ID:      998144529,
				Type:    "demilestoned",
				Created: t3339("2017-03-13T22:39:28Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Milestone: "World Domination",
			},
			p: &maintpb.GithubIssueEvent{
				Id:        998144529,
				EventType: "demilestoned",
				ActorId:   2621,
				Created:   p3339("2017-03-13T22:39:28Z"),
				Milestone: &maintpb.GithubMilestone{Title: "World Domination"},
			},
		},

		{
			name: "assigned",
			j: `{
    "id": 998144530,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144530",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "assigned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "assignee": {
      "login": "bradfitz",
      "id": 2621
    },
    "assigner": {
      "login": "bradfitz",
      "id": 2621
    }}`,
			e: &GitHubIssueEvent{
				ID:      998144530,
				Type:    "assigned",
				Created: t3339("2017-03-13T22:39:28Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Assignee: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Assigner: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
			},
			p: &maintpb.GithubIssueEvent{
				Id:         998144530,
				EventType:  "assigned",
				ActorId:    2621,
				Created:    p3339("2017-03-13T22:39:28Z"),
				AssigneeId: 2621,
				AssignerId: 2621,
			},
		},

		{
			name: "unassigned",
			j: `{
    "id": 1000077586,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1000077586",
    "actor": {
      "login": "dmitshur",
      "id": 1924134
    },
    "event": "unassigned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-15T00:31:42Z",
    "assignee": {
      "login": "dmitshur",
      "id": 1924134
    },
    "assigner": {
      "login": "bradfitz",
      "id": 2621
    }
  }`,
			e: &GitHubIssueEvent{
				ID:      1000077586,
				Type:    "unassigned",
				Created: t3339("2017-03-15T00:31:42Z"),
				Actor: &GitHubUser{
					ID:    1924134,
					Login: "dmitshur",
				},
				Assignee: &GitHubUser{
					ID:    1924134,
					Login: "dmitshur",
				},
				Assigner: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
			},
			p: &maintpb.GithubIssueEvent{
				Id:         1000077586,
				EventType:  "unassigned",
				ActorId:    1924134,
				Created:    p3339("2017-03-15T00:31:42Z"),
				AssigneeId: 1924134,
				AssignerId: 2621,
			},
		},

		{
			name: "locked",
			j: `{
    "id": 998144646,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144646",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "locked",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:36Z",
    "lock_reason": "off-topic"
  }`,
			e: &GitHubIssueEvent{
				ID:      998144646,
				Type:    "locked",
				Created: t3339("2017-03-13T22:39:36Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
			},
			p: &maintpb.GithubIssueEvent{
				Id:        998144646,
				EventType: "locked",
				ActorId:   2621,
				Created:   p3339("2017-03-13T22:39:36Z"),
			},
		},

		{
			name: "unlocked",
			j: `{
    "id": 1000014895,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1000014895",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "unlocked",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T23:26:21Z"
 }`,
			e: &GitHubIssueEvent{
				ID:      1000014895,
				Type:    "unlocked",
				Created: t3339("2017-03-14T23:26:21Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
			},
			p: &maintpb.GithubIssueEvent{
				Id:        1000014895,
				EventType: "unlocked",
				ActorId:   2621,
				Created:   p3339("2017-03-14T23:26:21Z"),
			},
		},

		{
			name: "closed",
			j: `  {
    "id": 1006040931,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1006040931",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "closed",
    "commit_id": "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
    "commit_url": "https://api.github.com/repos/bradfitz/go-issue-mirror/commits/e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
    "created_at": "2017-03-19T23:40:33Z"
  }`,
			e: &GitHubIssueEvent{
				ID:      1006040931,
				Type:    "closed",
				Created: t3339("2017-03-19T23:40:33Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				CommitID:  "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				CommitURL: "https://api.github.com/repos/bradfitz/go-issue-mirror/commits/e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
			},
			p: &maintpb.GithubIssueEvent{
				Id:        1006040931,
				EventType: "closed",
				ActorId:   2621,
				Created:   p3339("2017-03-19T23:40:33Z"),
				Commit: &maintpb.GithubCommit{
					Owner:    "bradfitz",
					Repo:     "go-issue-mirror",
					CommitId: "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				},
			},
		},

		{
			name: "reopened",
			j: `{
    "id": 1000014895,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1000014895",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "reopened",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T23:26:21Z"
 }`,
			e: &GitHubIssueEvent{
				ID:      1000014895,
				Type:    "reopened",
				Created: t3339("2017-03-14T23:26:21Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
			},
			p: &maintpb.GithubIssueEvent{
				Id:        1000014895,
				EventType: "reopened",
				ActorId:   2621,
				Created:   p3339("2017-03-14T23:26:21Z"),
			},
		},

		{
			name: "referenced",
			j: `{
    "id": 1006040930,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1006040930",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "referenced",
    "commit_id": "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
    "commit_url": "https://api.github.com/repos/bradfitz/go-issue-mirror/commits/e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
    "created_at": "2017-03-19T23:40:32Z"
  }`,
			e: &GitHubIssueEvent{
				ID:      1006040930,
				Type:    "referenced",
				Created: t3339("2017-03-19T23:40:32Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				CommitID:  "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				CommitURL: "https://api.github.com/repos/bradfitz/go-issue-mirror/commits/e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
			},
			p: &maintpb.GithubIssueEvent{
				Id:        1006040930,
				EventType: "referenced",
				ActorId:   2621,
				Created:   p3339("2017-03-19T23:40:32Z"),
				Commit: &maintpb.GithubCommit{
					Owner:    "bradfitz",
					Repo:     "go-issue-mirror",
					CommitId: "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				},
			},
		},

		{
			name: "renamed",
			j: `{
    "id": 1006107803,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1006107803",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "renamed",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-20T02:53:43Z",
    "rename": {
      "from": "test-2",
      "to": "test-2 new name"
    }
  }`,
			e: &GitHubIssueEvent{
				ID:      1006107803,
				Type:    "renamed",
				Created: t3339("2017-03-20T02:53:43Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				From: "test-2",
				To:   "test-2 new name",
			},
			p: &maintpb.GithubIssueEvent{
				Id:         1006107803,
				EventType:  "renamed",
				ActorId:    2621,
				Created:    p3339("2017-03-20T02:53:43Z"),
				RenameFrom: "test-2",
				RenameTo:   "test-2 new name",
			},
		},

		{
			name: "Extra Unknown JSON",
			j: `{
    "id": 998144526,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144526",
    "actor": {
      "login": "bradfitz",
      "id": 2621
    },
    "event": "labeled",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "label": {
      "name": "enhancement",
      "color": "84b6eb"
    },
    "random_new_field": "some new thing that GitHub API may add"
  }
`,
			e: &GitHubIssueEvent{
				ID:      998144526,
				Type:    "labeled",
				Created: t3339("2017-03-13T22:39:28Z"),
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Label:     "enhancement",
				OtherJSON: `{"random_new_field":"some new thing that GitHub API may add"}`,
			},
			p: &maintpb.GithubIssueEvent{
				Id:        998144526,
				EventType: "labeled",
				ActorId:   2621,
				Created:   p3339("2017-03-13T22:39:28Z"),
				Label:     &maintpb.GithubLabel{Name: "enhancement"},
				OtherJson: []byte(`{"random_new_field":"some new thing that GitHub API may add"}`),
			},
		},
	}

	var eventTypes []string

	for _, tt := range tests {
		evts, err := parseGithubEvents(strings.NewReader("[" + tt.j + "]"))
		if err != nil {
			t.Errorf("%s: parse JSON: %v", tt.name, err)
			continue
		}
		if len(evts) != 1 {
			t.Errorf("%s: parse JSON = %v entries; want 1", tt.name, len(evts))
			continue
		}
		gote := evts[0]
		if !reflect.DeepEqual(gote, tt.e) {
			t.Errorf("%s: JSON -> githubEvent differs: %v", tt.name, DeepDiff(gote, tt.e))
			continue
		}
		eventTypes = append(eventTypes, gote.Type)

		gotp := gote.Proto()
		if !reflect.DeepEqual(gotp, tt.p) {
			t.Errorf("%s: githubEvent -> proto differs: %v", tt.name, DeepDiff(gotp, tt.p))
			continue
		}

		var c Corpus
		c.initGithub()
		c.github.getOrCreateUserID(2621).Login = "bradfitz"
		c.github.getOrCreateUserID(1924134).Login = "dmitshur"
		gr := c.github.getOrCreateRepo("foowner", "bar")
		e2 := gr.newGithubEvent(gotp)

		if !reflect.DeepEqual(e2, tt.e) {
			t.Errorf("%s: proto -> githubEvent differs: %v", tt.name, DeepDiff(e2, tt.e))
			continue
		}
	}

	t.Logf("Tested event types: %q", eventTypes)
}

func TestParseMultipleGithubEvents(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "TestParseMultipleGithubEvents.json"))
	if err != nil {
		t.Errorf("error while loading testdata: %s\n", err.Error())
	}
	evts, err := parseGithubEvents(strings.NewReader(string(content)))
	if err != nil {
		t.Errorf("error was not expected: %s\n", err.Error())
	}
	if len(evts) != 7 {
		t.Errorf("there should have been three events. was: %d\n", len(evts))
	}
	lastEvent := evts[len(evts)-1]
	if lastEvent.Type != "closed" {
		t.Errorf("the last event's type should have been closed. was: %s\n", lastEvent.Type)
	}
}

func TestParseMultipleGithubEventsWithForeach(t *testing.T) {
	issue := &GitHubIssue{
		PullRequest: true,
		events: map[int64]*GitHubIssueEvent{
			0: &GitHubIssueEvent{
				Type: "labelled",
			},
			1: &GitHubIssueEvent{
				Type: "milestone",
			},
			2: &GitHubIssueEvent{
				Type: "closed",
			},
		},
	}
	eventTypes := []string{"closed", "labelled", "milestone"}
	gatheredTypes := make([]string, 0)
	issue.ForeachEvent(func(e *GitHubIssueEvent) error {
		gatheredTypes = append(gatheredTypes, e.Type)
		return nil
	})
	sort.Strings(gatheredTypes)
	if !reflect.DeepEqual(eventTypes, gatheredTypes) {
		t.Fatalf("want event types: %v; got: %v\n", eventTypes, gatheredTypes)
	}
}

type ClientMock struct {
	err        error
	status     string
	statusCode int
	testdata   string
}

var timesDoWasCalled = 0

func (c *ClientMock) Do(req *http.Request) (*http.Response, error) {
	if len(c.testdata) < 1 {
		c.testdata = "TestParseMultipleGithubEvents.json"
	}
	timesDoWasCalled++
	content, _ := os.ReadFile(filepath.Join("testdata", c.testdata))
	headers := make(http.Header, 0)
	t := time.Now()
	var b []byte
	headers["Date"] = []string{string(t.AppendFormat(b, "Mon Jan _2 15:04:05 2006"))}
	return &http.Response{
		Body:       io.NopCloser(bytes.NewReader(content)),
		Status:     c.status,
		StatusCode: c.statusCode,
		Header:     headers,
	}, c.err
}

type MockLogger struct {
}

var eventLog = make([]string, 0)

func (m *MockLogger) Log(mut *maintpb.Mutation) error {
	for _, e := range mut.GithubIssue.Event {
		eventLog = append(eventLog, e.EventType)
	}
	return nil
}

func TestSyncEvents(t *testing.T) {
	var c Corpus
	c.initGithub()
	c.mutationLogger = &MockLogger{}
	gr := c.github.getOrCreateRepo("foowner", "bar")
	issue := &GitHubIssue{
		ID:          1001,
		PullRequest: true,
		events:      map[int64]*GitHubIssueEvent{},
	}
	gr.issues = map[int32]*GitHubIssue{
		1001: issue,
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Write([]byte("OK"))
	}))
	defer server.Close()
	p := &githubRepoPoller{
		c:             &c,
		token:         "foobar",
		gr:            gr,
		githubDirect:  github.NewClient(server.Client()),
		githubCaching: github.NewClient(server.Client()),
	}
	t.Run("successful sync", func(t2 *testing.T) {
		defer func() { eventLog = make([]string, 0) }()
		timesDoWasCalled = 0
		ctx := context.Background()
		p.client = &ClientMock{
			status:     "OK",
			statusCode: http.StatusOK,
			err:        nil,
			testdata:   "TestParseMultipleGithubEvents.json",
		}
		err := p.syncEventsOnIssue(ctx, int32(issue.ID))
		if err != nil {
			t2.Error(err)
		}
		want := []string{"labeled", "labeled", "labeled", "labeled", "labeled", "milestoned", "closed"}
		if !reflect.DeepEqual(want, eventLog) {
			t2.Errorf("want: %v; got: %v\n", want, eventLog)
		}

		wantTimesCalled := 1
		if timesDoWasCalled != wantTimesCalled {
			t.Errorf("client.Do should have been called %d times. got: %d\n", wantTimesCalled, timesDoWasCalled)
		}
	})
	t.Run("successful sync missing milestones", func(t2 *testing.T) {
		defer func() { eventLog = make([]string, 0) }()
		timesDoWasCalled = 0
		ctx := context.Background()
		p.client = &ClientMock{
			status:     "OK",
			statusCode: http.StatusOK,
			err:        nil,
			testdata:   "TestMissingMilestoneEvents.json",
		}
		err := p.syncEventsOnIssue(ctx, int32(issue.ID))
		if err != nil {
			t2.Error(err)
		}
		want := []string{"mentioned", "subscribed", "mentioned", "subscribed", "assigned", "labeled", "labeled", "milestoned", "renamed", "demilestoned", "milestoned"}
		sort.Strings(want)
		sort.Strings(eventLog)
		if !reflect.DeepEqual(want, eventLog) {
			t2.Errorf("want: %v; got: %v\n", want, eventLog)
		}

		wantTimesCalled := 1
		if timesDoWasCalled != wantTimesCalled {
			t.Errorf("client.Do should have been called %d times. got: %d\n", wantTimesCalled, timesDoWasCalled)
		}
	})
}

func TestSyncMultipleConsecutiveEvents(t *testing.T) {
	var c Corpus
	c.initGithub()
	c.mutationLogger = &MockLogger{}
	gr := c.github.getOrCreateRepo("foowner", "bar")
	issue := &GitHubIssue{
		ID:          1001,
		PullRequest: true,
		events:      map[int64]*GitHubIssueEvent{},
	}
	gr.issues = map[int32]*GitHubIssue{
		1001: issue,
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Write([]byte("OK"))
	}))
	defer server.Close()
	p := &githubRepoPoller{
		c:             &c,
		token:         "foobar",
		gr:            gr,
		githubDirect:  github.NewClient(server.Client()),
		githubCaching: github.NewClient(server.Client()),
	}
	t.Run("successful sync", func(t2 *testing.T) {
		defer func() { eventLog = make([]string, 0) }()
		timesDoWasCalled = 0
		ctx := context.Background()
		for i := 1; i < 5; i++ {
			testdata := fmt.Sprintf("TestParseMultipleGithubEvents_p%d.json", i)
			p.client = &ClientMock{
				status:     "OK",
				statusCode: http.StatusOK,
				err:        nil,
				testdata:   testdata,
			}
			err := p.syncEventsOnIssue(ctx, int32(issue.ID))
			if err != nil {
				t2.Fatal(err)
			}
		}

		want := []string{"labeled", "labeled", "labeled", "labeled", "labeled", "milestoned", "closed"}
		if !reflect.DeepEqual(want, eventLog) {
			t2.Errorf("want: %v; got: %v\n", want, eventLog)
		}

		wantTimesCalled := 4
		if timesDoWasCalled != wantTimesCalled {
			t.Errorf("client.Do should have been called %d times. got: %d\n", wantTimesCalled, timesDoWasCalled)
		}
	})
}

func TestParseGitHubReviews(t *testing.T) {
	tests := []struct {
		name string                // test
		j    string                // JSON from Github API
		e    *GitHubReview         // in-memory
		p    *maintpb.GithubReview // on disk
	}{
		{
			name: "Approved",
			j: `{
				"id": 123456,
				"node_id": "548913adsafas84asdf48a",
				"user": {
					"login": "bradfitz",
					"id": 2621
				},
				"body": "I approve this commit",
				"state": "APPROVED",
				"html_url": "https://github.com/bradfitz/go-issue-mirror/pull/21",
				"pull_request_url": "https://github.com/bradfitz/go-issue-mirror/pull/21",
				"author_association": "CONTRIBUTOR",
				"_links":{
					"html":{
						"href": "https://github.com/bradfitz/go-issue-mirror/pull/21"
					},
					"pull_request":{
						"href": "https://github.com/bradfitz/go-issue-mirror/pull/21"
					}
				},
				"submitted_at": "2018-03-22T00:26:48Z",
				"commit_id" : "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126"
				}`,
			e: &GitHubReview{
				ID: 123456,
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Body:             "I approve this commit",
				State:            "APPROVED",
				CommitID:         "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				ActorAssociation: "CONTRIBUTOR",
				Created:          t3339("2018-03-22T00:26:48Z"),
			},
			p: &maintpb.GithubReview{
				Id:               123456,
				ActorId:          2621,
				Body:             "I approve this commit",
				State:            "APPROVED",
				CommitId:         "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				ActorAssociation: "CONTRIBUTOR",
				Created:          p3339("2018-03-22T00:26:48Z"),
			},
		},
		{
			name: "Extra Unknown JSON",
			j: `{
				"id": 123456,
				"node_id": "548913adsafas84asdf48a",
				"user": {
					"login": "bradfitz",
					"id": 2621
				},
				"body": "I approve this commit",
				"state": "APPROVED",
				"html_url": "https://github.com/bradfitz/go-issue-mirror/pull/21",
				"pull_request_url": "https://github.com/bradfitz/go-issue-mirror/pull/21",
				"author_association": "CONTRIBUTOR",
				"_links":{
					"html":{
						"href": "https://github.com/bradfitz/go-issue-mirror/pull/21"
					},
					"pull_request":{
						"href": "https://github.com/bradfitz/go-issue-mirror/pull/21"
					}
				},
				"submitted_at": "2018-03-22T00:26:48Z",
				"commit_id" : "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				"random_new_field": "some new thing that GitHub API may add"
				}`,
			e: &GitHubReview{
				ID: 123456,
				Actor: &GitHubUser{
					ID:    2621,
					Login: "bradfitz",
				},
				Body:             "I approve this commit",
				State:            "APPROVED",
				CommitID:         "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				ActorAssociation: "CONTRIBUTOR",
				Created:          t3339("2018-03-22T00:26:48Z"),
				OtherJSON:        `{"random_new_field":"some new thing that GitHub API may add"}`,
			},
			p: &maintpb.GithubReview{
				Id:               123456,
				ActorId:          2621,
				Body:             "I approve this commit",
				State:            "APPROVED",
				CommitId:         "e4d70f7e8892f024e4ed3e8b99ee6c5a9f16e126",
				ActorAssociation: "CONTRIBUTOR",
				Created:          p3339("2018-03-22T00:26:48Z"),
				OtherJson:        []byte(`{"random_new_field":"some new thing that GitHub API may add"}`),
			},
		},
	}

	for _, tt := range tests {
		evts, err := parseGithubReviews(strings.NewReader("[" + tt.j + "]"))
		if err != nil {
			t.Errorf("%s: parse JSON: %v", tt.name, err)
			continue
		}
		if len(evts) != 1 {
			t.Errorf("%s: parse JSON = %v entries; want 1", tt.name, len(evts))
			continue
		}
		gote := evts[0]
		if !reflect.DeepEqual(gote, tt.e) {
			t.Errorf("%s: JSON -> githubReviewEvent differs: %v", tt.name, DeepDiff(gote, tt.e))
			continue
		}

		gotp := gote.Proto()
		if !reflect.DeepEqual(gotp, tt.p) {
			t.Errorf("%s: githubReviewEvent -> proto differs: %v", tt.name, DeepDiff(gotp, tt.p))
			continue
		}

		var c Corpus
		c.initGithub()
		c.github.getOrCreateUserID(2621).Login = "bradfitz"
		c.github.getOrCreateUserID(1924134).Login = "dmitshur"
		gr := c.github.getOrCreateRepo("foowner", "bar")
		e2 := gr.newGithubReview(gotp)

		if !reflect.DeepEqual(e2, tt.e) {
			t.Errorf("%s: proto -> githubReviewEvent differs: %v", tt.name, DeepDiff(e2, tt.e))
			continue
		}
	}
}

func TestForeachRepo(t *testing.T) {
	tests := []struct {
		name    string
		issue   *GitHubIssue
		want    []string
		wantErr error
	}{
		{
			name: "Skips non-PullRequests",
			issue: &GitHubIssue{
				PullRequest: false,
			},
			want:    []string{},
			wantErr: nil,
		},
		{
			name: "Processes Multiple in Order",
			issue: &GitHubIssue{
				PullRequest: true,
				reviews: map[int64]*GitHubReview{
					0: &GitHubReview{
						Body:    "Second",
						Created: t3339("2018-04-22T00:26:48Z"),
					},
					1: &GitHubReview{
						Body:    "First",
						Created: t3339("2018-03-22T00:26:48Z"),
					},
				},
			},
			want:    []string{"First", "Second"},
			wantErr: nil,
		},
		{
			name: "Will Error",
			issue: &GitHubIssue{
				PullRequest: true,
				reviews: map[int64]*GitHubReview{
					0: &GitHubReview{
						Body: "Fail",
					},
				},
			},
			want:    []string{},
			wantErr: fmt.Errorf("Planned Failure"),
		},
		{
			name: "Will Error Late",
			issue: &GitHubIssue{
				PullRequest: true,
				reviews: map[int64]*GitHubReview{
					0: &GitHubReview{
						Body:    "First Event",
						Created: t3339("2018-03-22T00:26:48Z"),
					},
					1: &GitHubReview{
						Body:    "Fail",
						Created: t3339("2018-04-22T00:26:48Z"),
					},
					2: &GitHubReview{
						Body:    "Third Event",
						Created: t3339("2018-05-22T00:26:48Z"),
					},
				},
			},
			want:    []string{"First Event"},
			wantErr: fmt.Errorf("Planned Failure"),
		}}

	for _, tt := range tests {
		got := make([]string, 0)

		err := tt.issue.ForeachReview(func(r *GitHubReview) error {
			if r.Body == "Fail" {
				return fmt.Errorf("Planned Failure")
			}
			got = append(got, r.Body)
			return nil
		})

		if !equalError(tt.wantErr, err) {
			t.Errorf("%s: ForeachReview errs differ. got: %s, want: %s", tt.name, err, tt.wantErr)
		}

		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: ForeachReview calls differ. got: %s want: %s", tt.name, got, tt.want)
		}
	}

	t.Log("Tested Reviews")
}

// equalError reports whether errors a and b are considered equal.
// They're equal if both are nil, or both are not nil and a.Error() == b.Error().
func equalError(a, b error) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Error() == b.Error()
}

func TestCacheableURL(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"https://api.github.com/repos/OWNER/RePO/milestones?page=1", true},
		{"https://api.github.com/repos/OWNER/RePO/milestones?page=2", false},
		{"https://api.github.com/repos/OWNER/RePO/milestones?", false},
		{"https://api.github.com/repos/OWNER/RePO/milestones", false},

		{"https://api.github.com/repos/OWNER/RePO/labels?page=1", true},
		{"https://api.github.com/repos/OWNER/RePO/labels?page=2", false},
		{"https://api.github.com/repos/OWNER/RePO/labels?", false},
		{"https://api.github.com/repos/OWNER/RePO/labels", false},

		{"https://api.github.com/repos/OWNER/RePO/foos?page=1", false},

		{"https://api.github.com/repos/OWNER/RePO/issues?page=1", false},
		{"https://api.github.com/repos/OWNER/RePO/issues?page=1&sort=updated&direction=desc", true},
	}

	for _, tt := range tests {
		got := cacheableURL(tt.v)
		if got != tt.want {
			t.Errorf("cacheableURL(%q) = %v; want %v", tt.v, got, tt.want)
		}
	}
}

func t3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func p3339(s string) *timestamppb.Timestamp {
	return timestamppb.New(t3339(s))
}

func TestParseGithubRefs(t *testing.T) {
	tests := []struct {
		gerritProj string // "go.googlesource.com/go", etc
		msg        string
		want       []string
	}{
		{"go.googlesource.com/go", "\nFixes #1234\n", []string{"golang/go#1234"}},
		{"go.googlesource.com/go", "Fixes #1234\n", []string{"golang/go#1234"}},
		{"go.googlesource.com/go", "Fixes #1234", []string{"golang/go#1234"}},
		{"go.googlesource.com/go", "Fixes golang/go#1234", []string{"golang/go#1234"}},
		{"go.googlesource.com/go", "Fixes golang/go#1234\n", []string{"golang/go#1234"}},
		{"go.googlesource.com/go", "Fixes golang/go#1234.", []string{"golang/go#1234"}},
		{"go.googlesource.com/go", "Mention issue #1234 a second time.\n\nFixes #1234.", []string{"golang/go#1234"}},
		{"go.googlesource.com/go", "Mention issue #1234 a second time.\n\nFixes #1234.\nUpdates #1235.", []string{"golang/go#1234", "golang/go#1235"}},
		{"go.googlesource.com/net", "Fixes golang/go#1234.", []string{"golang/go#1234"}},
		{"go.googlesource.com/net", "Fixes #1234", nil},
	}
	for _, tt := range tests {
		c := new(Corpus)
		var got []string
		for _, ref := range c.parseGithubRefs(tt.gerritProj, tt.msg) {
			got = append(got, ref.String())
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseGithubRefs(%q, %q) = %q; want %q", tt.gerritProj, tt.msg, got, tt.want)
		}
	}
}
