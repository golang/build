// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	rdbpb "go.chromium.org/luci/resultdb/proto/v1"
	"golang.org/x/build/cmd/watchflakes/internal/script"
	"rsc.io/github"
)

const day = 24 * time.Hour

// TestSilenceP checks silenceP against the closing thresholds it implies:
// after n flakes spanning W, we close once the silence exceeds
// W*(alpha^(-1/(n-1))-1), which in units of the mean interval W/(n-1) is
// (n-1)*(alpha^(-1/(n-1))-1) and tends to ln(1/alpha) as n grows.
func TestSilenceP(t *testing.T) {
	for _, alpha := range []float64{0.05, 0.01} {
		for _, m := range []int{1, 2, 3, 5, 10, 20, 100} {
			span := 1000 * time.Hour
			want := time.Duration(float64(span) * (math.Pow(alpha, -1/float64(m)) - 1))
			// Just under the threshold: not yet significant.
			if p := silenceP(m+1, span, want-time.Hour); p < alpha {
				t.Errorf("alpha=%v m=%d: p=%.4g just below threshold, want >= alpha", alpha, m, p)
			}
			// Just over: significant.
			if p := silenceP(m+1, span, want+time.Hour); p >= alpha {
				t.Errorf("alpha=%v m=%d: p=%.4g just above threshold, want < alpha", alpha, m, p)
			}
			// The threshold in mean intervals never drops below ln(1/alpha).
			if mult := float64(want) / (float64(span) / float64(m)); mult < math.Log(1/alpha) {
				t.Errorf("alpha=%v m=%d: threshold %.3g mean intervals, want >= %.3g",
					alpha, m, mult, math.Log(1/alpha))
			}
		}
	}

	// Small n needs far more silence than the ln(1/alpha) rule of thumb,
	// which would call for 4.6 mean intervals. Two flakes a day apart need
	// 99 days of silence before they are surprising at alpha=0.01.
	if p := silenceP(2, day, 98*day); p < closeAlpha {
		t.Errorf("silenceP(2, 1d, 98d) = %.4g, want >= %v", p, closeAlpha)
	}
	if p := silenceP(2, day, 100*day); p >= closeAlpha {
		t.Errorf("silenceP(2, 1d, 100d) = %.4g, want < %v", p, closeAlpha)
	}

	// Degenerate inputs never argue for closing.
	if p := silenceP(1, 0, 365*day); p != 1 {
		t.Errorf("silenceP(1, 0, 365d) = %v, want 1", p)
	}
	if p := silenceP(5, 0, 365*day); p != 1 {
		t.Errorf("silenceP(5, 0, 365d) = %v, want 1", p)
	}
}

// markdown renders a failure the way watchflakes posts it, so that
// TestFlakeHistory tests the parser against the real writer.
func markdown(when time.Time, builder, commit string) string {
	fp := &FailurePost{
		BuildResult: &BuildResult{
			Time:                    when,
			Commit:                  commit,
			Builder:                 builder,
			BuilderConfigProperties: &BuilderConfigProperties{Repo: "go", GoBranch: "master"},
		},
		Failure: &Failure{TestID: "net/http.TestFoo", Status: rdbpb.TestStatus_FAIL},
		URL:     "https://ci.chromium.org/b/8759448820419452721",
		Pkg:     "net/http",
		Test:    "TestFoo",
		Snippet: "--- FAIL: TestFoo\n",
	}
	return fp.Markdown()
}

func comment(body string) *github.IssueComment {
	return &github.IssueComment{Body: body + signature}
}

func TestFlakeHistory(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	issue := &Issue{Comments: []*github.IssueComment{
		comment("Found new dashboard test flakes for:\n\n" +
			markdown(t0, "gotip-linux-amd64", "aaaaaaaa11") +
			markdown(t0.Add(time.Hour), "gotip-linux-386", "bbbbbbbb22")),
		// Same commit failing on a second builder: one event, not two.
		comment("Found new dashboard test flakes for:\n\n" +
			markdown(t0.Add(2*time.Hour), "gotip-darwin-amd64", "bbbbbbbb22")),
		// Out of order, to check sorting.
		comment("Found new dashboard test flakes for:\n\n" +
			markdown(t0.Add(-48*time.Hour), "gotip-linux-arm64", "cccccccc33")),
		// Not ours: must be ignored even though it quotes a failure.
		{Body: "I think this is the same as #1234.\n\n" + markdown(t0, "gotip-linux-amd64", "dddddddd44")},
	}}

	got := flakeHistory(issue)
	// Comments carry the short hash that (*FailurePost).String writes.
	want := []flakeEvent{
		{t0.Add(-48 * time.Hour), "gotip-linux-arm64", "cccccccc"},
		{t0, "gotip-linux-amd64", "aaaaaaaa"},
		{t0.Add(time.Hour), "gotip-linux-386", "bbbbbbbb"},
	}
	if len(got) != len(want) {
		t.Fatalf("flakeHistory returned %d events, want %d:\n%v", len(got), len(want), got)
	}
	for i, e := range got {
		if !e.Time.Equal(want[i].Time) || e.Builder != want[i].Builder || e.Commit != want[i].Commit {
			t.Errorf("event %d = %v, want %v", i, e, want[i])
		}
	}
}

// closeTest builds an issue with flakes at the given offsets (in days before
// now) and asks shouldClose about it.
type closeTest struct {
	name      string
	flakes    []float64 // days before now, ascending
	builds    int       // builds on the builder since the last flake
	buildsEnd float64   // days before now that those builds stopped
	rateAlone bool      // the rate test alone would have closed it
	wantWhy   string    // "" means close; otherwise a substring of the reason
	gone      bool      // the builder is no longer configured in LUCI
	retired   bool      // want the close to be on retired-builder grounds
	capped    bool      // want the close to be on the closeMaxSilence cap
}

var closeTests = []closeTest{{
	name:    "steady flake gone quiet",
	flakes:  []float64{200, 190, 180, 170, 160, 150, 140, 130, 120, 110},
	builds:  500,
	wantWhy: "",
}, {
	name:    "still flaking",
	flakes:  []float64{200, 150, 100, 50, 10},
	builds:  500,
	wantWhy: "not yet past",
}, {
	name:    "quiet, but not for long enough to be surprising",
	flakes:  []float64{400, 300, 200, 60},
	builds:  500,
	wantWhy: "not yet past",
}, {
	// A steady flake that has only just stopped. With no floor on silence,
	// two weeks is enough here: the flakes came every two days, the issue
	// has never been quiet a quarter this long, and builds kept running
	// throughout. A 30-day floor would have made this wait twice as long
	// for no added confidence.
	name:    "fast flake, recently stopped",
	flakes:  []float64{40, 38, 36, 34, 32, 30, 28, 26, 24, 22, 20, 18, 16, 14},
	builds:  300,
	wantWhy: "",
}, {
	// A seasonal flake: two tight bursts, nine months apart. The rate test
	// alone would close this well inside the quiet season; the max-gap guard
	// is what holds it open.
	name: "seasonal",
	flakes: []float64{
		360, 359, 358, 357, 356, 355, 354, 353, 352, 351,
		89, 88, 87, 86, 85, 84, 83, 82, 81, 80,
	},
	builds:    5000,
	rateAlone: true,
	wantWhy:   "has been quiet 262 days before",
}, {
	// The same shape, but past the cap. The max-gap guard would hold this
	// open for another nine months; the cap closes it instead, accepting a
	// reopen when the next season comes around.
	name: "seasonal, past the cap",
	flakes: []float64{
		600, 599, 598, 597, 596, 595, 594, 593, 592, 591,
		235, 234, 233, 232, 231, 230, 229, 228, 227, 226,
	},
	builds:  5000,
	wantWhy: "",
	capped:  true,
}, {
	// Two flakes a month apart. The rate test wants eight years of silence,
	// which is the same as never closing.
	name:    "sparse, past the cap",
	flakes:  []float64{125, 95},
	builds:  500,
	wantWhy: "",
	capped:  true,
}, {
	// Almost nothing has run, but the builder is still alive: a freeze or an
	// outage, which says nothing about the flake.
	name:    "tree frozen",
	flakes:  []float64{200, 190, 180, 170, 160, 150, 140, 130, 120, 110},
	builds:  3,
	wantWhy: "only 3 builds",
}, {
	// The builder ran for a while after the last flake and then stopped.
	name:      "builder retired",
	flakes:    []float64{200, 190, 180, 170, 160, 150, 140, 130, 120, 110},
	builds:    300,
	buildsEnd: 60,
	wantWhy:   "",
	retired:   true,
}, {
	// The builder is no longer configured in LUCI.
	name:    "builder gone",
	flakes:  []float64{200, 190, 180, 170, 160, 150, 140, 130, 120, 110},
	builds:  0,
	gone:    true,
	wantWhy: "",
	retired: true,
}, {
	// Still configured, but ListBuilders keeps it off the dashboards for
	// having a known issue. It is very likely still running, so the absence
	// of build times proves nothing and the issue stays open.
	name:    "builder hidden by known_issue",
	flakes:  []float64{200, 190, 180, 170, 160, 150, 140, 130, 120, 110},
	builds:  0,
	wantWhy: "not on the dashboards",
}, {
	name:    "single flake, quiet a while",
	flakes:  []float64{60},
	builds:  500,
	wantWhy: "a single flake, quiet 60 days",
}, {
	name:    "single flake, quiet a long while",
	flakes:  []float64{100},
	builds:  500,
	wantWhy: "",
	capped:  true,
}, {
	// One burst inside a day gives no rate to test, so it falls back to the
	// long timeout rather than closing as soon as the burst ends.
	name:    "one burst",
	flakes:  []float64{60, 59.9, 59.8, 59.7, 59.6},
	builds:  500,
	wantWhy: "5 flakes inside a day",
}}

func TestShouldClose(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range closeTests {
		t.Run(tt.name, func(t *testing.T) {
			const builder = "gotip-linux-amd64"
			var body strings.Builder
			body.WriteString("Found new dashboard test flakes for:\n\n")
			for i, d := range tt.flakes {
				when := now.Add(-time.Duration(d * float64(day)))
				// Distinct in the first 8 characters, which is all a
				// comment records.
				body.WriteString(markdown(when, builder, fmt.Sprintf("%08xff", i)))
			}
			issue := &Issue{
				Issue:    &github.Issue{Number: 1, Body: autoIssueMarker},
				Comments: []*github.IssueComment{comment(body.String())},
			}

			// Spread the builds evenly from the last flake to buildsEnd.
			last := now.Add(-time.Duration(tt.flakes[len(tt.flakes)-1] * float64(day)))
			end := now.Add(-time.Duration(tt.buildsEnd * float64(day)))
			act := &builderActivity{
				since:  now.Add(-timeLimit),
				builds: map[string][]time.Time{},
				known:  map[string]bool{builder: !tt.gone},
			}
			for i := range tt.builds {
				frac := float64(i+1) / float64(tt.builds)
				at := last.Add(time.Duration(frac * float64(end.Sub(last))))
				act.builds[builder] = append(act.builds[builder], at)
			}

			stats, why := shouldClose(issue, act, now)
			if tt.wantWhy == "" && why != "" {
				t.Errorf("shouldClose kept the issue open: %s", why)
			} else if tt.wantWhy != "" && !strings.Contains(why, tt.wantWhy) {
				t.Errorf("shouldClose said %q, want it to mention %q", why, tt.wantWhy)
			}
			if why == "" && stats.Retired != tt.retired {
				t.Errorf("closed with Retired = %v, want %v", stats.Retired, tt.retired)
			}
			if why == "" && stats.Capped != tt.capped {
				t.Errorf("closed with Capped = %v, want %v", stats.Capped, tt.capped)
			}
			// Where the rate test alone would have been wrong, check that it
			// really was, so that the guard holding the issue open is not
			// mistaken for the rate test agreeing.
			if tt.rateAlone && stats.P >= closeAlpha {
				t.Errorf("p = %.4g, want < %v so that the guard is what holds the issue open",
					stats.P, closeAlpha)
			}
		})
	}
}

func TestCloseCandidate(t *testing.T) {
	ok := func() *Issue {
		s, err := script.Parse("script", `default <- pkg == "net/http"`, fields)
		if err != nil {
			t.Fatal(err)
		}
		return &Issue{
			Issue: &github.Issue{
				Number: 1,
				Body:   "```\n#!watchflakes\ndefault <- pkg == \"net/http\"\n```\n\n" + autoIssueMarker,
			},
			Script: s,
		}
	}
	if why := closeCandidate(ok()); why != "" {
		t.Fatalf("closeCandidate on a plain watchflakes issue = %q, want \"\"", why)
	}

	tests := []struct {
		name string
		fix  func(*Issue)
		want string
	}{
		{"closed", func(i *Issue) { i.Closed = true }, "not an open issue"},
		{"flaking", func(i *Issue) { i.Post = []*FailurePost{{}} }, "flaking right now"},
		{"human-filed", func(i *Issue) { i.Body = "TestFoo is flaky on windows" }, "not watchflakes' issue"},
		{"repurposed", func(i *Issue) { i.Body = "net/http: connection leak under load" }, "not watchflakes' issue"},
		{"broken script", func(i *Issue) { i.Error = "cannot parse" }, "script is not usable"},
		{"skip rule", func(i *Issue) { i.Script.Rules[0].Action = "skip" }, "script skips its failures"},
		// Things a human may do that do not take the issue away from us.
		{"retitled", func(i *Issue) { i.Title = "net/http: TestFoo failures on windows only" }, ""},
		{"milestoned", func(i *Issue) { i.Milestone = &github.Milestone{Title: "Backlog"} }, ""},
		{"edited script", func(i *Issue) {
			i.Body = "```\n#!watchflakes\ndefault <- pkg == \"net/http\" && goos == \"windows\"\n```\n\n" + autoIssueMarker
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := ok()
			tt.fix(issue)
			if why := closeCandidate(issue); why != tt.want && !strings.Contains(why, tt.want) {
				t.Errorf("closeCandidate = %q, want %q", why, tt.want)
			} else if tt.want == "" && why != "" {
				t.Errorf("closeCandidate = %q, want \"\"", why)
			}
		})
	}
}

// TestCloseConstants checks the one relationship between the tunables that is
// not obvious: a builder cannot be seen to have been quiet for longer than the
// dashboards reach back, so a closeRetiredQuiet above timeLimit would make
// retirement undetectable rather than merely stricter.
func TestCloseConstants(t *testing.T) {
	if closeRetiredQuiet > timeLimit {
		t.Errorf("closeRetiredQuiet = %v, above timeLimit = %v: no builder could ever be seen quiet that long",
			closeRetiredQuiet, timeLimit)
	}
}

func TestShouldUntrack(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	item := new(github.ProjectItem)
	tests := []struct {
		name string
		fix  func(*Issue)
		want bool
	}{
		{"long closed", func(i *Issue) {}, true},
		{"open", func(i *Issue) { i.Closed = false }, false},
		{"closed recently", func(i *Issue) { i.ClosedAt = now.Add(-89 * day) }, false},
		{"closed just past the delay", func(i *Issue) { i.ClosedAt = now.Add(-91 * day) }, true},
		// Reading an issue outside a project leaves nothing to delete.
		{"not a project item", func(i *Issue) { i.Item = nil }, false},
		// A close date GitHub did not report is not a date 90 days back.
		{"no close date", func(i *Issue) { i.ClosedAt = time.Time{} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Issue: &github.Issue{Number: 1, Closed: true, ClosedAt: now.Add(-120 * day)},
				Item:  item,
			}
			tt.fix(issue)
			if got := shouldUntrack(issue, now); got != tt.want {
				t.Errorf("shouldUntrack = %v, want %v", got, tt.want)
			}
		})
	}
}
