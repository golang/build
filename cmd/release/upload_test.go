package main

import "testing"

func TestFileRe(t *testing.T) {
	shouldMatch := []string{
		"go1.5beta2.src.tar.gz",
		"go1.5.1.linux-386.tar.gz",
		"go1.5.windows-amd64.msi",

		"go1.5beta2.src.tar.gz.asc",
		"go1.5.1.linux-386.tar.gz.asc",
		"go1.5.windows-amd64.msi.asc",
	}
	for _, fn := range shouldMatch {
		t.Run(fn, func(t *testing.T) {
			if !fileRe.MatchString(fn) {
				t.Fatalf("want %q to match, didn't", fn)
			}
		})
	}
}
