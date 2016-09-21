// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package stats

import (
	"time"

	"github.com/aclements/go-gg/table"
	"golang.org/x/build/godash"
)

func truncateWeek(t time.Time) time.Time {
	year, month, day := t.Date()
	loc := t.Location()
	_, week1 := t.ISOWeek()
	for {
		day--
		tnew := time.Date(year, month, day, 0, 0, 0, 0, loc)
		if _, week2 := tnew.ISOWeek(); week1 != week2 {
			return t
		}
		t = tnew
	}
}

// IssueStats prepares a table.Grouping with information about the issues found in s, which can be used for later plotting.
func IssueStats(s *godash.Stats) table.Grouping {
	var nums []int
	var issues []godash.IssueStat
	for num, i := range s.Issues {
		nums = append(nums, num)
		issues = append(issues, *i)
	}
	tb := table.NewBuilder(table.TableFromStructs(issues))
	tb.Add("Number", nums)
	g := table.Grouping(tb.Done())
	for _, in := range []string{"Created", "Closed", "Updated"} {
		g = table.MapCols(g, func(in []time.Time, outD, outW, outM, outY []time.Time) {
			for i, t := range in {
				year, month, day := t.Date()
				loc := t.Location()
				outD[i] = time.Date(year, month, day, 0, 0, 0, 0, loc)
				outW[i] = truncateWeek(t)
				outM[i] = time.Date(year, month, 1, 0, 0, 0, 0, loc)
				outY[i] = time.Date(year, time.January, 1, 0, 0, 0, 0, loc)
			}
		}, in)(in+"Day", in+"Week", in+"Month", in+"Year")
	}
	g = table.MapCols(g, func(created, updated, closed []time.Time, open, updateAge []time.Duration) {
		for i := range created {
			if !closed[i].IsZero() {
				open[i] = closed[i].Sub(created[i])
			} else {
				open[i] = time.Now().Sub(created[i])
			}
			updateAge[i] = time.Now().Sub(updated[i])
		}
	}, "Created", "Updated", "Closed")("Open", "UpdateAge")
	return g
}
