// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package benchtab

import (
	"golang.org/x/perf/benchmath"
	"golang.org/x/perf/benchproc"
	"golang.org/x/perf/benchunit"
)

// A Table summarizes and compares benchmark results in a 2D grid.
// Each cell summarizes a Sample of results with identical row and
// column Keys. Comparisons are done within each row between the
// Sample in the first column and the Samples in any remaining
// columns.
type Table struct {
	// Opts is the configuration options for this table.
	Opts TableOpts

	// Unit is the benchmark unit of all samples in this Table.
	Unit string

	// Assumption is the distributional assumption used for all
	// samples in this table.
	Assumption benchmath.Assumption

	// Rows and Cols give the sequence of row and column Keys
	// in this table. All row Keys have the same Projection and all
	// col Keys have the same Projection.
	Rows, Cols []benchproc.Key

	// Cells is the cells in the body of this table. Each key in
	// this map is a pair of some Key from Rows and some Key
	// from Cols. However, not all Pairs may be present in the
	// map.
	Cells map[TableKey]*TableCell

	// Summary is the final row of this table, which gives summary
	// information across all benchmarks in this table. It is
	// keyed by Cols.
	Summary map[benchproc.Key]*TableSummary

	// SummaryLabel is the label for the summary row.
	SummaryLabel string
}

// TableKey is a map key used to index a single cell in a Table.
type TableKey struct {
	Row, Col benchproc.Key
}

// TableCell is a single cell in a Table. It represents a sample of
// benchmark results with the same row and column Key.
type TableCell struct {
	// Sample is the set of benchmark results in this cell.
	Sample *benchmath.Sample

	// Summary is the summary of Sample, as computed by the
	// Table's distributional assumption.
	Summary benchmath.Summary

	// Baseline is the baseline cell used for comparisons with
	// this cell, or nil if there is no comparison. This is the
	// cell in the first column of this cell's row, if any.
	Baseline *TableCell

	// Comparison is the comparison with the Baseline cell, as
	// computed by the Table's distributional assumption. If
	// Baseline is nil, this value is meaningless.
	Comparison benchmath.Comparison
}

// TableSummary is a cell that summarizes a column of a Table.
// It appears in the last row of a table.
type TableSummary struct {
	// HasSummary indicates that Summary is valid.
	HasSummary bool
	// Summary summarizes all of the TableCell.Summary values in
	// this column.
	Summary float64

	// HasRatio indicates that Ratio is valid.
	HasRatio bool
	// Ratio summarizes all of the TableCell.Comparison values in
	// this column.
	Ratio float64

	// Warnings is a list of warnings for this summary cell.
	Warnings []error
}

// RowScaler returns a common scaler for the values in row.
func (t *Table) RowScaler(row benchproc.Key, unitClass benchunit.Class) benchunit.Scaler {
	// Collect the row summaries.
	var values []float64
	for _, col := range t.Cols {
		cell, ok := t.Cells[TableKey{row, col}]
		if ok {
			values = append(values, cell.Summary.Center)
		}
	}
	return benchunit.CommonScale(values, unitClass)
}
