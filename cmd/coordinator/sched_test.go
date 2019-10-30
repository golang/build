// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"testing"
	"time"
)

func TestSchedLess(t *testing.T) {
	t1, t2 := time.Unix(1, 0), time.Unix(2, 0)
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
				requestTime: t2,
			},
			b: &SchedItem{
				requestTime: t1,
			},
			want: true,
		},
		{
			name: "reg LIFO, greater",
			a: &SchedItem{
				requestTime: t1,
			},
			b: &SchedItem{
				requestTime: t2,
			},
			want: false,
		},
	}
	for _, tt := range tests {
		got := schedLess(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("%s: got %v; want %v", tt.name, got, tt.want)
		}
	}

}
