// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
)

func TestParseGithubEvents(t *testing.T) {
	res, err := http.ReadResponse(bufio.NewReader(strings.NewReader(eventRes)), nil)
	if err != nil {
		t.Fatal(err)
	}

	events, err := parseGithubEvents(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		t.Logf("events[%d]: %#v at %v by %#v", i, e, e.Created, e.Actor)
		if e.OtherJSON != "" {
			t.Errorf("Contains OtherJSON: events[%d]: %#v at %v by %#v", i, e, e.Created, e.Actor)
		}
	}
}

const eventRes = `HTTP/1.1 200 OK
Server: GitHub.com
Date: Tue, 14 Mar 2017 20:55:28 GMT
Content-Type: application/json; charset=utf-8
Status: 200 OK
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 55
X-RateLimit-Reset: 1489527982
Cache-Control: public, max-age=60, s-maxage=60
Vary: Accept
ETag: "47e7fe4c82c28a54a4d0d82dc10e603f"
X-GitHub-Media-Type: github.v3; format=json
Link: <https://api.github.com/repositories/23096959/issues/9/events?per_page=2&page=2>; rel="next", <https://api.github.com/repositories/23096959/issues/9/events?per_page=2&page=2>; rel="last"
Access-Control-Expose-Headers: ETag, Link, X-GitHub-OTP, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, X-OAuth-Scopes, X-Accepted-OAuth-Scopes, X-Poll-Interval
Access-Control-Allow-Origin: *
Content-Security-Policy: default-src 'none'
Strict-Transport-Security: max-age=31536000; includeSubdomains; preload
X-Content-Type-Options: nosniff
X-Frame-Options: deny
X-XSS-Protection: 1; mode=block
Vary: Accept-Encoding
X-Served-By: a30e6f9aa7cf5731b87dfb3b9992202d
X-GitHub-Request-Id: B13C:356D:B31D97B:D547D0F:58C858C0

[
  {
    "id": 998144526,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144526",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "labeled",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "label": {
      "name": "enhancement",
      "color": "84b6eb"
    }
  },
  {
    "id": 998144527,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144527",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "labeled",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "label": {
      "name": "help wanted",
      "color": "128A0C"
    }
  },
  {
    "id": 998144529,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144529",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "milestoned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "milestone": {
      "title": "World Domination"
    }
  },
  {
    "id": 998144530,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144530",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "assigned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:28Z",
    "assignee": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "assigner": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    }
  },
  {
    "id": 998144646,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/998144646",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "locked",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-13T22:39:36Z"
  },
  {
    "id": 999944299,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/999944299",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "demilestoned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T22:23:08Z",
    "milestone": {
      "title": "World Domination"
    }
  },
  {
    "id": 999944363,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/999944363",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "unlabeled",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T22:23:12Z",
    "label": {
      "name": "help wanted",
      "color": "128A0C"
    }
  },
  {
    "id": 999945324,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/999945324",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "labeled",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T22:23:40Z",
    "label": {
      "name": "invalid",
      "color": "e6e6e6"
    }
  },
  {
    "id": 999945325,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/999945325",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "labeled",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T22:23:40Z",
    "label": {
      "name": "wontfix",
      "color": "ffffff"
    }
  },
  {
    "id": 1000011082,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1000011082",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "assigned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T23:22:57Z",
    "assignee": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "assigner": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    }
  },
  {
    "id": 1000011083,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1000011083",
    "actor": {
      "login": "shurcooL",
      "id": 1924134,
      "avatar_url": "https://avatars0.githubusercontent.com/u/1924134?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/shurcooL",
      "html_url": "https://github.com/shurcooL",
      "followers_url": "https://api.github.com/users/shurcooL/followers",
      "following_url": "https://api.github.com/users/shurcooL/following{/other_user}",
      "gists_url": "https://api.github.com/users/shurcooL/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/shurcooL/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/shurcooL/subscriptions",
      "organizations_url": "https://api.github.com/users/shurcooL/orgs",
      "repos_url": "https://api.github.com/users/shurcooL/repos",
      "events_url": "https://api.github.com/users/shurcooL/events{/privacy}",
      "received_events_url": "https://api.github.com/users/shurcooL/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "assigned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T23:22:57Z",
    "assignee": {
      "login": "shurcooL",
      "id": 1924134,
      "avatar_url": "https://avatars0.githubusercontent.com/u/1924134?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/shurcooL",
      "html_url": "https://github.com/shurcooL",
      "followers_url": "https://api.github.com/users/shurcooL/followers",
      "following_url": "https://api.github.com/users/shurcooL/following{/other_user}",
      "gists_url": "https://api.github.com/users/shurcooL/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/shurcooL/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/shurcooL/subscriptions",
      "organizations_url": "https://api.github.com/users/shurcooL/orgs",
      "repos_url": "https://api.github.com/users/shurcooL/repos",
      "events_url": "https://api.github.com/users/shurcooL/events{/privacy}",
      "received_events_url": "https://api.github.com/users/shurcooL/received_events",
      "type": "User",
      "site_admin": false
    },
    "assigner": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    }
  },
  {
    "id": 1000014895,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1000014895",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "unlocked",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-14T23:26:21Z"
  },
  {
    "id": 1000077586,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/1000077586",
    "actor": {
      "login": "shurcooL",
      "id": 1924134,
      "avatar_url": "https://avatars0.githubusercontent.com/u/1924134?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/shurcooL",
      "html_url": "https://github.com/shurcooL",
      "followers_url": "https://api.github.com/users/shurcooL/followers",
      "following_url": "https://api.github.com/users/shurcooL/following{/other_user}",
      "gists_url": "https://api.github.com/users/shurcooL/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/shurcooL/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/shurcooL/subscriptions",
      "organizations_url": "https://api.github.com/users/shurcooL/orgs",
      "repos_url": "https://api.github.com/users/shurcooL/repos",
      "events_url": "https://api.github.com/users/shurcooL/events{/privacy}",
      "received_events_url": "https://api.github.com/users/shurcooL/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "unassigned",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2017-03-15T00:31:42Z",
    "assignee": {
      "login": "shurcooL",
      "id": 1924134,
      "avatar_url": "https://avatars0.githubusercontent.com/u/1924134?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/shurcooL",
      "html_url": "https://github.com/shurcooL",
      "followers_url": "https://api.github.com/users/shurcooL/followers",
      "following_url": "https://api.github.com/users/shurcooL/following{/other_user}",
      "gists_url": "https://api.github.com/users/shurcooL/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/shurcooL/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/shurcooL/subscriptions",
      "organizations_url": "https://api.github.com/users/shurcooL/orgs",
      "repos_url": "https://api.github.com/users/shurcooL/repos",
      "events_url": "https://api.github.com/users/shurcooL/events{/privacy}",
      "received_events_url": "https://api.github.com/users/shurcooL/received_events",
      "type": "User",
      "site_admin": false
    },
    "assigner": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    }
  },
  {
    "id": 769896411,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/769896411",
    "actor": {
      "login": "rakyll",
      "id": 108380,
      "avatar_url": "https://avatars3.githubusercontent.com/u/108380?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/rakyll",
      "html_url": "https://github.com/rakyll",
      "followers_url": "https://api.github.com/users/rakyll/followers",
      "following_url": "https://api.github.com/users/rakyll/following{/other_user}",
      "gists_url": "https://api.github.com/users/rakyll/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/rakyll/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/rakyll/subscriptions",
      "organizations_url": "https://api.github.com/users/rakyll/orgs",
      "repos_url": "https://api.github.com/users/rakyll/repos",
      "events_url": "https://api.github.com/users/rakyll/events{/privacy}",
      "received_events_url": "https://api.github.com/users/rakyll/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "renamed",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2016-08-28T05:14:52Z",
    "rename": {
      "from": "add a link to the original issue",
      "to": "cmd/servegoissues: add a link to the original issue"
    }
  },
  {
    "id": 769905440,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/769905440",
    "actor": {
      "login": "shurcooL",
      "id": 1924134,
      "avatar_url": "https://avatars0.githubusercontent.com/u/1924134?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/shurcooL",
      "html_url": "https://github.com/shurcooL",
      "followers_url": "https://api.github.com/users/shurcooL/followers",
      "following_url": "https://api.github.com/users/shurcooL/following{/other_user}",
      "gists_url": "https://api.github.com/users/shurcooL/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/shurcooL/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/shurcooL/subscriptions",
      "organizations_url": "https://api.github.com/users/shurcooL/orgs",
      "repos_url": "https://api.github.com/users/shurcooL/repos",
      "events_url": "https://api.github.com/users/shurcooL/events{/privacy}",
      "received_events_url": "https://api.github.com/users/shurcooL/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "referenced",
    "commit_id": "5383ecf5a0824649ffcc0349f00f0317575753d0",
    "commit_url": "https://api.github.com/repos/bradfitz/go-issue-mirror/commits/5383ecf5a0824649ffcc0349f00f0317575753d0",
    "created_at": "2016-08-28T06:28:38Z"
  },
  {
    "id": 790032051,
    "url": "https://api.github.com/repos/bradfitz/go-issue-mirror/issues/events/790032051",
    "actor": {
      "login": "bradfitz",
      "id": 2621,
      "avatar_url": "https://avatars2.githubusercontent.com/u/2621?v=3",
      "gravatar_id": "",
      "url": "https://api.github.com/users/bradfitz",
      "html_url": "https://github.com/bradfitz",
      "followers_url": "https://api.github.com/users/bradfitz/followers",
      "following_url": "https://api.github.com/users/bradfitz/following{/other_user}",
      "gists_url": "https://api.github.com/users/bradfitz/gists{/gist_id}",
      "starred_url": "https://api.github.com/users/bradfitz/starred{/owner}{/repo}",
      "subscriptions_url": "https://api.github.com/users/bradfitz/subscriptions",
      "organizations_url": "https://api.github.com/users/bradfitz/orgs",
      "repos_url": "https://api.github.com/users/bradfitz/repos",
      "events_url": "https://api.github.com/users/bradfitz/events{/privacy}",
      "received_events_url": "https://api.github.com/users/bradfitz/received_events",
      "type": "User",
      "site_admin": false
    },
    "event": "closed",
    "commit_id": null,
    "commit_url": null,
    "created_at": "2016-09-15T00:18:43Z"
  }
]
`

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
