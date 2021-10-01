// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package envutil

import (
	"fmt"
	"reflect"
	"testing"
)

func TestDedup(t *testing.T) {
	tests := []struct {
		in   []string
		want map[string][]string // keyed by GOOS
	}{
		{
			in: []string{"k1=v1", "k2=v2", "K1=v3"},
			want: map[string][]string{
				"windows": {"k2=v2", "K1=v3"},
				"linux":   {"k1=v1", "k2=v2", "K1=v3"},
			},
		},
		{
			in: []string{"k1=v1", "K1=V2", "k1=v3"},
			want: map[string][]string{
				"windows": {"k1=v3"},
				"linux":   {"K1=V2", "k1=v3"},
			},
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			for goos, want := range tt.want {
				t.Run(goos, func(t *testing.T) {
					got := Dedup(goos, tt.in)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("Dedup(%q, %q) = %q; want %q", goos, tt.in, got, want)
					}
				})
			}
		})
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		env  []string
		want map[string]map[string]string // GOOS → key → value
	}{
		{
			env: []string{"k1=v1", "k2=v2", "K1=v3"},
			want: map[string]map[string]string{
				"windows": {"k1": "v3", "k2": "v2", "K1": "v3", "K2": "v2"},
				"linux":   {"k1": "v1", "k2": "v2", "K1": "v3", "K2": ""},
			},
		},
		{
			env: []string{"k1=v1", "K1=V2", "k1=v3"},
			want: map[string]map[string]string{
				"windows": {"k1": "v3", "K1": "v3"},
				"linux":   {"k1": "v3", "K1": "V2"},
			},
		},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			for goos, m := range tt.want {
				t.Run(goos, func(t *testing.T) {
					for k, want := range m {
						got := Get(goos, tt.env, k)
						if got != want {
							t.Errorf("Get(%q, %q, %q) = %q; want %q", goos, tt.env, k, got, want)
						}
					}
				})
			}
		})
	}
}
