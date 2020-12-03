// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// fake provides a fake implementation of a Datastore client to use in testing.
package fake

import (
	"context"
	"log"
	"reflect"

	"cloud.google.com/go/datastore"
	"github.com/googleapis/google-cloud-go-testing/datastore/dsiface"
)

// Client is a fake implementation of dsiface.Client to use in testing.
type Client struct {
	dsiface.Client

	db map[string]map[string]interface{}
}

var _ dsiface.Client = &Client{}

// Close is unimplemented and panics.
func (f *Client) Close() error {
	panic("unimplemented")
}

// AllocateIDs is unimplemented and panics.
func (f *Client) AllocateIDs(context.Context, []*datastore.Key) ([]*datastore.Key, error) {
	panic("unimplemented")
}

// Count is unimplemented and panics.
func (f *Client) Count(context.Context, *datastore.Query) (n int, err error) {
	panic("unimplemented")
}

// Delete is unimplemented and panics.
func (f *Client) Delete(context.Context, *datastore.Key) error {
	panic("unimplemented")
}

// DeleteMulti is unimplemented and panics.
func (f *Client) DeleteMulti(context.Context, []*datastore.Key) (err error) {
	panic("unimplemented")
}

// Get loads the entity stored for key into dst, which must be a struct pointer.
func (f *Client) Get(_ context.Context, key *datastore.Key, dst interface{}) (err error) {
	if f == nil {
		return datastore.ErrNoSuchEntity
	}
	if dst == nil { // get catches nil interfaces; we need to catch nil ptr here
		return datastore.ErrInvalidEntityType
	}
	kdb := f.db[key.Kind]
	if kdb == nil {
		return datastore.ErrNoSuchEntity
	}
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Ptr {
		return datastore.ErrInvalidEntityType
	}
	rd := rv.Elem()
	v := kdb[key.Encode()]
	if v == nil {
		return datastore.ErrNoSuchEntity
	}
	rd.Set(reflect.ValueOf(v).Elem())
	return nil
}

// GetAll runs the provided query in the given context and returns all keys that match that query,
// as well as appending the values to dst.
//
// GetAll currently only supports a query of all entities of a given Kind, and a dst of a slice of pointers to structs.
func (f *Client) GetAll(_ context.Context, q *datastore.Query, dst interface{}) (keys []*datastore.Key, err error) {
	fv := reflect.ValueOf(q).Elem().FieldByName("kind")
	kdb := f.db[fv.String()]
	if kdb == nil {
		return
	}
	s := reflect.ValueOf(dst).Elem()
	for k, v := range kdb {
		dk, err := datastore.DecodeKey(k)
		if err != nil {
			log.Printf("f.GetAll() failed to decode key %q: %v", k, err)
			continue
		}
		keys = append(keys, dk)
		// This value is expected to represent a slice of pointers to structs.
		// ev := reflect.New(s.Type().Elem().Elem())
		// json.Unmarshal(v, ev.Interface())
		s.Set(reflect.Append(s, reflect.ValueOf(v)))
	}
	return
}

// GetMulti is unimplemented and panics.
func (f *Client) GetMulti(context.Context, []*datastore.Key, interface{}) (err error) {
	panic("unimplemented")
}

// Mutate is unimplemented and panics.
func (f *Client) Mutate(context.Context, ...*datastore.Mutation) (ret []*datastore.Key, err error) {
	panic("unimplemented")
}

// NewTransaction is unimplemented and panics.
func (f *Client) NewTransaction(context.Context, ...datastore.TransactionOption) (t dsiface.Transaction, err error) {
	panic("unimplemented")
}

// Put saves the entity src into the datastore with the given key. src must be a struct pointer.
func (f *Client) Put(_ context.Context, key *datastore.Key, src interface{}) (*datastore.Key, error) {
	if f.db == nil {
		f.db = make(map[string]map[string]interface{})
	}
	kdb := f.db[key.Kind]
	if kdb == nil {
		f.db[key.Kind] = make(map[string]interface{})
		kdb = f.db[key.Kind]
	}
	kdb[key.Encode()] = src
	return key, nil
}

// PutMulti is unimplemented and panics.
func (f *Client) PutMulti(context.Context, []*datastore.Key, interface{}) (ret []*datastore.Key, err error) {
	panic("unimplemented")
}

// Run is unimplemented and panics.
func (f *Client) Run(context.Context, *datastore.Query) dsiface.Iterator {
	panic("unimplemented")
}

// RunInTransaction is unimplemented and panics.
func (f *Client) RunInTransaction(context.Context, func(tx dsiface.Transaction) error, ...datastore.TransactionOption) (cmt dsiface.Commit, err error) {
	panic("unimplemented")
}
