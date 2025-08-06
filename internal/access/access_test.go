// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package access

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/idtoken"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestIAPFromContextError(t *testing.T) {
	ctx := context.WithValue(context.Background(), contextIAP, "dance party")
	if got, err := IAPFromContext(ctx); got != nil || err == nil {
		t.Errorf("IAPFromContext(ctx) = %v, %s; want error", got, err)
	}
}

func TestIAPAuthFunc(t *testing.T) {
	want := &IAPFields{
		Email: "charlie@brown.com",
		ID:    "chaz.service.moo",
	}
	wantJWTToken := "eyJhb.eyJzdDIyfQ.Bh17Fl2gFjyLh6mo1GjqSPnGUg8MRLAE1Vdo3Z3gvdI"
	wantAudience := "foo/bar/zar"
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		iapHeaderJWT: wantJWTToken,
	}))
	testValidator := func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != wantJWTToken || audience != wantAudience {
			return nil, fmt.Errorf("testValidator(%q, %q); want %q, %q", token, audience, wantJWTToken, wantAudience)
		}
		return &idtoken.Payload{
			Issuer:   "https://cloud.google.com/iap",
			Audience: audience,
			Expires:  time.Now().Add(time.Minute).Unix(),
			IssuedAt: time.Now().Add(-time.Minute).Unix(),
			Subject:  want.ID,
			Claims: map[string]any{
				"email": want.Email,
			},
		}, nil
	}
	authFunc := iapAuthFunc(wantAudience, testValidator)
	gotCtx, err := authFunc(ctx)
	if err != nil {
		t.Fatalf("authFunc(ctx) = %+v, %s; want ctx, no error", gotCtx, err)
	}
	got, err := IAPFromContext(gotCtx)
	if err != nil {
		t.Fatalf("IAPFromContext(ctx) = %+v, %s; want no error", got, err)
	}
	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("ctx.Value(%v) mismatch (-got, +want):\n%s", contextIAP, diff)
	}
}

func TestIAPAuthFuncError(t *testing.T) {
	testCases := []struct {
		desc      string
		validator validator
		ctx       context.Context
		audience  string
		wantErr   codes.Code
	}{
		{
			desc: "invalid context",
			validator: func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
				return &idtoken.Payload{
					Issuer:   "https://cloud.google.com/iap",
					Audience: audience,
					Expires:  time.Now().Add(time.Minute).Unix(),
					IssuedAt: time.Now().Add(-time.Minute).Unix(),
					Subject:  "chaz-service.moo",
					Claims: map[string]any{
						"email": "mary@foo.com",
					},
				}, nil
			},
			ctx:      context.Background(),
			audience: "foo/bar/zar",
			wantErr:  codes.Internal,
		},
		{
			desc: "missing jwt header",
			validator: func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
				return &idtoken.Payload{
					Issuer:   "https://cloud.google.com/iap",
					Audience: audience,
					Expires:  time.Now().Add(time.Minute).Unix(),
					IssuedAt: time.Now().Add(-time.Minute).Unix(),
					Subject:  "chaz-service.moo",
					Claims: map[string]any{
						"email": "mary@foo.com",
					},
				}, nil
			},
			ctx:      metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{})),
			audience: "foo/bar/zar",
			wantErr:  codes.Unauthenticated,
		},
		{
			desc: "failed validation",
			validator: func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
				return nil, errors.New("validation failed")
			},
			ctx: metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
				iapHeaderJWT: "xyz",
			})),
			audience: "foo/bar/zar",
			wantErr:  codes.Unauthenticated,
		},
		{
			desc: "wrong issuer",
			validator: func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
				return &idtoken.Payload{
					Issuer:   "https://cloud.google.com/iap-wrong",
					Audience: audience,
					Expires:  time.Now().Add(time.Minute).Unix(),
					IssuedAt: time.Now().Add(-time.Minute).Unix(),
					Subject:  "chaz-service.moo",
					Claims: map[string]any{
						"email": "mary@foo.com",
					},
				}, nil
			},
			ctx: metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
				iapHeaderJWT: "xyz",
			})),
			audience: "foo/bar/zar",
			wantErr:  codes.Unauthenticated,
		},
		{
			desc: "jwt expired",
			validator: func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
				return &idtoken.Payload{
					Issuer:   "https://cloud.google.com/iap",
					Audience: audience,
					Expires:  time.Now().Add(-time.Minute).Unix(),
					IssuedAt: time.Now().Add(-10 * time.Minute).Unix(),
					Subject:  "chaz-service.moo",
					Claims: map[string]any{
						"email": "mary@foo.com",
					},
				}, nil
			},
			ctx: metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
				iapHeaderJWT: "xyz",
			})),
			audience: "foo/bar/zar",
			wantErr:  codes.Unauthenticated,
		},
		{
			desc: "jwt missing subject",
			validator: func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
				return &idtoken.Payload{
					Issuer:   "https://cloud.google.com/iap",
					Audience: audience,
					Expires:  time.Now().Add(time.Minute).Unix(),
					IssuedAt: time.Now().Add(-time.Minute).Unix(),
					Claims: map[string]any{
						"email": "mary@foo.com",
					},
				}, nil
			},
			ctx: metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
				iapHeaderJWT: "xyz",
			})),
			audience: "foo/bar/zar",
			wantErr:  codes.Unauthenticated,
		},
		{
			desc: "jwt missing claims",
			validator: func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
				return &idtoken.Payload{
					Issuer:   "https://cloud.google.com/iap",
					Audience: audience,
					Expires:  time.Now().Add(time.Minute).Unix(),
					IssuedAt: time.Now().Add(-time.Minute).Unix(),
					Subject:  "chaz-service.moo",
				}, nil
			},
			ctx: metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
				iapHeaderJWT: "xyz",
			})),
			audience: "foo/bar/zar",
			wantErr:  codes.Unauthenticated,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			authFunc := iapAuthFunc(tc.audience, tc.validator)
			gotCtx, err := authFunc(tc.ctx)
			if err == nil {
				t.Fatalf("authFunc(ctx) = %s, nil; want error", gotCtx)
			}
			if status.Code(err) != tc.wantErr {
				t.Fatalf("authFunc(ctx) = nil, %s; want %s", status.Code(err), tc.wantErr)
			}
		})
	}
}

func TestIAPAudienceGCE(t *testing.T) {
	want := "/projects/11/global/backendServices/bar"
	if got := IAPAudienceGCE(11, "bar"); got != want {
		t.Errorf("IAPAudienceGCE(11, bar) = %s; want %s", got, want)
	}
}

func TestIAPAudience(t *testing.T) {
	want := "/projects/11/apps/bar"
	if got := IAPAudienceAppEngine(11, "bar"); got != want {
		t.Errorf("IAPAudienceAppEngine(11, bar) = %s; want %s", got, want)
	}
}
