// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package devapp

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"golang.org/x/build/godash"
	gdstats "golang.org/x/build/godash/stats"

	"github.com/aclements/go-gg/gg"
	"github.com/aclements/go-gg/ggstat"
	"github.com/aclements/go-gg/table"
	"github.com/kylelemons/godebug/pretty"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/urlfetch"
)

func updateStats(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	r.ParseForm()

	caches := getCaches(ctx, "github-token", "gzstats")

	log := logFn(ctx, w)

	stats := &godash.Stats{}
	if err := unpackCache(caches["gzstats"], stats); err != nil {
		return err
	}

	transport := &urlfetch.Transport{Context: ctx}
	gh := godash.NewGitHubClient("golang/go", string(caches["github-token"].Value), transport)

	if r.Form.Get("reset_detail") != "" {
		stats.IssueDetailSince = time.Time{}
	}

	start := time.Now()
	if issue := r.Form.Get("issue"); issue != "" {
		num, err := strconv.Atoi(issue)
		if err != nil {
			return err
		}
		err = stats.UpdateIssue(gh, num, log)
		if err != nil {
			return err
		}
		json.NewEncoder(w).Encode(stats.Issues[num])
	} else {
		if err := stats.Update(ctx, gh, log); err != nil {
			return err
		}
	}
	log("Have data about %d issues", len(stats.Issues))
	log("Updated issue stats to %v (detail to %v) in %.3f seconds", stats.Since, stats.IssueDetailSince, time.Now().Sub(start).Seconds())
	return writeCache(ctx, "gzstats", stats)
}

func rawHandler(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)

	stats := &godash.Stats{}
	if err := loadCache(ctx, "gzstats", stats); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("Content-type", "text/plain")
	(&pretty.Config{PrintStringers: true}).Fprint(w, stats)
}

func svgHandler(w http.ResponseWriter, req *http.Request) {
	ctx := appengine.NewContext(req)
	req.ParseForm()

	stats := &godash.Stats{}
	if err := loadCache(ctx, "gzstats", stats); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if err := plot(w, req, gdstats.IssueStats(stats)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

type openFilter bool

func (o openFilter) F(g table.Grouping) table.Grouping {
	return table.Filter(g, func(closed time.Time) bool { return bool(o) == closed.IsZero() }, "Closed")
}

type releaseFilter struct{}

var milestoneRE = regexp.MustCompile(`^(Go\d+(\.\d+)?)(|\.(\d+))(|[-A-Za-z].*)$`)

func milestoneToRelease(milestone string) string {
	switch milestone {
	case "Unreleased", "Unplanned":
		return milestone
	}
	var release string
	if m := milestoneRE.FindStringSubmatch(milestone); m != nil {
		release = m[1]
	}
	return release
}

func (releaseFilter) F(g table.Grouping) table.Grouping {
	g = table.MapTables(g, func(_ table.GroupID, t *table.Table) *table.Table {
		var releases []string
		tb := table.NewBuilder(t)
		milestones := t.MustColumn("Milestone").([]string)
		for _, milestone := range milestones {
			releases = append(releases, milestoneToRelease(milestone))
		}
		tb.Add("Release", releases)
		return tb.Done()
	})
	return table.Filter(g, func(r string) bool { return r != "" }, "Release")
}

type countChange struct {
	t     time.Time
	delta int
}

type countChangeSlice []countChange

func (s countChangeSlice) Len() int           { return len(s) }
func (s countChangeSlice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s countChangeSlice) Less(i, j int) bool { return s[i].t.Before(s[j].t) }

// openCount takes a table of issue stats and figures out how many
// were open at each time. This produces columns called "Time" and
// "Count".
type openCount struct {
	// ByRelease will add a Release column and provide counts per release.
	ByRelease bool
}

func (o openCount) F(input table.Grouping) table.Grouping {
	return table.MapTables(input, func(_ table.GroupID, t *table.Table) *table.Table {
		releases := make(map[string]countChangeSlice)
		add := func(milestone string, t time.Time, count int) {
			r := milestoneToRelease(milestone)
			releases[r] = append(releases[r], countChange{t, count})
		}

		created := t.MustColumn("Created").([]time.Time)
		closed := t.MustColumn("Closed").([]time.Time)
		milestones := t.MustColumn("Milestone").([]string)
		milestonehists := t.MustColumn("MilestoneHistory").([][]godash.MilestoneChange)
		for i := range created {
			milestone := milestones[i]
			// Find the first milestone in which the issue is closed
			closedAt := sort.Search(len(milestonehists[i]), func(j int) bool {
				return closed[i].After(milestonehists[i][j].Until)
			})
			if closedAt == 0 && !closed[i].IsZero() {
				add(milestone, closed[i], -1)
			}
			for j := closedAt - 1; j >= 0; j-- {
				hist := milestonehists[i][j]
				if j == closedAt-1 && !closed[i].IsZero() {
					// Don't show milestone changes after close
					add(hist.Name, closed[i], -1)
				} else {
					add(milestone, hist.Until, 1)
					add(hist.Name, hist.Until, -1)
				}
				milestone = hist.Name
			}
			add(milestone, created[i], 1)
		}

		var nt table.Builder

		var times []time.Time
		var counts []int
		if o.ByRelease {
			var names []string
			for name, s := range releases {
				sort.Sort(s)
				sum := 0
				for _, c := range s {
					names = append(names, name)
					times = append(times, c.t)
					sum += c.delta
					counts = append(counts, sum)
				}
			}
			nt.Add("Release", names)
		} else {
			var all countChangeSlice
			for _, s := range releases {
				all = append(all, s...)
			}
			sort.Sort(all)
			sum := 0
			for _, c := range all {
				times = append(times, c.t)
				sum += c.delta
				counts = append(counts, sum)
			}
		}
		nt.Add("Time", times)
		nt.Add("Count", counts)
		for _, col := range t.Columns() {
			if c, ok := t.Const(col); ok {
				nt.AddConst(col, c)
			}
		}
		return nt.Done()
	})
}

func argtoi(req *http.Request, arg string) (int, bool, error) {
	val := req.Form.Get(arg)
	if val != "" {
		val, err := strconv.Atoi(val)
		if err != nil {
			return 0, false, fmt.Errorf("%s not a number: %q", arg, err)
		}
		return val, true, nil
	}
	return 0, false, nil
}

func plot(w http.ResponseWriter, req *http.Request, stats table.Grouping) error {
	plot := gg.NewPlot(stats)
	for _, aes := range []string{"x", "y"} {
		switch scale := req.Form.Get(aes + "scale"); scale {
		case "log":
			ls := gg.NewLogScaler(10)
			if aes == "x" {
				// Our plots tend to go to 0, which makes log scales unhappy.
				ls.SetMin(1)
			}
			plot.SetScale(aes, ls)
		case "lin":
			s := gg.NewLinearScaler()
			max, ok, err := argtoi(req, aes+"max")
			if err != nil {
				return err
			} else if ok {
				s.SetMax(max)
			}
			min, ok, err := argtoi(req, aes+"min")
			if err != nil {
				return err
			} else if ok {
				s.SetMin(min)
			}
			plot.SetScale(aes, s)
		case "":
			if aes == "y" {
				s := gg.NewLinearScaler()
				s.Include(0)
				plot.SetScale(aes, s)
			}
		default:
			return fmt.Errorf("unknown %sscale %q", aes, scale)
		}
	}
	switch pivot := req.Form.Get("pivot"); pivot {
	case "opencount":
		byRelease := req.Form.Get("group") == "release"
		plot.Stat(openCount{ByRelease: byRelease})
		if byRelease {
			plot.GroupBy("Release")
		}
		plot.SortBy("Time")
		lp := gg.LayerPaths{
			X: "Time",
			Y: "Count",
		}
		if byRelease {
			lp.Color = "Release"
		}
		plot.Add(gg.LayerSteps{LayerPaths: lp})
		if byRelease {
			plot.Add(gg.LayerTooltips{
				X:     "Time",
				Y:     "Count",
				Label: "Release",
			})
		}
	case "":
		switch filter := req.Form.Get("filter"); filter {
		case "open":
			plot.Stat(openFilter(true))
		case "closed":
			plot.Stat(openFilter(false))
		case "":
		default:
			return fmt.Errorf("unknown filter %q", filter)
		}
		column := req.Form.Get("column")
		{
			// TODO: I wish Grouping had a .Column like Table has.
			var found bool
			for _, c := range stats.Columns() {
				if c == column {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("unknown column %q", column)
			}
		}
		plot.SortBy(column)
		switch agg := req.Form.Get("agg"); agg {
		case "count", "":
			plot.Stat(ggstat.Agg(column)(ggstat.AggCount("count")))
			plot.Add(gg.LayerSteps{
				LayerPaths: gg.LayerPaths{
					X: column,
					Y: "count",
				},
			})
		case "ecdf":
			plot.Stat(ggstat.ECDF{
				X: column,
			})
			plot.Add(gg.LayerSteps{
				LayerPaths: gg.LayerPaths{
					X: column,
					Y: "cumulative count",
				},
			})
		case "bin":
			bin := ggstat.Bin{
				X: column,
			}
			d := plot.Data()
			if _, ok := d.Table(d.Tables()[0]).MustColumn(column).([]time.Duration); ok {
				// TODO: These bins are pretty
				// useless, but so are the
				// auto-computed bins. Come up with a
				// better choice for bin widths?
				bin.Breaks = []time.Duration{1 * time.Second, 1 * time.Minute, 1 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}
			}
			plot.Stat(bin)
			if bin.Breaks != nil {
				plot.SetScale("x", gg.NewOrdinalScale())
			}
			plot.Add(gg.LayerSteps{
				LayerPaths: gg.LayerPaths{
					X: column,
					Y: "count",
				},
			})
		case "density":
			plot.Stat(ggstat.Density{
				X:           column,
				BoundaryMin: float64(1 * time.Second),
				BoundaryMax: math.Inf(1),
			})
			plot.Add(gg.LayerPaths{
				X: column,
				Y: "probability density",
			})
		}
	default:
		return fmt.Errorf("unknown pivot %q", pivot)
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	plot.WriteSVG(w, 1200, 600)
	return nil
}
