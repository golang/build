// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package benchtab presents benchmark results as comparison tables.
package benchtab

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"

	"github.com/aclements/go-moremath/stats"
	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"
	"golang.org/x/perf/benchproc"
)

// TODO: Color by good/bad (or nothing for unknown units)

// A Builder collects benchmark results into a Tables set.
type Builder struct {
	tableBy, rowBy, colBy *benchproc.Projection
	residue               *benchproc.Projection

	unitField *benchproc.Field

	// tables maps from tableBy to table.
	tables map[benchproc.Key]*builderTable
}

type builderTable struct {
	// Observed row and col Keys within this group. Within the
	// group, we show only the row and col labels for the data in
	// the group, but we sort them according to the global
	// observation order for consistency across groups.
	rows map[benchproc.Key]struct{}
	cols map[benchproc.Key]struct{}

	// cells maps from (row, col) to each cell.
	cells map[TableKey]*builderCell
}

type builderCell struct {
	// values is the observed values in this cell.
	values []float64
	// residue is the set of residue keys mapped to this cell.
	// It is used to check for non-unique keys.
	residue map[benchproc.Key]struct{}
}

// NewBuilder creates a new Builder for collecting benchmark results
// into tables. Each result will be mapped to a Table by tableBy.
// Within each table, the results are mapped to cells by rowBy and
// colBy. Any results within a single cell that vary by residue will
// be reported as warnings. tableBy must have a ".unit" field.
func NewBuilder(tableBy, rowBy, colBy, residue *benchproc.Projection) *Builder {
	tableFields := tableBy.Fields()
	unitField := tableFields[len(tableFields)-1]
	if unitField.Name != ".unit" {
		panic("tableBy projection missing .unit field")
	}
	return &Builder{
		tableBy: tableBy, rowBy: rowBy, colBy: colBy, residue: residue,
		unitField: unitField,
		tables:    make(map[benchproc.Key]*builderTable),
	}
}

// Add adds all of the values in result to the tables in the Builder.
func (b *Builder) Add(result *benchfmt.Result) {
	// Project the result.
	tableKeys := b.tableBy.ProjectValues(result)
	rowKey := b.rowBy.Project(result)
	colKey := b.colBy.Project(result)
	residueKey := b.residue.Project(result)
	cellKey := TableKey{rowKey, colKey}

	// Map to tables.
	for unitI, tableKey := range tableKeys {
		table := b.tables[tableKey]
		if table == nil {
			table = b.newTable()
			b.tables[tableKey] = table
		}

		// Map to a cell.
		c := table.cells[cellKey]
		if c == nil {
			c = new(builderCell)
			c.residue = make(map[benchproc.Key]struct{})
			table.cells[cellKey] = c
			table.rows[rowKey] = struct{}{}
			table.cols[colKey] = struct{}{}
		}

		// Add to the cell.
		c.values = append(c.values, result.Values[unitI].Value)
		c.residue[residueKey] = struct{}{}
	}
}

func (b *Builder) newTable() *builderTable {
	return &builderTable{
		rows:  make(map[benchproc.Key]struct{}),
		cols:  make(map[benchproc.Key]struct{}),
		cells: make(map[TableKey]*builderCell),
	}
}

// TableOpts provides options for constructing the final analysis
// tables from a Builder.
type TableOpts struct {
	// Confidence is the desired confidence level in summary
	// intervals; e.g., 0.95 for 95%.
	Confidence float64

	// Thresholds is the thresholds to use for statistical tests.
	Thresholds *benchmath.Thresholds

	// Units is the unit metadata. This gives distributional
	// assumptions for units, among other properties.
	Units benchfmt.UnitMetadataMap
}

// Tables is a sequence of benchmark statistic tables.
type Tables struct {
	// Tables is a slice of statistic tables. Within a Table, all
	// results have the same table Key (including unit).
	Tables []*Table
	// Keys is a slice of table keys, corresponding 1:1 to
	// the Tables slice. These always end with a ".unit"
	// field giving the unit.
	Keys []benchproc.Key
}

// ToTables finalizes a Builder into a sequence of statistic tables.
func (b *Builder) ToTables(opts TableOpts) *Tables {
	// Sort tables.
	var keys []benchproc.Key
	for k := range b.tables {
		keys = append(keys, k)
	}
	benchproc.SortKeys(keys)

	// We're going to compute table cells in parallel because the
	// statistics are somewhat expensive. This is entirely
	// CPU-bound, so we put a simple concurrency limit on it.
	limit := make(chan struct{}, 2*runtime.GOMAXPROCS(-1))
	var wg sync.WaitGroup

	// Process each table.
	var tables []*Table
	for _, k := range keys {
		cTable := b.tables[k]

		// Get the configured assumption for this unit.
		unit := k.Get(b.unitField)
		assumption := opts.Units.GetAssumption(unit)

		// Sort the rows and columns.
		rowKeys, colKeys := mapKeys(cTable.rows), mapKeys(cTable.cols)
		table := &Table{
			Unit:       unit,
			Opts:       opts,
			Assumption: assumption,
			Rows:       rowKeys,
			Cols:       colKeys,
			Cells:      make(map[TableKey]*TableCell),
		}
		tables = append(tables, table)

		// Create all TableCells and fill their Samples. This
		// is fast enough it's not worth parallelizing. This
		// enables the second pass to look up baselines and
		// their samples.
		for k, cCell := range cTable.cells {
			table.Cells[k] = &TableCell{
				Sample: benchmath.NewSample(cCell.values, opts.Thresholds),
			}
		}

		// Populate cells.
		baselineCfg := colKeys[0]
		wg.Add(len(cTable.cells))
		for k, cCell := range cTable.cells {
			cell := table.Cells[k]

			// Look up the baseline.
			if k.Col != baselineCfg {
				base, ok := table.Cells[TableKey{k.Row, baselineCfg}]
				if ok {
					cell.Baseline = base
				}
			}

			limit <- struct{}{}
			cCell := cCell
			go func() {
				summarizeCell(cCell, cell, assumption, opts.Confidence)
				<-limit
				wg.Done()
			}()
		}
	}
	wg.Wait()

	// Add summary rows to each table.
	for _, table := range tables {
		table.SummaryLabel = "geomean"
		table.Summary = make(map[benchproc.Key]*TableSummary)

		// Count the number of baseline benchmarks. If later
		// columns don't have the same number of baseline
		// pairings, we know the benchmark sets don't match.
		nBase := 0
		baseCol := table.Cols[0]
		for _, row := range table.Rows {
			if _, ok := table.Cells[TableKey{row, baseCol}]; ok {
				nBase++
			}
		}

		for i, col := range table.Cols {
			var s TableSummary
			table.Summary[col] = &s
			isBase := i == 0

			limit <- struct{}{}
			table, col := table, col
			wg.Go(func() {
				summarizeCol(table, col, &s, nBase, isBase)
				<-limit
			})
		}
	}
	wg.Wait()

	return &Tables{tables, keys}
}

func mapKeys(m map[benchproc.Key]struct{}) []benchproc.Key {
	var keys []benchproc.Key
	for k := range m {
		keys = append(keys, k)
	}
	benchproc.SortKeys(keys)
	return keys
}

func summarizeCell(cCell *builderCell, cell *TableCell, assumption benchmath.Assumption, confidence float64) {
	cell.Summary = assumption.Summary(cell.Sample, confidence)

	// If there's a baseline, compute comparison.
	if cell.Baseline != nil {
		cell.Comparison = assumption.Compare(cell.Baseline.Sample, cell.Sample)
	}

	// Warn for non-singular keys in this cell.
	nsk := benchproc.NonSingularFields(mapKeys(cCell.residue))
	if len(nsk) > 0 {
		// Emit a warning.
		var warn strings.Builder
		warn.WriteString("benchmarks vary in ")
		for i, field := range nsk {
			if i > 0 {
				warn.WriteString(", ")
			}
			warn.WriteString(field.Name)
		}

		cell.Sample.Warnings = append(cell.Sample.Warnings, errors.New(warn.String()))
	}
}

func summarizeCol(table *Table, col benchproc.Key, s *TableSummary, nBase int, isBase bool) {
	// Collect cells.
	//
	// This computes the geomean of the summary ratios rather than
	// ratio of the summary geomeans. These are identical *if* the
	// benchmark sets are the same. But if the benchmark sets
	// differ, this leads to more sensible ratios because it's
	// still the geomean of the column, rather than being a
	// comparison of two incomparable numbers. It's still easy to
	// misinterpret, but at least it's not meaningless.
	var summaries, ratios []float64
	badRatio := false
	for _, row := range table.Rows {
		cell, ok := table.Cells[TableKey{row, col}]
		if !ok {
			continue
		}
		summaries = append(summaries, cell.Summary.Center)
		if cell.Baseline != nil {
			var ratio float64
			a, b := cell.Summary.Center, cell.Baseline.Summary.Center
			if a == b {
				// Treat 0/0 as 1.
				ratio = 1
			} else if b == 0 {
				badRatio = true
				// Keep nBase check working.
				ratios = append(ratios, 0)
				continue
			} else {
				ratio = a / b
			}
			ratios = append(ratios, ratio)
		}
	}

	// If the number of cells in this column that had a baseline
	// is the same as the total number of baselines, then we know
	// the benchmark sets match. Otherwise, they don't and these
	// numbers are probably misleading.
	if !isBase && nBase != len(ratios) {
		s.Warnings = append(s.Warnings, fmt.Errorf("benchmark set differs from baseline; geomeans may not be comparable"))
	}

	// Summarize centers.
	gm := stats.GeoMean(summaries)
	if math.IsNaN(gm) {
		s.Warnings = append(s.Warnings, fmt.Errorf("summaries must be >0 to compute geomean"))
	} else {
		s.HasSummary = true
		s.Summary = gm
	}

	// Summarize ratios.
	if !isBase && !badRatio {
		gm := stats.GeoMean(ratios)
		if math.IsNaN(gm) {
			s.Warnings = append(s.Warnings, fmt.Errorf("ratios must be >0 to compute geomean"))
		} else {
			s.HasRatio = true
			s.Ratio = gm
		}
	}
}
