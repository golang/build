// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package access

import (
	"context"
	"fmt"
	"log"

	grpcauth "github.com/grpc-ecosystem/go-grpc-middleware/auth"
	"google.golang.org/api/idtoken"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type contextKeyIAP string

const (
	// contextIAP is the key used to store IAP provided fields in the context.
	contextIAP contextKeyIAP = contextKeyIAP("IAP-JWT")

	// IAPHeaderJWT is the header IAP stores the JWT token in.
	iapHeaderJWT = "X-Goog-IAP-JWT-Assertion"
	// iapHeaderEmail is the header IAP stores the email in.
	iapHeaderEmail = "X-Goog-Authenticated-User-Email"
	// iapHeaderID is the header IAP stores the user id in.
	iapHeaderID = "X-Goog-Authenticated-User-Id"
)

// IAPFields contains the values for the headers retrieved from Identity Aware
// Proxy.
type IAPFields struct {
	Email string
	ID    string
}

// IAPFromContext retrieves the IAPFields stored in the context if it exists.
func IAPFromContext(ctx context.Context) (*IAPFields, error) {
	v := ctx.Value(contextIAP)
	if v == nil {
		return nil, fmt.Errorf("IAP fields not found in context")
	}
	iap, ok := v.(IAPFields)
	if !ok {
		return nil, fmt.Errorf("context value retrieved does not match expected type")
	}
	return &iap, nil
}

// iapAuthFunc creates an authentication function used to create a GRPC interceptor.
// It ensures that the caller has successfully authenticated via IAP. If the caller
// has authenticated, the headers created by IAP will be added to the request scope
// context passed down to the server implementation.
func iapAuthFunc(audience string, validatorFn validator) grpcauth.AuthFunc {
	return func(ctx context.Context) (context.Context, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return ctx, status.Error(codes.Internal, codes.Internal.String())
		}
		jwt := md.Get(iapHeaderJWT)
		if len(jwt) == 0 {
			return ctx, status.Error(codes.Unauthenticated, "IAP JWT not found in request")
		}
		var err error
		if _, err = validatorFn(ctx, jwt[0], audience); err != nil {
			log.Printf("access: error validating JWT: %s", err)
			return ctx, status.Error(codes.Unauthenticated, "unable to authenticate")
		}
		if ctx, err = contextWithIAPMD(ctx, md); err != nil {
			log.Printf("access: unable to set IAP fields in context: %s", err)
			return ctx, status.Error(codes.Unauthenticated, "unable to authenticate")
		}
		return ctx, nil
	}
}

// contextWithIAPMD copies the headers set by IAP into the context.
func contextWithIAPMD(ctx context.Context, md metadata.MD) (context.Context, error) {
	retrieveFn := func(fmd metadata.MD, mdKey string) (string, error) {
		val := fmd.Get(mdKey)
		if len(val) == 0 || val[0] == "" {
			return "", fmt.Errorf("unable to retrieve %s from GRPC metadata", mdKey)
		}
		return val[0], nil
	}
	var iap IAPFields
	var err error
	if iap.Email, err = retrieveFn(md, iapHeaderEmail); err != nil {
		return ctx, fmt.Errorf("unable to retrieve metadata field: %s", iapHeaderEmail)
	}
	if iap.ID, err = retrieveFn(md, iapHeaderID); err != nil {
		return ctx, fmt.Errorf("unable to retrieve metadata field: %s", iapHeaderID)
	}
	return context.WithValue(ctx, contextIAP, iap), nil
}

// RequireIAPAuthUnaryInterceptor creates an authentication interceptor for a GRPC
// server. This requires Identity Aware Proxy authentication. Upon a successful authentication
// the associated headers will be copied into the request context.
func RequireIAPAuthUnaryInterceptor(audience string) grpc.UnaryServerInterceptor {
	return grpcauth.UnaryServerInterceptor(iapAuthFunc(audience, idtoken.Validate))
}

// RequireIAPAuthStreamInterceptor creates an authentication interceptor for a GRPC
// streaming server. This requires Identity Aware Proxy authentication. Upon a successful
// authentication the associated headers will be copied into the request context.
func RequireIAPAuthStreamInterceptor(audience string) grpc.StreamServerInterceptor {
	return grpcauth.StreamServerInterceptor(iapAuthFunc(audience, idtoken.Validate))
}

// validator is a function type for the validator function. The primary purpose is to be able to
// replace the validator function.
type validator func(ctx context.Context, token, audiance string) (*idtoken.Payload, error)

// IAPAudienceGCE returns the jwt audience for GCE and GKE services.
func IAPAudienceGCE(projectNumber int64, serviceID string) string {
	return fmt.Sprintf("/projects/%d/global/backendServices/%s", projectNumber, serviceID)
}

// IAPAudienceAppEngine returns the JWT audience for App Engine services.
func IAPAudienceAppEngine(projectNumber int64, projectID string) string {
	return fmt.Sprintf("/projects/%d/apps/%s", projectNumber, projectID)
}
