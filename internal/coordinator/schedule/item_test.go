// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package schedule

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
				requestTime: t2,
			},
			b: &SchedItem{
				requestTime: t1,
			},
			want: true,
		},
		{
			name: "gomote over try",
			a: &SchedItem{
				IsGomote:    true,
				requestTime: t2,
			},
			b: &SchedItem{
				IsTry:       true,
				requestTime: t1,
			},
			want: true,
		},
		{
			name: "try over reg",
			a: &SchedItem{
				IsTry:       true,
				requestTime: t2,
			},
			b: &SchedItem{
				requestTime: t1,
			},
			want: true,
		},
		{
			name: "try FIFO, less",
			a: &SchedItem{
				IsTry:       true,
				requestTime: t1,
			},
			b: &SchedItem{
				IsTry:       true,
				requestTime: t2,
			},
			want: true,
		},
		{
			name: "try FIFO, greater",
			a: &SchedItem{
				IsTry:       true,
				requestTime: t2,
			},
			b: &SchedItem{
				IsTry:       true,
				requestTime: t1,
			},
			want: false,
		},
		{
			name: "reg LIFO, less",
			a: &SchedItem{
				CommitTime:  t2,
				requestTime: t1, // shouldn't be used
			},
			b: &SchedItem{
				CommitTime:  t1,
				requestTime: t2, // shouldn't be used
			},
			want: true,
		},
		{
			name: "reg LIFO, greater",
			a: &SchedItem{
				CommitTime:  t1,
				requestTime: t2, // shouldn't be used
			},
			b: &SchedItem{
				CommitTime:  t2,
				requestTime: t1, // shouldn't be used
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.a.Less(tt.b)
			if got != tt.want {
				t.Errorf("got %v; want %v", got, tt.want)
			}
		})
	}
}
