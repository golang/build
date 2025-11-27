// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package secret

import (
	"context"
	"flag"
	"fmt"
	"io"
	"reflect"
	"testing"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeSecretClient struct {
	accessReturnError error
	accessSecretMap   map[string]string // map[path] = secret

	closeReturnError error
}

func (fsc *fakeSecretClient) AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	if ctx == nil || req == nil {
		return nil, status.Error(codes.InvalidArgument, "ctx or req are nil")
	}
	if secret, ok := fsc.accessSecretMap[req.GetName()]; ok {
		return &secretmanagerpb.AccessSecretVersionResponse{
			Payload: &secretmanagerpb.SecretPayload{
				Data: []byte(secret),
			},
		}, nil
	}
	return nil, status.Error(codes.NotFound, "secret not found")
}

func (fsc *fakeSecretClient) Close() error {
	return fsc.closeReturnError
}

func TestRetrieve(t *testing.T) {
	testCases := []struct {
		desc          string
		fakeClient    secretClient
		ctx           context.Context
		name          string
		projectID     string
		wantSecret    string
		wantErrorCode codes.Code
	}{
		{
			desc:          "nil-params",
			fakeClient:    &fakeSecretClient{},
			ctx:           nil,
			name:          "x",
			projectID:     "y",
			wantSecret:    "",
			wantErrorCode: codes.InvalidArgument,
		},
		{
			desc:          "secret-not-found",
			fakeClient:    &fakeSecretClient{},
			ctx:           context.Background(),
			name:          "x",
			projectID:     "y",
			wantSecret:    "",
			wantErrorCode: codes.NotFound,
		},
		{
			desc: "secret-found",
			fakeClient: &fakeSecretClient{
				accessReturnError: nil,
				accessSecretMap: map[string]string{
					buildNamePath("projecto", "nombre", "latest"): "secreto",
				},
			},
			ctx:           context.Background(),
			name:          "nombre",
			projectID:     "projecto",
			wantSecret:    "secreto",
			wantErrorCode: codes.OK,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := &Client{
				client:    tc.fakeClient,
				projectID: tc.projectID,
			}
			gotSecret, gotErr := c.Retrieve(tc.ctx, tc.name)
			gotErrStatus, _ := status.FromError(gotErr)
			if gotErrStatus.Code() != tc.wantErrorCode || gotSecret != tc.wantSecret {
				t.Errorf("Retrieve(%v, %q) = %q, %v, wanted %q, %v", tc.ctx, tc.name, gotSecret, gotErr, tc.wantSecret, tc.wantErrorCode)
			}
		})
	}
}

func TestClose(t *testing.T) {
	randomErr := fmt.Errorf("close error")

	testCases := []struct {
		desc       string
		fakeClient secretClient
		wantError  error
	}{
		{
			desc:       "no-error",
			fakeClient: &fakeSecretClient{},
			wantError:  nil,
		},
		{
			desc: "error",
			fakeClient: &fakeSecretClient{
				closeReturnError: randomErr,
			},
			wantError: randomErr,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := &Client{
				client: tc.fakeClient,
			}
			if gotErr := c.Close(); gotErr != tc.wantError {
				t.Errorf("Close() = %v, wanted %v", gotErr, tc.wantError)
			}
		})
	}
}

func TestBuildNamePath(t *testing.T) {
	want := "projects/x/secrets/y/versions/z"
	got := buildNamePath("x", "y", "z")
	if got != want {
		t.Errorf("BuildVersionNumber(%s, %s, %s) = %q; want=%q", "x", "y", "z", got, want)
	}
}

func TestFlag(t *testing.T) {
	r := &FlagResolver{
		Context: context.Background(),
		Client: &fakeSecretClient{
			accessSecretMap: map[string]string{
				buildNamePath("project1", "secret1", "latest"): "supersecret",
				buildNamePath("project2", "secret2", "latest"): "tippytopsecret",
			},
		},
		DefaultProjectID: "project1",
	}

	tests := []struct {
		flagVal, wantVal string
		wantErr          bool
	}{
		{"hey", "hey", false},
		{"secret:secret1", "supersecret", false},
		{"secret:project2/secret2", "tippytopsecret", false},
		{"secret:foo", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.flagVal, func(t *testing.T) {
			fs := flag.NewFlagSet("", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			flagVal := r.Flag(fs, "testflag", "usage")
			err := fs.Parse([]string{"--testflag", tt.flagVal})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("flag parsing succeeded, should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("flag parsing failed: %v", err)
			}
			if *flagVal != tt.wantVal {
				t.Errorf("flag value = %q, want %q", *flagVal, tt.wantVal)
			}
		})
	}
}

type jsonValue struct {
	Foo, Bar int
}

func TestJSONFlag(t *testing.T) {
	r := &FlagResolver{
		Context: context.Background(),
		Client: &fakeSecretClient{
			accessSecretMap: map[string]string{
				buildNamePath("project1", "secret1", "latest"): `{"Foo": 1, "Bar": 2}`,
				buildNamePath("project1", "secret2", "latest"): `i am not json`,
			},
		},
		DefaultProjectID: "project1",
	}
	tests := []struct {
		flagVal   string
		wantValue *jsonValue
		wantErr   bool
	}{
		{"secret:secret1", &jsonValue{Foo: 1, Bar: 2}, false},
		{"secret:secret2", nil, true},
		{`{"Foo":0, "Bar":1}`, &jsonValue{Foo: 0, Bar: 1}, false},
	}

	for _, tt := range tests {
		t.Run(tt.flagVal, func(t *testing.T) {
			fs := flag.NewFlagSet("", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			value := &jsonValue{}
			r.JSONVarFlag(fs, value, "testflag", "usage")
			err := fs.Parse([]string{"--testflag", tt.flagVal})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("flag parsing succeeded, should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("flag parsing failed: %v", err)
			}
			if !reflect.DeepEqual(value, tt.wantValue) {
				t.Errorf("flag value = %#v, want %#v", value, tt.wantValue)
			}
		})
	}

}
