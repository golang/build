// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package dashboard

import (
	"errors"
	"testing"

	"cloud.google.com/go/datastore"
)

var testError = errors.New("this is a test error for cmd/coordinator")

func ignoreTestError(err error) error {
	if !errors.Is(err, testError) {
		return err
	}
	return nil
}

func ignoreNothing(err error) error {
	return err
}

func TestFilterMultiError(t *testing.T) {
	cases := []struct {
		desc    string
		err     error
		ignores []ignoreFunc
		wantErr bool
	}{
		{
			desc:    "single ignored error",
			err:     datastore.MultiError{testError},
			ignores: []ignoreFunc{ignoreTestError},
		},
		{
			desc:    "multiple ignored errors",
			err:     datastore.MultiError{testError, testError},
			ignores: []ignoreFunc{ignoreTestError},
		},
		{
			desc:    "non-ignored error",
			err:     datastore.MultiError{testError, errors.New("this should fail")},
			ignores: []ignoreFunc{ignoreTestError},
			wantErr: true,
		},
		{
			desc:    "nil error",
			ignores: []ignoreFunc{ignoreTestError},
		},
		{
			desc:    "non-multistore error",
			err:     errors.New("this should fail"),
			ignores: []ignoreFunc{ignoreTestError},
			wantErr: true,
		},
		{
			desc:    "no ignoreFuncs",
			err:     errors.New("this should fail"),
			ignores: []ignoreFunc{},
			wantErr: true,
		},
		{
			desc:    "if any ignoreFunc ignores, error is ignored.",
			err:     datastore.MultiError{testError, testError},
			ignores: []ignoreFunc{ignoreNothing, ignoreTestError},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if err := filterMultiError(c.err, c.ignores...); (err != nil) != c.wantErr {
				t.Errorf("filterMultiError(%v, %v) = %v, wantErr = %v", c.err, c.ignores, err, c.wantErr)
			}
		})
	}
}

func TestIgnoreNoSuchEntity(t *testing.T) {
	if err := ignoreNoSuchEntity(datastore.ErrNoSuchEntity); err != nil {
		t.Errorf("ignoreNoSuchEntity(%v) = %v, wanted no error", datastore.ErrNoSuchEntity, err)
	}
	if err := ignoreNoSuchEntity(datastore.ErrInvalidKey); err == nil {
		t.Errorf("ignoreNoSuchEntity(%v) = %v, wanted error", datastore.ErrInvalidKey, err)
	}
}
