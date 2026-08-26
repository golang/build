// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"log"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/build/cmd/watchflakes/internal/script"
)

// Automatic close parameters.
const (
	// closeAlpha is the smallest probability of the observed silence,
	// assuming the flake rate has not changed, that we are willing to
	// dismiss as coincidence. Each quiet stretch on an issue is one
	// chance to close it wrongly, so an issue with k flakes over its
	// lifetime has roughly a 1-(1-closeAlpha)^k chance of being closed
	// at least once by accident: 18% at k=20 for closeAlpha=0.01,
	// versus 64% for closeAlpha=0.05.
	closeAlpha = 0.01

	// closeMaxSilence is the longest an issue is ever left open while quiet,
	// whatever the statistics ask for.
	//
	// Two things need it. Some issues have no usable rate at all: a single
	// flake, or several packed into less than closeMinSpan, leave nothing to
	// test against. Others have a rate that asks for more silence than is
	// worth waiting for, because the required wait scales with the span
	// the flakes cover: two flakes a month apart ask for eight years, and
	// three flakes over two months ask for eighteen months, so in practice
	// those issues never close at all.
	//
	// Capping the wait gives up calibration for the sparse cases, which will
	// be closed while still live more often than closeAlpha allows. The cost
	// of being wrong is one automatic reopen, and the worst case is an issue
	// that flakes just slower than the cap, which then closes and reopens
	// every 91 days.
	closeMaxSilence = 90 * 24 * time.Hour

	// closeMinSpan is the shortest span of flakes that gives a usable rate.
	// Flakes closer together than this are a single burst, not a rate.
	closeMinSpan = 24 * time.Hour

	// closeMinBuilds is how many builds must have run since the last flake,
	// on the builders that produced this issue's flakes, before silence
	// counts as evidence. Without it, an issue goes quiet when the tree is
	// frozen or the fleet is down, neither of which says anything about
	// whether the flake was fixed.
	closeMinBuilds = 20

	// closeRetiredQuiet is how long the builders that produced an issue's
	// flakes must go without running anything before we call them retired
	// and close the issue on those grounds instead. A renamed builder counts
	// as retired under its old name, which is the right answer: the failures
	// this issue collected cannot recur while nothing runs under that name,
	// and anything the new name produces gets an issue of its own.
	//
	// Ideally this would be longer, but the dashboards only reach back
	// timeLimit, so a longer stretch of no builds is not observable.
	closeRetiredQuiet = 30 * 24 * time.Hour

	// closeUntrackDelay is how long an issue stays in the Test Flakes project
	// after being closed. Once it has been closed that long without a flake
	// reopening it, watchflakes stops watching it, and a recurrence after
	// that gets a new issue instead of reviving an old one. This applies
	// however the issue was closed: one closed by a fix waits the same time,
	// measured from the fix, as one closed here for looking fixed.
	closeUntrackDelay = 90 * 24 * time.Hour

	// closeCheckPeriod is the minimum time between closing passes. A pass
	// reads the comments on every candidate issue, which is too many round
	// trips to repeat on every -repeat cycle.
	closeCheckPeriod = 24 * time.Hour
)

// autoIssueMarker is the sentence postNew puts in the body of every issue
// watchflakes files. Its absence means a human has rewritten the issue into
// something that is no longer ours to close.
const autoIssueMarker = "Issue created automatically to collect these failures."

// lastCloseCheck is when the last closing pass ran, for closeCheckPeriod.
var lastCloseCheck time.Time

// closeIssues closes the issues whose flakes appear to have stopped for good.
// It reports what it decided for every issue it considered, and closes them
// only if both -post and -close are set.
func closeIssues(issues []*Issue, boards []*Dashboard, known map[string]bool) {
	if !lastCloseCheck.IsZero() && time.Since(lastCloseCheck) < closeCheckPeriod {
		return
	}
	lastCloseCheck = time.Now()

	act := newBuilderActivity(boards, known)
	now := time.Now()
	closed, considered := 0, 0
	for _, issue := range issues {
		// Check what we can before spending a round trip on comments.
		if why := closeCandidate(issue); why != "" {
			if *verbose {
				fmt.Printf(" - keeping #%d: %s\n", issue.Number, why)
			}
			continue
		}
		considered++
		readComments(issue)
		stats, why := shouldClose(issue, act, now)
		if why != "" {
			if *verbose {
				fmt.Printf(" - keeping #%d: %s\n", issue.Number, why)
			}
			continue
		}
		if stats.Retired {
			fmt.Printf(" - closing #%d %s\n   %d flakes to %s, %s no longer running\n",
				issue.Number, issue.Title, stats.N, stats.Last.Format("2006-01-02"),
				builderList(stats.Builders))
		} else {
			how := fmt.Sprintf("p=%.2g", stats.P)
			if stats.Capped {
				how = "on the " + days(closeMaxSilence) + " cap"
			}
			fmt.Printf(" - closing #%d %s\n   %d flakes to %s, quiet %s, %s, %d builds since\n",
				issue.Number, issue.Title, stats.N, stats.Last.Format("2006-01-02"),
				days(stats.Silence), how, stats.Builds)
		}
		closed++
		if !*post || !*doClose {
			continue
		}
		if err := postClose(issue, closeText(stats)); err != nil {
			log.Print(err)
		}
	}
	untracked := untrackIssues(issues, now)
	closeVerb, dropVerb := "closed", "dropped"
	if !*post || !*doClose {
		closeVerb, dropVerb = "would close", "would drop"
	}
	log.Printf("Close pass: %s %d of %d quiet issues, %s %d long-closed ones from the project.",
		closeVerb, closed, considered, dropVerb, untracked)
}

// untrackIssues removes from the Test Flakes project the issues that have been
// closed for closeUntrackDelay, and reports how many it found. It returns
// them to the project's care rather than watchflakes': the script stops
// matching, so a failure like the ones it collected files a fresh issue.
//
// Unlike closing, this is not limited to the issues watchflakes filed. An
// issue closed by a fix has had the same chance to prove the fix wrong as one
// closed here for going quiet, so both stop being watched on the same terms.
func untrackIssues(issues []*Issue, now time.Time) int {
	n := 0
	for _, issue := range issues {
		if !shouldUntrack(issue, now) {
			continue
		}
		n++
		fmt.Printf(" - dropping #%d %s\n   closed %s ago\n",
			issue.Number, issue.Title, days(now.Sub(issue.ClosedAt)))
		if !*post || !*doClose {
			continue
		}
		if err := gh.DeleteProjectItem(testFlakes, issue.Item); err != nil {
			log.Print(err)
		}
	}
	return n
}

// shouldUntrack reports whether issue has been closed long enough to stop
// watching. An issue that was reopened is not closed, so its delay starts
// over from whenever it is closed again.
func shouldUntrack(issue *Issue, now time.Time) bool {
	return issue.Item != nil && issue.Closed && !issue.ClosedAt.IsZero() &&
		now.Sub(issue.ClosedAt) >= closeUntrackDelay
}

// closeCandidate reports why issue can never be closed automatically,
// or "" if it might be. It looks only at data readIssues has already
// loaded, so that callers can skip fetching comments for issues that
// cannot qualify.
func closeCandidate(issue *Issue) string {
	switch {
	case issue.Number == 0 || issue.Closed:
		return "not an open issue"
	case len(issue.Post) > 0:
		return "flaking right now"

	// Nothing here looks at milestones, assignees, or discussion. A comment
	// on a flake issue is usually diagnosis rather than a commitment to keep
	// it open, and the milestones these issues carry say the same: as of
	// 2026-08-27 a milestone check held back 350 issues, of which the great
	// majority were Unreleased, Backlog, gopls/backlog, or vuln/unplanned,
	// which mean the opposite of a promise to fix before a release.
	//
	// watchflakes closes only the issues it filed and still owns, which the
	// marker sentence postNew writes into the body records. A human may
	// retitle one of these issues, or edit its script, without taking it
	// over, but rewriting the body past the marker means it has become
	// something else.
	//
	// Not the Automation label, which postNew also applies but which is
	// missing from most of the issues watchflakes filed: as of 2026-08-27
	// the Test Flakes project held 1194 issues carrying the marker and only
	// 192 carrying the label. Requiring the label would leave the great
	// majority of watchflakes' own issues untouchable.
	case !strings.Contains(issue.Body, autoIssueMarker):
		return "not watchflakes' issue to close"

	// If the script does not work, or deliberately drops what it matches,
	// then the issue is quiet for reasons that have nothing to do with
	// whether the failures stopped.
	case issue.Script == nil || issue.Error != "":
		return "script is not usable"
	case hasSkipRule(issue.Script):
		return "script skips its failures"
	}
	return ""
}

// shouldClose reports why issue should stay open, or "" if it should be
// closed, along with the evidence either way. It requires that issue's
// comments have been read.
//
// There are three ways to close. If the builders that produced the flakes
// have stopped running altogether, the issue is moot whatever the flake rate
// was. Otherwise the test is that the silence since the last flake is too
// long to be a coincidence (see silenceP) and is longer than any quiet
// stretch this issue has produced before, or else that the silence has
// reached closeMaxSilence. Either way enough builds must have run during the
// silence for a flake to have shown up if it were still there.
//
// There is deliberately no floor on how long an issue must be quiet. The two
// things a floor would guard against are already covered, and covered by the
// data rather than by a guess: a holiday or a freeze shows up as too few
// builds, and a flake that normally goes quiet for a while shows up in
// MaxGap. Imposing a floor on top would only delay the clearest cases, such
// as a consistent failure that a fix has just made stop.
func shouldClose(issue *Issue, act *builderActivity, now time.Time) (*closeStats, string) {
	events := flakeHistory(issue)
	if len(events) == 0 {
		return nil, "no flake history to read"
	}

	s := &closeStats{
		N:     len(events),
		First: events[0].Time,
		Last:  events[len(events)-1].Time,
	}
	s.Span = s.Last.Sub(s.First)
	s.Silence = now.Sub(s.Last)
	for i := 1; i < len(events); i++ {
		if gap := events[i].Time.Sub(events[i-1].Time); gap > s.MaxGap {
			s.MaxGap = gap
		}
	}
	builders := make(map[string]bool)
	for _, e := range events {
		builders[e.Builder] = true
	}
	s.Builders = slices.Sorted(maps.Keys(builders))
	s.Builds = act.countSince(s.Builders, s.Last)
	s.LastBuild = act.lastBuild(s.Builders)
	s.Since = act.since
	s.P = silenceP(s.N, s.Span, s.Silence)

	// A builder that has stopped running cannot reproduce the flake, so there
	// is no rate to test: the issue is moot either way. Build times here are
	// commit times, which run a little behind when the build actually ran,
	// but only by hours against a threshold of weeks.
	switch {
	case !act.exists(s.Builders):
		// Gone from LUCI altogether.
		s.Retired = true
		return s, ""
	case !act.seen(s.Builders):
		// Still configured, but kept off the dashboards, which ListBuilders
		// does for every builder with a known issue. Those builders run as
		// usual, so their absence here says nothing at all -- and a builder
		// with a known issue is exactly the sort to have flake issues open.
		// Without build times there is no exposure to measure, so leave it.
		return s, fmt.Sprintf("%s not on the dashboards", strings.Join(s.Builders, ", "))
	case now.Sub(s.LastBuild) > closeRetiredQuiet:
		// Configured, but nothing has run for a long time.
		s.Retired = true
		return s, ""
	}

	// Work out whether the statistics alone make the case for closing.
	var statsWhy string
	switch {
	case s.N < 2:
		statsWhy = "a single flake"
	case s.Span < closeMinSpan:
		statsWhy = fmt.Sprintf("%d flakes inside a day", s.N)
	case s.P >= closeAlpha:
		statsWhy = fmt.Sprintf("p=%.2g, not yet past %v", s.P, closeAlpha)
	case s.Silence <= s.MaxGap:
		// The rate test assumes flakes arrive steadily. A seasonal flake
		// breaks that assumption, and the test alone recovers from it only
		// slowly: absorbing a quiet season into the span raises the
		// threshold, but it converges to season*ln(1/alpha)/burst, which
		// stays inside the season once a burst is more than ln(1/alpha)
		// flakes, so the issue would be closed and reopened forever. So also
		// require silence longer than any gap this issue has survived, which
		// is distribution-free and, for a genuinely steady flake with fewer
		// than about fifty intervals, is not the binding constraint.
		statsWhy = fmt.Sprintf("has been quiet %s before", days(s.MaxGap))
	}
	if statsWhy != "" {
		if s.Silence < closeMaxSilence {
			return s, fmt.Sprintf("%s, quiet %s of the %s that closes anything",
				statsWhy, days(s.Silence), days(closeMaxSilence))
		}
		s.Capped = true
	}
	if s.Builds < closeMinBuilds {
		return s, fmt.Sprintf("only %d builds on %s since the last flake",
			s.Builds, strings.Join(s.Builders, ", "))
	}
	return s, ""
}

// closeStats is the evidence for closing an issue.
type closeStats struct {
	N         int           // flakes recorded on the issue, bursts collapsed
	First     time.Time     // time of the first flake
	Last      time.Time     // time of the most recent flake
	Span      time.Duration // First to Last
	Silence   time.Duration // Last to now
	MaxGap    time.Duration // longest stretch between consecutive flakes
	P         float64       // probability of this much silence, rate unchanged
	Builders  []string      // builders that produced the flakes
	Builds    int           // builds on those builders since Last
	LastBuild time.Time     // most recent build on them, zero if none in the window
	Since     time.Time     // start of the dashboard window LastBuild is drawn from
	Retired   bool          // they have stopped running, so the issue is moot
	Capped    bool          // closed on closeMaxSilence, not on the statistics
}

// silenceP returns the probability of seeing no flakes for the given silence,
// after n flakes spanning the given time, assuming the flake rate has not
// changed.
//
// Condition on the n-1 flakes that arrived between the first flake and now,
// an interval of length span+silence. For a Poisson process, given how many
// there are, their arrival times are independent and uniform over that
// interval, so the chance that every one of them landed in the first span of
// it is (span/(span+silence))^(n-1). This is exact: no rate is estimated and
// no large-n approximation is used, which matters because most issues have
// only a handful of flakes. Solved for silence, it says to wait
// span*(alpha^(-1/(n-1))-1), which for large n approaches ln(1/alpha) mean
// intervals but at n=3 is nearly four times that.
//
// It is also the Bayesian predictive probability under the scale-invariant
// prior on the rate, which is some comfort that it is not an artifact of how
// the question was set up.
func silenceP(n int, span, silence time.Duration) float64 {
	if n < 2 || span <= 0 {
		return 1
	}
	return math.Pow(float64(span)/float64(span+silence), float64(n-1))
}

// flakeEvent is one flake watchflakes reported on an issue.
type flakeEvent struct {
	Time    time.Time
	Builder string
	Commit  string
}

// flakeLineRE matches the header (*FailurePost).String writes for each
// failure, as wrapped by (*FailurePost).Markdown: a timestamp, the builder,
// and repo@commit, where the commit is the short hash shortHash produces.
var flakeLineRE = regexp.MustCompile(`(\d{4}-\d\d-\d\d \d\d:\d\d) (\S+) \S+@([0-9a-f]+)`)

// flakeHistory returns the flakes watchflakes has reported on issue, oldest
// first, read back out of the comments it signed.
//
// Flakes sharing a commit are counted once. A single bad commit can fail on
// many builders at once, and counting each one separately would overstate the
// flake rate and so close the issue too eagerly. Collapsing them can only
// make closing harder, since the threshold span*(alpha^(-1/(n-1))-1) grows as
// n falls.
//
// Comments left by versions of watchflakes that predate this failure format
// do not parse and are ignored, which is likewise conservative.
func flakeHistory(issue *Issue) []flakeEvent {
	var events []flakeEvent
	seen := make(map[string]bool)
	for _, com := range issue.Comments {
		if !isWatchflakesComment(com.Body) {
			continue
		}
		for _, m := range flakeLineRE.FindAllStringSubmatch(com.Body, -1) {
			t, err := time.Parse("2006-01-02 15:04", m[1])
			if err != nil || seen[m[3]] {
				continue
			}
			seen[m[3]] = true
			events = append(events, flakeEvent{Time: t, Builder: m[2], Commit: m[3]})
		}
	}
	slices.SortFunc(events, func(a, b flakeEvent) int { return a.Time.Compare(b.Time) })
	return events
}

// builderActivity records when each builder ran, whether or not the build
// failed, so that silence on an issue can be told apart from a builder that
// has stopped running.
type builderActivity struct {
	since  time.Time              // start of the dashboard window
	builds map[string][]time.Time // build times, by builder
	known  map[string]bool        // every builder LUCI has configured
}

// newBuilderActivity gathers the build times on the dashboards, which cover
// the most recent timeLimit, alongside the set of builders LUCI has
// configured. The two differ: ListBuilders keeps any builder with a known
// issue off the dashboards, so a builder can be running steadily and still
// have no build times here.
func newBuilderActivity(boards []*Dashboard, known map[string]bool) *builderActivity {
	act := &builderActivity{builds: make(map[string][]time.Time), known: known}
	for _, dash := range boards {
		for i, b := range dash.Builders {
			for _, r := range dash.Results[i] {
				if r == nil {
					continue
				}
				act.builds[b.Name] = append(act.builds[b.Name], r.Time)
				if act.since.IsZero() || r.Time.Before(act.since) {
					act.since = r.Time
				}
			}
		}
	}
	return act
}

// countSince returns how many builds ran on the named builders since t.
// The dashboards only reach back timeLimit, so when t is older than that the
// count is a lower bound, which can only delay a close.
func (act *builderActivity) countSince(builders []string, t time.Time) int {
	n := 0
	for _, b := range builders {
		for _, bt := range act.builds[b] {
			if bt.After(t) {
				n++
			}
		}
	}
	return n
}

// exists reports whether any of builders is still configured in LUCI, whether
// or not it reaches the dashboards.
func (act *builderActivity) exists(builders []string) bool {
	for _, b := range builders {
		if act.known[b] {
			return true
		}
	}
	return false
}

// seen reports whether any of builders appears on the dashboards, and so has
// build times to reason about.
func (act *builderActivity) seen(builders []string) bool {
	for _, b := range builders {
		if len(act.builds[b]) > 0 {
			return true
		}
	}
	return false
}

// lastBuild returns the most recent time any of builders ran, or the zero
// time if none of them ran during the dashboard window.
func (act *builderActivity) lastBuild(builders []string) time.Time {
	var last time.Time
	for _, b := range builders {
		for _, bt := range act.builds[b] {
			if bt.After(last) {
				last = bt
			}
		}
	}
	return last
}

// closeText returns the comment to post when closing an issue.
func closeText(s *closeStats) string {
	if s.Retired {
		return retiredText(s)
	}
	var b strings.Builder
	if s.Capped {
		fmt.Fprintf(&b, "Closing automatically: no failures matching this issue in %s.\n\n", days(s.Silence))
	} else {
		fmt.Fprintf(&b, "Closing automatically: the failures this issue tracks appear to have stopped.\n\n")
	}
	if s.N == 1 {
		fmt.Fprintf(&b, "- One flake recorded, on %s.\n", s.First.Format("2006-01-02"))
	} else {
		fmt.Fprintf(&b, "- %d flakes recorded, from %s to %s.\n",
			s.N, s.First.Format("2006-01-02"), s.Last.Format("2006-01-02"))
	}
	fmt.Fprintf(&b, "- Quiet for %s.", days(s.Silence))
	if s.MaxGap > 0 {
		fmt.Fprintf(&b, " The longest it had gone quiet before was %s.", days(s.MaxGap))
	}
	fmt.Fprintf(&b, "\n- %d builds have run since then, on %s.\n", s.Builds, builderList(s.Builders))

	if !s.Capped {
		fmt.Fprintf(&b, "\nIf the flakes were still arriving at their old rate, the chance of this much silence would be about %s.\n",
			percent(s.P))
		fmt.Fprintf(&b, "\nThat is evidence the failures stopped, not proof they were fixed. If a matching failure turns up again, watchflakes will reopen this issue.\n")
		return b.String()
	}

	// Closed on the cap, so say what the statistics could not settle.
	switch {
	case s.N == 1:
		fmt.Fprintf(&b, "\nA single flake gives no rate to compare this silence against.")
	case s.Span < closeMinSpan:
		fmt.Fprintf(&b, "\nFlakes packed into less than a day give no rate to compare this silence against.")
	case s.MaxGap >= s.Silence:
		fmt.Fprintf(&b, "\nThis issue has gone quiet for %s before and come back, so its history cannot tell whether this time is different.",
			days(s.MaxGap))
	default:
		fmt.Fprintf(&b, "\nThese flakes are too few and too spread out for their rate to settle the question: it would take %s of silence first.",
			days(requiredSilence(s)))
	}
	fmt.Fprintf(&b, " Watchflakes closes any issue quiet for %s regardless, so this is a timeout rather than evidence that the problem is gone.\n",
		days(closeMaxSilence))
	fmt.Fprintf(&b, "\nIf a matching failure turns up again, watchflakes will reopen this issue.\n")
	return b.String()
}

// requiredSilence returns how long the rate test would leave an issue open,
// for reporting when closeMaxSilence overrides it.
func requiredSilence(s *closeStats) time.Duration {
	d := time.Duration(float64(s.Span) * (math.Pow(closeAlpha, -1/float64(s.N-1)) - 1))
	return max(d, s.MaxGap)
}

// retiredText returns the comment to post when closing an issue because the
// builders that produced its failures are no longer running.
func retiredText(s *closeStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Closing automatically: the failures this issue tracks only ever happened on %s, which %s no longer running.\n\n",
		builderList(s.Builders), plural(len(s.Builders), "is", "are"))
	if s.LastBuild.IsZero() {
		fmt.Fprintf(&b, "- No builds at all since %s, as far back as the dashboards go.\n",
			s.Since.Format("2006-01-02"))
	} else {
		fmt.Fprintf(&b, "- Last build on %s.\n", s.LastBuild.Format("2006-01-02"))
	}
	if s.N == 1 {
		fmt.Fprintf(&b, "- One flake recorded, on %s.\n", s.First.Format("2006-01-02"))
	} else {
		fmt.Fprintf(&b, "- %d flakes recorded, from %s to %s.\n",
			s.N, s.First.Format("2006-01-02"), s.Last.Format("2006-01-02"))
	}
	fmt.Fprintf(&b, "\nThese failures cannot show up again while nothing is running under %s. This says nothing about whether the underlying problem was fixed: if the builder comes back, or has been renamed, or the problem turns up elsewhere, watchflakes will reopen this issue or file a new one.\n",
		plural(len(s.Builders), "that name", "those names"))
	return b.String()
}

// builderList formats builders for an issue comment, keeping it short when
// a flake has hit many builders.
func builderList(builders []string) string {
	const max = 5
	if len(builders) <= max {
		return strings.Join(builders, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(builders[:max], ", "), len(builders)-max)
}

// postClose comments on the issue explaining the decision and then closes it,
// in that order so that the explanation is there when the notification lands.
func postClose(issue *Issue, body string) error {
	if err := gh.AddIssueComment(issue.Issue, body+signature); err != nil {
		return err
	}
	return gh.CloseIssue(issue.Issue)
}

// hasSkipRule reports whether s drops any of the failures it matches,
// instead of posting all of them.
func hasSkipRule(s *script.Script) bool {
	for _, r := range s.Rules {
		if r.Action == "skip" {
			return true
		}
	}
	return false
}

// plural returns one or many according to n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// days formats d as a whole number of days, for issue comments and logs.
func days(d time.Duration) string {
	n := int(d / (24 * time.Hour))
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}

// percent formats a small probability as a percentage.
func percent(p float64) string {
	if p < 0.0001 {
		return "under 0.01%"
	}
	return fmt.Sprintf("%.2g%%", p*100)
}
