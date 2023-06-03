// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fake

import (
	"bytes"
	"context"
	"encoding/gob"
	"testing"
	"time"

	"cloud.google.com/go/datastore"
	"github.com/google/go-cmp/cmp"
)

type author struct {
	Name string
}

func TestClientGet(t *testing.T) {
	cases := []struct {
		desc    string
		db      map[string]map[string][]byte
		key     *datastore.Key
		dst     interface{}
		want    *author
		wantErr bool
	}{
		{
			desc: "correct key",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			key:  datastore.NameKey("Author", "The Trial", nil),
			dst:  new(author),
			want: &author{Name: "Kafka"},
		},
		{
			desc: "incorrect key errors",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			key:     datastore.NameKey("Author", "The Go Programming Language", nil),
			dst:     new(author),
			wantErr: true,
		},
		{
			desc: "nil dst errors",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			key:     datastore.NameKey("Author", "The Go Programming Language", nil),
			wantErr: true,
		},
		{
			desc: "incorrect dst type errors",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			key:     datastore.NameKey("Author", "The Go Programming Language", nil),
			dst:     &time.Time{},
			wantErr: true,
		},
		{
			desc: "non-pointer dst errors",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			key:     datastore.NameKey("Author", "The Go Programming Language", nil),
			dst:     author{},
			wantErr: true,
		},
		{
			desc: "nil key",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			key:     nil,
			dst:     new(author),
			wantErr: true,
		},
		{
			desc: "nil dst errors",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			key:     datastore.NameKey("Author", "The Go Programming Language", nil),
			dst:     nil,
			wantErr: true,
		},
		{
			desc:    "empty db errors",
			key:     datastore.NameKey("Author", "The Go Programming Language", nil),
			dst:     nil,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			cl := &Client{db: c.db}

			if err := cl.Get(context.Background(), c.key, c.dst); (err != nil) != c.wantErr {
				t.Fatalf("cl.Get(_, %v, %v) = %q, wantErr: %v", c.key, c.dst, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if diff := cmp.Diff(c.want, c.dst); diff != "" {
				t.Errorf("author mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClientGetAll(t *testing.T) {
	cases := []struct {
		desc     string
		db       map[string]map[string][]byte
		query    *datastore.Query
		want     []*author
		wantKeys []*datastore.Key
		wantErr  bool
	}{
		{
			desc: "all of a Kind",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			query:    datastore.NewQuery("Author"),
			wantKeys: []*datastore.Key{datastore.NameKey("Author", "The Trial", nil)},
			want:     []*author{{Name: "Kafka"}},
		},
		{
			desc: "all of a non-existent kind",
			db: map[string]map[string][]byte{
				"Author": {datastore.NameKey("Author", "The Trial", nil).Encode(): gobEncode(t, &author{Name: "Kafka"})},
			},
			query:   datastore.NewQuery("Book"),
			wantErr: false,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			cl := &Client{db: c.db}

			var got []*author
			keys, err := cl.GetAll(context.Background(), c.query, &got)
			if (err != nil) != c.wantErr {
				t.Fatalf("cl.Getall(_, %v, %v) = %q, wantErr: %v", c.query, got, err, c.wantErr)
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("authors mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(c.wantKeys, keys); diff != "" {
				t.Errorf("keys mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClientPut(t *testing.T) {
	cl := &Client{}
	src := &author{Name: "Kafka"}
	key := datastore.NameKey("Author", "The Trial", nil)

	gotKey, err := cl.Put(context.Background(), key, src)
	if err != nil {
		t.Fatalf("cl.Put(_, %v, %v) = %v, %q, wanted no error", gotKey, key, src, err)
	}
	got := new(author)
	gobDecode(t, cl.db["Author"][key.Encode()], got)

	if diff := cmp.Diff(src, got); diff != "" {
		t.Errorf("author mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(key, gotKey); diff != "" {
		t.Errorf("keys mismatch (-want +got):\n%s", diff)
	}
}

// gobEncode encodes src with gob, returning the encoded byte slice.
// It will report errors on the provided testing.T.
func gobEncode(t *testing.T, src interface{}) []byte {
	t.Helper()
	dst := bytes.Buffer{}
	e := gob.NewEncoder(&dst)
	if err := e.Encode(src); err != nil {
		t.Errorf("e.Encode(%v) = %q, wanted no error", src, err)
		return nil
	}
	return dst.Bytes()
}

// gobDecode decodes v into dst with gob. It will report errors on the
// provided testing.T.
func gobDecode(t *testing.T, v []byte, dst interface{}) {
	t.Helper()
	d := gob.NewDecoder(bytes.NewReader(v))
	if err := d.Decode(dst); err != nil {
		t.Errorf("d.Decode(%v) = %q, wanted no error", dst, err)
	}
}
