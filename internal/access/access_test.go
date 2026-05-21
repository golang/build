// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package access

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestIAPAuth(t *testing.T) {
	want := &IAPFields{
		Email: "charlie@brown.com",
		ID:    "chaz.service.moo",
	}
	const wantJWTToken = "eyJhb.eyJzdDIyfQ.Bh17Fl2gFjyLh6mo1GjqSPnGUg8MRLAE1Vdo3Z3gvdI"
	const wantAudience = "foo/bar/zar"
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

	t.Run("Func", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
			iapHeaderJWT: wantJWTToken,
		}))
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
	})
	t.Run("Handler", func(t *testing.T) {
		var gotCtx context.Context
		h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			gotCtx = req.Context()
			w.WriteHeader(http.StatusOK)
		})
		authHandler := iapAuthHandler(h, wantAudience, testValidator)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add(iapHeaderJWT, wantJWTToken)

		w := httptest.NewRecorder()
		authHandler.ServeHTTP(w, req)

		if resp := w.Result(); resp.StatusCode != http.StatusOK {
			t.Fatalf("authHandler.ServeHTTP(w, req) got code %d want 200", resp.StatusCode)
		}

		got, err := IAPFromContext(gotCtx)
		if err != nil {
			t.Fatalf("IAPFromContext(%+v) = %+v, %s; want no error", gotCtx, got, err)
		}
		if diff := cmp.Diff(got, want); diff != "" {
			t.Errorf("ctx.Value(%v) mismatch (-got, +want):\n%s", contextIAP, diff)
		}
	})
}

func TestIAPAuthError(t *testing.T) {
	testCases := []struct {
		desc         string
		validator    validator
		fields       map[string]string
		audience     string
		wantGRPCErr  codes.Code
		wantHTTPCode int
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
			fields:       nil,
			audience:     "foo/bar/zar",
			wantGRPCErr:  codes.Internal,
			wantHTTPCode: http.StatusUnauthorized,
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
			fields:       map[string]string{},
			audience:     "foo/bar/zar",
			wantGRPCErr:  codes.Unauthenticated,
			wantHTTPCode: http.StatusUnauthorized,
		},
		{
			desc: "failed validation",
			validator: func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
				return nil, errors.New("validation failed")
			},
			fields: map[string]string{
				iapHeaderJWT: "xyz",
			},
			audience:     "foo/bar/zar",
			wantGRPCErr:  codes.Unauthenticated,
			wantHTTPCode: http.StatusUnauthorized,
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
			fields: map[string]string{
				iapHeaderJWT: "xyz",
			},
			audience:     "foo/bar/zar",
			wantGRPCErr:  codes.Unauthenticated,
			wantHTTPCode: http.StatusUnauthorized,
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
			fields: map[string]string{
				iapHeaderJWT: "xyz",
			},
			audience:     "foo/bar/zar",
			wantGRPCErr:  codes.Unauthenticated,
			wantHTTPCode: http.StatusUnauthorized,
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
			fields: map[string]string{
				iapHeaderJWT: "xyz",
			},
			audience:     "foo/bar/zar",
			wantGRPCErr:  codes.Unauthenticated,
			wantHTTPCode: http.StatusUnauthorized,
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
			fields: map[string]string{
				iapHeaderJWT: "xyz",
			},
			audience:     "foo/bar/zar",
			wantGRPCErr:  codes.Unauthenticated,
			wantHTTPCode: http.StatusUnauthorized,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Run("grpc", func(t *testing.T) {
				ctx := context.Background()
				if tc.fields != nil {
					ctx = metadata.NewIncomingContext(ctx, metadata.New(tc.fields))
				}
				authFunc := iapAuthFunc(tc.audience, tc.validator)
				gotCtx, err := authFunc(ctx)
				if err == nil {
					t.Fatalf("authFunc(ctx) = %s, nil; want error", gotCtx)
				}
				if status.Code(err) != tc.wantGRPCErr {
					t.Fatalf("authFunc(ctx) = nil, %s; want %s", status.Code(err), tc.wantGRPCErr)
				}
			})

			t.Run("http", func(t *testing.T) {
				h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
				authHandler := iapAuthHandler(h, tc.audience, tc.validator)

				req := httptest.NewRequest(http.MethodGet, "/", nil)
				for k, v := range tc.fields {
					req.Header.Add(k, v)
				}

				w := httptest.NewRecorder()
				authHandler.ServeHTTP(w, req)

				if resp := w.Result(); resp.StatusCode != tc.wantHTTPCode {
					t.Fatalf("authHandler.ServeHTTP(w, req) got code %d want %d", resp.StatusCode, tc.wantHTTPCode)
				}
			})
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
