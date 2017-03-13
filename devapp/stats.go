// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package devapp

import (
	"encoding/json"
	"fmt"
	"html/template"
	"image/color"
	"io/ioutil"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aclements/go-gg/generic/slice"
	"github.com/aclements/go-gg/gg"
	"github.com/aclements/go-gg/ggstat"
	"github.com/aclements/go-gg/table"
	"github.com/aclements/go-moremath/stats"
	"github.com/kylelemons/godebug/pretty"
	"golang.org/x/build/godash"
	gdstats "golang.org/x/build/godash/stats"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

func updateStats(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	r.ParseForm()

	g, errctx := errgroup.WithContext(ctx)
	var token string
	g.Go(func() error {
		var err error
		token, err = getToken(errctx)
		return err
	})
	var gzstats *Cache
	g.Go(func() error {
		gzstats, _ = getCache(errctx, "gzstats")
		return nil
	})
	if err := g.Wait(); err != nil {
		return err
	}

	log := logFn(ctx, w)

	stats := &godash.Stats{}
	if err := unpackCache(gzstats, stats); err != nil {
		return err
	}

	transport := &countTransport{newTransport(ctx), 0}
	defer func() {
		log("Sent %d requests to GitHub", transport.Count())
	}()
	gh := godash.NewGitHubClient("golang/go", token, transport)

	if r.Form.Get("reset_detail") != "" {
		stats.IssueDetailSince = time.Time{}
	}

	start := time.Now()
	if issue := r.Form.Get("issue"); issue != "" {
		num, err := strconv.Atoi(issue)
		if err != nil {
			return err
		}
		err = stats.UpdateIssue(ctx, gh, num, log)
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
	log("Updated issue stats to %v (detail to %v) in %.3f seconds", stats.Since, stats.IssueDetailSince, time.Since(start).Seconds())
	return writeCache(ctx, "gzstats", stats)
}

// GET /stats/release
func release(ctx context.Context, w http.ResponseWriter, req *http.Request) error {
	req.ParseForm()

	// TODO add this to the binary with go-bindata or similar.
	tmpl, err := ioutil.ReadFile("template/release.html")
	if err != nil {
		return err
	}

	t, err := template.New("main").Parse(string(tmpl))
	if err != nil {
		return err
	}

	cycle, _, err := argtoi(req, "cycle")
	if err != nil {
		return err
	}
	if cycle == 0 {
		data, err := loadData(ctx)
		if err != nil {
			return err
		}
		cycle = data.GoReleaseCycle
	}

	return t.Execute(w, struct{ GoReleaseCycle int }{cycle})
}

// GET /stats/raw
func rawHandler(w http.ResponseWriter, r *http.Request) {
	ctx := getContext(r)

	stats := &godash.Stats{}
	if err := loadCache(ctx, "gzstats", stats); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("Content-type", "text/plain")
	(&pretty.Config{PrintStringers: true}).Fprint(w, stats)
}

// GET /stats/svg
func svgHandler(w http.ResponseWriter, req *http.Request) {
	if req.URL.RawQuery == "" {
		http.ServeFile(w, req, "static/svg.html")
		return
	}

	ctx := getContext(req)
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
	// By is the column to group by; if "" all issues will be
	// grouped together. Only "Release" and "Milestone" are
	// supported.
	By string
}

func (o openCount) F(input table.Grouping) table.Grouping {
	return table.MapTables(input, func(_ table.GroupID, t *table.Table) *table.Table {
		groups := make(map[string]countChangeSlice)
		add := func(milestone string, t time.Time, count int) {
			if o.By == "Release" {
				r := milestoneToRelease(milestone)
				if r != "" {
					milestone = r
				}
			}
			groups[milestone] = append(groups[milestone], countChange{t, count})
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
		if o.By != "" {
			var names []string
			for name, s := range groups {
				sort.Sort(s)
				sum := 0
				for _, c := range s {
					names = append(names, name)
					times = append(times, c.t)
					sum += c.delta
					counts = append(counts, sum)
				}
			}
			nt.Add(o.By, names)
		} else {
			var all countChangeSlice
			for _, s := range groups {
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
			p25, p50, p75 := s.Quantile(.25), s.Quantile(.5), s.Quantile(.75)
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
		o := openCount{}
		switch by := req.Form.Get("group"); by {
		case "release":
			o.By = "Release"
		case "milestone":
			o.By = "Milestone"
		case "":
		default:
			return fmt.Errorf("unknown group %q", by)
		}
		plot.Stat(o)
		if o.By != "" {
			plot.GroupBy(o.By)
		}
		plot.SortBy("Time")
		lp := gg.LayerPaths{
			X: "Time",
			Y: "Count",
		}
		if o.By != "" {
			lp.Color = o.By
		}
		plot.Add(gg.LayerSteps{LayerPaths: lp})
		if o.By != "" {
			plot.Add(gg.LayerTooltips{
				X:     "Time",
				Y:     "Count",
				Label: o.By,
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

func releaseData(ctx context.Context, w http.ResponseWriter, req *http.Request) error {
	req.ParseForm()

	stats := &godash.Stats{}
	if err := loadCache(ctx, "gzstats", stats); err != nil {
		return err
	}

	cycle, _, err := argtoi(req, "cycle")
	if err != nil {
		return err
	}
	if cycle == 0 {
		data, err := loadData(ctx)
		if err != nil {
			return err
		}
		cycle = data.GoReleaseCycle
	}

	prefix := fmt.Sprintf("Go1.%d", cycle)

	w.Header().Set("Content-Type", "application/javascript")

	g := gdstats.IssueStats(stats)
	g = openCount{By: "Milestone"}.F(g)
	g = table.Filter(g, func(m string) bool { return strings.HasPrefix(m, prefix) }, "Milestone")
	g = table.SortBy(g, "Time")

	// Dump data; remember that each row only affects one count, so we need to hold the counts from the previous row. Kind of like Pivot.
	data := [][]interface{}{{"Date"}}
	counts := make(map[string]int)
	var (
		maxt time.Time
		maxc int
	)
	for _, gid := range g.Tables() {
		// Find all the milestones that exist
		ms := g.Table(gid).MustColumn("Milestone").([]string)
		for _, m := range ms {
			counts[m] = 0
		}
		// Find the peak of the graph
		ts := g.Table(gid).MustColumn("Time").([]time.Time)
		cs := g.Table(gid).MustColumn("Count").([]int)
		for i, c := range cs {
			if c > maxc {
				maxc = c
				maxt = ts[i]
			}
		}
	}

	// Only show the most recent 6 months of data.
	start := maxt.Add(time.Duration(-6 * 30 * 24 * time.Hour))
	g = table.Filter(g, func(t time.Time) bool { return t.After(start) }, "Time")

	milestones := []string{prefix + "Early", prefix, prefix + "Maybe"}
	for m := range counts {
		switch m {
		case prefix + "Early", prefix, prefix + "Maybe":
		default:
			milestones = append(milestones, m)
		}
	}
	for _, m := range milestones {
		data[0] = append(data[0], m)
	}
	for _, gid := range g.Tables() {
		t := g.Table(gid)
		time := t.MustColumn("Time").([]time.Time)
		milestone := t.MustColumn("Milestone").([]string)
		count := t.MustColumn("Count").([]int)
		for i := range time {
			counts[milestone[i]] = count[i]
			row := []interface{}{time[i].UnixNano() / 1e6}
			for _, m := range milestones {
				row = append(row, counts[m])
			}
			data = append(data, row)
		}
	}
	fmt.Fprintf(w, "var ReleaseData = ")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		return err
	}
	fmt.Fprintf(w, ";\n")
	fmt.Fprintf(w, `
ReleaseData.map(function(row, i) {
  if (i > 0) {
    row[0] = new Date(row[0])
  }
});`)
	return nil
}
