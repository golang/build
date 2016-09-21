// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package devapp

import (
	"encoding/json"
	"fmt"
	"image/color"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"golang.org/x/build/godash"
	gdstats "golang.org/x/build/godash/stats"

	"github.com/aclements/go-gg/generic/slice"
	"github.com/aclements/go-gg/gg"
	"github.com/aclements/go-gg/ggstat"
	"github.com/aclements/go-gg/table"
	"github.com/aclements/go-moremath/stats"
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
	if req.URL.RawQuery == "" {
		http.ServeFile(w, req, "static/svg.html")
		return
	}

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
	if m := milestoneRE.FindStringSubmatch(milestone); m != nil {
		return m[1]
	}
	return ""
}

func (releaseFilter) F(g table.Grouping) table.Grouping {
	return table.MapCols(g, func(milestones, releases []string) {
		for i := range milestones {
			releases[i] = milestoneToRelease(milestones[i])
		}
	}, "Milestone")("Release")
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
			if r == "" {
				r = milestone
			}
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

// windowedPercentiles computes the 0, 25, 50, 75, and 100th
// percentile of the values in column Y over the range (X[i]-Window,
// X[i]).
type windowedPercentiles struct {
	Window time.Duration
	// X must name a time.Time column, Y must name a time.Duration column.
	X, Y string
}

// TODO: This ought to be able to operate on any float64-convertible
// column, but MapCols doesn't use slice.Convert.
func (p windowedPercentiles) F(input table.Grouping) table.Grouping {
	return table.MapCols(input, func(xs []time.Time, ys []time.Duration, outMin []time.Duration, out25 []time.Duration, out50 []time.Duration, out75 []time.Duration, outMax []time.Duration, points []int) {
		var ysFloat []float64
		slice.Convert(&ysFloat, ys)
		for i, x := range xs {
			start := x.Add(-p.Window)
			iStart := sort.Search(len(xs), func(j int) bool { return xs[j].After(start) })

			data := ysFloat[iStart : i+1]
			points[i] = len(data) // XXX

			s := stats.Sample{Xs: data}.Copy().Sort()

			min, max := s.Bounds()
			outMin[i], outMax[i] = time.Duration(min), time.Duration(max)
			p25, p50, p75 := s.Percentile(.25), s.Percentile(.5), s.Percentile(.75)
			out25[i], out50[i], out75[i] = time.Duration(p25), time.Duration(p50), time.Duration(p75)
		}
	}, p.X, p.Y)("min "+p.Y, "p25 "+p.Y, "median "+p.Y, "p75 "+p.Y, "max "+p.Y, "points "+p.Y)
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
	plot.Stat(releaseFilter{})
	for _, aes := range []string{"x", "y"} {
		var s gg.ContinuousScaler
		switch scale := req.Form.Get(aes + "scale"); scale {
		case "log":
			s = gg.NewLogScaler(10)
			// Our plots tend to go to 0, which makes log scales unhappy.
			s.SetMin(1)
		case "lin":
			s = gg.NewLinearScaler()
		case "":
			if aes == "y" {
				s = gg.NewLinearScaler()
				s.Include(0)
			}
		default:
			return fmt.Errorf("unknown %sscale %q", aes, scale)
		}
		if s != nil {
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
			for _, c := range plot.Data().Columns() {
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
		case "percentile":
			window := 30 * 24 * time.Hour
			if win := req.Form.Get("window"); win != "" {
				var err error
				window, err = time.ParseDuration(win)
				if err != nil {
					return err
				}
			}
			plot.Stat(windowedPercentiles{
				Window: window,
				X:      column,
				Y:      "Open",
			})
			// plot.Stat(ggstat.Agg(column)(ggstat.AggMin("Open"), ggstat.AggMax("Open"), ggstat.AggPercentile("median", .5, "Open"), ggstat.AggPercentile("p25", .25, "Open"), ggstat.AggPercentile("p75", .75, "Open")))
			/*
				plot.Add(gg.LayerPaths{
					X: column,
					Y: "points Open",
				})
			*/
			plot.Add(gg.LayerArea{
				X:     column,
				Upper: "max Open",
				Lower: "min Open",
				Fill:  plot.Const(color.Gray{192}),
			})
			plot.Add(gg.LayerArea{
				X:     column,
				Upper: "p75 Open",
				Lower: "p25 Open",
				Fill:  plot.Const(color.Gray{128}),
			})
			plot.Add(gg.LayerPaths{
				X: column,
				Y: "median Open",
			})
		default:
			return fmt.Errorf("unknown agg %q", agg)
		}
	default:
		return fmt.Errorf("unknown pivot %q", pivot)
	}
	if req.Form.Get("raw") != "" {
		w.Header().Set("Content-Type", "text/plain")
		table.Fprint(w, plot.Data())
		return nil
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	plot.WriteSVG(w, 1200, 600)
	return nil
}
