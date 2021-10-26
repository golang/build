// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package access

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/idtoken"
	"google.golang.org/grpc/metadata"
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
		iapHeaderJWT:   wantJWTToken,
		iapHeaderEmail: want.Email,
		iapHeaderID:    want.ID,
	}))
	testValidator := func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != wantJWTToken || audience != wantAudience {
			return nil, fmt.Errorf("testValidator(%q, %q); want %q, %q", token, audience, wantJWTToken, wantAudience)
		}
		return &idtoken.Payload{}, nil
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

func TestContextWithIAPMDError(t *testing.T) {
	testCases := []struct {
		desc string
		md   metadata.MD
	}{
		{
			desc: "missing email header",
			md: metadata.New(map[string]string{
				iapHeaderJWT: "jwt",
				iapHeaderID:  "id",
			}),
		},
		{
			desc: "missing id header",
			md: metadata.New(map[string]string{
				iapHeaderJWT:   "jwt",
				iapHeaderEmail: "email",
			}),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			ctx, err := contextWithIAPMD(context.Background(), tc.md)
			if err == nil {
				t.Errorf("contextWithIAPMD(ctx, %v) = %+v, %s; want ctx, error", tc.md, ctx, err)
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
