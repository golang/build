// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gophers_test

import (
	"testing"

	"golang.org/x/build/internal/gophers"
)

func TestPersonName(t *testing.T) {
	for _, tt := range []struct {
		id   string
		want string
	}{
		{id: "@golang/tools-team", want: "Tools Team"},
	} {
		p := gophers.GetPerson(tt.id)
		if p == nil {
			t.Errorf("no person with id %q", tt.id)
			continue
		}
		got := p.Name
		if got != tt.want {
			t.Errorf("person with id %q now has name %q but used to have %q; is that change intentional?", tt.id, got, tt.want)
		}
	}
}

// Test that a few people whose Gerrit emails have been broken
// in the past still have the expected Gerrit email. This test
// is mostly needed until golang.org/issue/34259 is resolved.
func TestGerritEmail(t *testing.T) {
	for _, tt := range []struct {
		id   string
		want string
	}{
		{id: "Andrew Bonventre", want: "andybons@golang.org"},
		{id: "Carl Mastrangelo", want: "notcarl@google.com"},
		{id: "Chris McGee", want: "newton688@gmail.com"},
		{id: "Eric Lagergren", want: "ericscottlagergren@gmail.com"},
		{id: "Filippo Valsorda", want: "filippo@golang.org"},
		{id: "Guillaume J. Charmes", want: "guillaume@charmes.net"},
		{id: "Harshavardhana", want: "hrshvardhana@gmail.com"},
		{id: "Jean de Klerk", want: "deklerk@google.com"},
		{id: "Joe Tsai", want: "joetsai@digital-static.net"},
		{id: "Martin Möhrmann", want: "moehrmann@google.com"},
		{id: "Olivier Poitrey", want: "rs@netflix.com"},
		{id: "Paul Jolly", want: "paul@myitcv.org.uk"},
		{id: "Ralph Corderoy", want: "ralph@inputplus.co.uk"},
		{id: "Raul Silvera", want: "rsilvera@google.com"},
		{id: "Richard Miller", want: "millerresearch@gmail.com"},
		{id: "Sebastien Binet", want: "seb.binet@gmail.com"},
		{id: "Tobias Klauser", want: "tobias.klauser@gmail.com"},
		{id: "Vitor De Mario", want: "vitordemario@gmail.com"},

		// We're still figuring out how to handle teams.
		// At this time their Gerrit email is unset.
		{id: "Fuzzing Team", want: ""},
		{id: "Pkgsite Team", want: ""},
		{id: "Tools Team", want: ""},
	} {
		p := gophers.GetPerson(tt.id)
		if p == nil {
			t.Errorf("no person with id %q", tt.id)
			continue
		}
		got := p.Gerrit
		if got != tt.want {
			t.Errorf("person with id %q now has Gerrit email %q but used to have %q; is that change intentional?", tt.id, got, tt.want)
		}
	}
}
