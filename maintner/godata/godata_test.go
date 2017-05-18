// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package godata

import (
	"context"
	"sync"
	"testing"

	"golang.org/x/build/maintner"
)

func BenchmarkGet(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := Get(context.Background())
		if err != nil {
			b.Fatal(err)
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
	corpusCache, err = Get(context.Background())
	if err != nil {
		tb.Fatalf("getting corpus: %v", err)
	}
	return corpusCache
}

func TestCorpusCheck(t *testing.T) {
	c := getGoData(t)
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestGerritForeachNonChangeRef(t *testing.T) {
	c := getGoData(t)
	c.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		t.Logf("%s:", gp.ServerSlashProject())
		gp.ForeachNonChangeRef(func(ref string, hash maintner.GitHash) error {
			t.Logf("  %s %s", hash, ref)
			return nil
		})
		return nil
	})
}

func TestGitAncestor(t *testing.T) {
	c := getGoData(t)
	tests := []struct {
		subject, ancestor string
		want              bool
	}{
		{"3b5637ff2bd5c03479780995e7a35c48222157c1", "0bb0b61d6a85b2a1a33dcbc418089656f2754d32", true},
		{"0bb0b61d6a85b2a1a33dcbc418089656f2754d32", "3b5637ff2bd5c03479780995e7a35c48222157c1", false},

		{"8f06e217eac10bae4993ca371ade35fecd26270e", "22f1b56dab29d397d2bdbdd603d85e60fb678089", true},
		{"22f1b56dab29d397d2bdbdd603d85e60fb678089", "8f06e217eac10bae4993ca371ade35fecd26270e", false},

		// Same on both sides:
		{"0bb0b61d6a85b2a1a33dcbc418089656f2754d32", "0bb0b61d6a85b2a1a33dcbc418089656f2754d32", false},
		{"3b5637ff2bd5c03479780995e7a35c48222157c1", "3b5637ff2bd5c03479780995e7a35c48222157c1", false},
	}
	for i, tt := range tests {
		subject := c.GitCommit(tt.subject)
		if subject == nil {
			t.Errorf("%d. missing subject commit %q", i, tt.subject)
			continue
		}
		anc := c.GitCommit(tt.ancestor)
		if anc == nil {
			t.Errorf("%d. missing ancestor commit %q", i, tt.ancestor)
			continue
		}
		got := subject.HasAncestor(anc)
		if got != tt.want {
			t.Errorf("HasAncestor(%q, %q) = %v; want %v", tt.subject, tt.ancestor, got, tt.want)
		}
	}
}

func BenchmarkGitAncestor(b *testing.B) {
	c := getGoData(b)
	subject := c.GitCommit("3b5637ff2bd5c03479780995e7a35c48222157c1")
	anc := c.GitCommit("0bb0b61d6a85b2a1a33dcbc418089656f2754d32")
	if subject == nil || anc == nil {
		b.Fatal("missing commit(s)")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !subject.HasAncestor(anc) {
			b.Fatal("wrong answer")
		}
	}
}
