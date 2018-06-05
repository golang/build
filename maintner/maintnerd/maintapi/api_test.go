// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintapi

import (
	"context"
	"flag"
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/build/maintner/maintnerd/apipb"
)

func TestGetRef(t *testing.T) {
	c := getGoData(t)
	s := apiService{c}
	req := &apipb.GetRefRequest{
		GerritServer:  "go.googlesource.com",
		GerritProject: "go",
		Ref:           "refs/heads/master",
	}
	res, err := s.GetRef(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Value) != 40 {
		t.Errorf("go master ref = %q; want length 40 string", res.Value)
	}

	// Bogus ref
	req.Ref = "NOT EXIST REF"
	res, err = s.GetRef(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Value) != 0 {
		t.Errorf("go bogus ref = %q; want empty string", res.Value)
	}

	// Bogus project
	req.GerritProject = "NOT EXIST PROJ"
	_, err = s.GetRef(context.Background(), req)
	if got, want := fmt.Sprint(err), "unknown gerrit project"; got != want {
		t.Errorf("error for bogus project = %q; want %q", got, want)
	}
}

var hitGerrit = flag.Bool("hit_gerrit", false, "query production Gerrit in TestFindTryWork")

func TestFindTryWork(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	if !*hitGerrit {
		t.Skip("skipping without flag --hit_gerrit")
	}
	c := getGoData(t)
	s := apiService{c}
	req := &apipb.GoFindTryWorkRequest{}
	t0 := time.Now()
	res, err := s.GoFindTryWork(context.Background(), req)
	d0 := time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}

	// Just for interactive debugging. This is using live data.
	// The stable tests are in TestTryWorkItem and TestTryBotStatus.
	t.Logf("Current: %v", res)

	t1 := time.Now()
	res, err = s.GoFindTryWork(context.Background(), req)
	d1 := time.Since(t1)
	t.Logf("Latency: %v, then %v", d0, d1)
	t.Logf("Cached: %v, %v", res, err)
}

func TestTryBotStatus(t *testing.T) {
	c := getGoData(t)
	tests := []struct {
		proj      string
		clnum     int32
		msgCutoff int
		wantTry   bool
		wantDone  bool
	}{
		{"go", 51430, 1, true, false},
		{"go", 51430, 2, true, false},
		{"go", 51430, 3, true, true},

		{"build", 48968, 5, true, false},  // adding trybot (coordinator ignores for "build" repo)
		{"build", 48968, 6, false, false}, // removing it
	}
	for _, tt := range tests {
		cl := c.Gerrit().Project("go.googlesource.com", tt.proj).CL(tt.clnum)
		if cl == nil {
			t.Errorf("CL %d in %s not found", tt.clnum, tt.proj)
			continue
		}
		old := *cl // save before mutations
		cl.Version = cl.Messages[tt.msgCutoff-1].Version
		cl.Messages = cl.Messages[:tt.msgCutoff]
		gotTry, gotDone := tryBotStatus(cl, false /* not staging */)
		if gotTry != tt.wantTry || gotDone != tt.wantDone {
			t.Errorf("tryBotStatus(%q, %d) after %d messages = try/done %v, %v; want %v, %v",
				tt.proj, tt.clnum, tt.msgCutoff, gotTry, gotDone, tt.wantTry, tt.wantDone)
			for _, msg := range cl.Messages {
				t.Logf("  msg ver=%d, text=%q", msg.Version, msg.Message)
			}
		}
		*cl = old // restore
	}

}

func TestTryWorkItem(t *testing.T) {
	c := getGoData(t)
	tests := []struct {
		proj  string
		clnum int32
		want  string
	}{
		// Same Change-Id, different branch:
		{"go", 51430, `project:"go" branch:"master" change_id:"I0bcae339624e7d61037d9ea0885b7bd07491bbb6" commit:"45a4609c0ae214e448612e0bc0846e2f2682f1b2" `},
		{"go", 51450, `project:"go" branch:"release-branch.go1.9" change_id:"I0bcae339624e7d61037d9ea0885b7bd07491bbb6" commit:"7320506bc58d3a55eff2c67b2ec65cfa94f7b0a7" `},
		// Different project:
		{"build", 51432, `project:"build" branch:"master" change_id:"I1f71836da7008e58d3e76e2cc3170e96cd57ddf6" commit:"9251bc9950baff61d95da0761e2e4bfab61ed210" `},
	}
	for _, tt := range tests {
		cl := c.Gerrit().Project("go.googlesource.com", tt.proj).CL(tt.clnum)
		if cl == nil {
			t.Errorf("CL %d in %s not found", tt.clnum, tt.proj)
			continue
		}
		got := fmt.Sprint(tryWorkItem(cl))
		if got != tt.want {
			t.Errorf("tryWorkItem(%q, %v) = %#q; want %#q", tt.proj, tt.clnum, got, tt.want)
		}
	}
}

var (
	corpusMu    sync.Mutex
	corpusCache *maintner.Corpus
)

func getGoData(tb testing.TB) *maintner.Corpus {
	corpusMu.Lock()
	defer corpusMu.Unlock()
	if corpusCache != nil {
		return corpusCache
	}
	var err error
	corpusCache, err = godata.Get(context.Background())
	if err != nil {
		tb.Fatalf("getting corpus: %v", err)
	}
	return corpusCache
}
