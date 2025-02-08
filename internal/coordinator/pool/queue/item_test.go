// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package queue

import (
	"testing"
	"time"
)

func TestSchedItemLess(t *testing.T) {
	t1 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	tests := []struct {
		name string
		a, b *SchedItem
		want bool
	}{
		{
			name: "gomote over reg",
			a: &SchedItem{
				IsGomote:    true,
				RequestTime: t2,
			},
			b: &SchedItem{
				RequestTime: t1,
			},
			want: true,
		},
		{
			name: "gomote over try",
			a: &SchedItem{
				IsGomote:    true,
				RequestTime: t2,
			},
			b: &SchedItem{
				IsTry:       true,
				RequestTime: t1,
			},
			want: true,
		},
		{
			name: "try over reg",
			a: &SchedItem{
				IsTry:       true,
				RequestTime: t2,
			},
			b: &SchedItem{
				RequestTime: t1,
			},
			want: true,
		},
		{
			name: "try FIFO",
			a: &SchedItem{
				IsTry:       true,
				RequestTime: t1,
			},
			b: &SchedItem{
				IsTry:       true,
				RequestTime: t2,
			},
			want: true,
		},
		{
			name: "reg LIFO, less",
			a: &SchedItem{
				CommitTime:  t2,
				RequestTime: t1, // shouldn't be used
			},
			b: &SchedItem{
				CommitTime:  t1,
				RequestTime: t2, // shouldn't be used
			},
			want: true,
		},
		{
			name: "release branch less than master",
			a: &SchedItem{
				CommitTime: t2,
				Branch:     "release-branch.go1.19",
			},
			b: &SchedItem{
				CommitTime: t1,
				Branch:     "master",
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.a.Less(tt.b)
			if got != tt.want {
				t.Errorf("got %v; want %v", got, tt.want)
			}
			if got == tt.b.Less(tt.a) {
				t.Errorf("not reflexive")
			}
		})
	}
}
