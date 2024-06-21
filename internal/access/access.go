// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package access

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

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

	// IAPSkipAudienceValidation is the audience string used when the validation is not
	// necessary. https://pkg.go.dev/google.golang.org/api/idtoken#Validate
	IAPSkipAudienceValidation = ""
)

// IAPFields contains the values for the headers retrieved from Identity Aware
// Proxy.
type IAPFields struct {
	// Email contains the user's email address
	// For example, "accounts.google.com:example@gmail.com"
	Email string
	// ID contains a unique identifier for the user
	// For example, "accounts.google.com:userIDvalue"
	ID string
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

func RequireIAPAuthHandler(h http.Handler, audience string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt := r.Header.Get("x-goog-iap-jwt-assertion")
		if jwt == "" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, "must run under IAP\n")
			return
		}
		payload, err := validateIAPJWT(r.Context(), jwt, audience, idtoken.Validate)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			log.Printf("JWT validation error: %v", err)
			return
		}
		// TODO: idtoken.Payload doesn't include email for some reason, so we
		// get it from claims, which _should_ also contain it.
		ctx := context.WithValue(r.Context(), "subject", payload.Subject)
		ctx = context.WithValue(ctx, "email", payload.Claims["email"])
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// iapAuthFunc creates an authentication function used to create a GRPC interceptor.
// It ensures that the caller has successfully authenticated via IAP. If the caller
// has authenticated, the headers created by IAP will be added to the request scope
// context passed down to the server implementation.
// https://cloud.google.com/iap/docs/signed-headers-howto
func iapAuthFunc(audience string, validatorFn validator) grpcauth.AuthFunc {
	return func(ctx context.Context) (context.Context, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return ctx, status.Error(codes.Internal, codes.Internal.String())
		}
		jwtHeaders := md.Get(iapHeaderJWT)
		if len(jwtHeaders) == 0 {
			return ctx, status.Error(codes.Unauthenticated, "IAP JWT not found in request")
		}
		if _, err := validateIAPJWT(ctx, jwtHeaders[0], audience, validatorFn); err != nil {
			return nil, err
		}
		ctx, err := contextWithIAPMD(ctx, md)
		if err != nil {
			log.Printf("access: unable to set IAP fields in context: %s", err)
			return ctx, status.Error(codes.Unauthenticated, "unable to authenticate")
		}
		return ctx, nil
	}
}

func validateIAPJWT(ctx context.Context, jwt, audience string, validatorFn validator) (*idtoken.Payload, error) {
	payload, err := validatorFn(ctx, jwt, audience)
	if err != nil {
		log.Printf("access: error validating JWT: %s", err)
		return nil, status.Error(codes.Unauthenticated, "unable to authenticate")
	}
	if payload.Issuer != "https://cloud.google.com/iap" {
		log.Printf("access: incorrect issuer: %q", payload.Issuer)
		return nil, status.Error(codes.Unauthenticated, "incorrect issuer")
	}
	if payload.Expires+30 < time.Now().Unix() || payload.IssuedAt-30 > time.Now().Unix() {
		log.Printf("Bad JWT times: expires %v, issued %v", time.Unix(payload.Expires, 0), time.Unix(payload.IssuedAt, 0))
		return nil, status.Error(codes.Unauthenticated, "JWT timestamp invalid")
	}

	return payload, nil
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
	return ContextWithIAP(ctx, iap), nil
}

// ContextWithIAP adds the iap fields to the context.
func ContextWithIAP(ctx context.Context, iap IAPFields) context.Context {
	return context.WithValue(ctx, contextIAP, iap)
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
// The project number is the numerical GCP project number the service is deployed in.
// The service ID is the identifier for the backend service used to route IAP requests.
// https://cloud.google.com/iap/docs/signed-headers-howto
func IAPAudienceGCE(projectNumber int64, serviceID string) string {
	return fmt.Sprintf("/projects/%d/global/backendServices/%s", projectNumber, serviceID)
}

// IAPAudienceAppEngine returns the JWT audience for App Engine services.
// The project number is the numerical GCP project number the service is deployed in.
// The project ID is the textual identifier for the GCP project that the App Engine instance is deployed in.
// https://cloud.google.com/iap/docs/signed-headers-howto
func IAPAudienceAppEngine(projectNumber int64, projectID string) string {
	return fmt.Sprintf("/projects/%d/apps/%s", projectNumber, projectID)
}

// FakeContextWithOutgoingIAPAuth adds the iap fields to the metadata of an outgoing GRPC request and
// should only be used for testing.
func FakeContextWithOutgoingIAPAuth(ctx context.Context, iap IAPFields) context.Context {
	md := metadata.New(map[string]string{
		iapHeaderEmail: iap.Email,
		iapHeaderID:    iap.ID,
		iapHeaderJWT:   "test-jwt",
	})
	return metadata.NewOutgoingContext(ctx, md)
}

// FakeIAPAuthFunc provides a fake IAP authentication validation and should only be used for testing.
func FakeIAPAuthFunc() grpcauth.AuthFunc {
	return iapAuthFunc("TESTING", func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
		return &idtoken.Payload{
			Issuer:   "https://cloud.google.com/iap",
			Audience: audience,
			Expires:  time.Now().Add(time.Minute).Unix(),
			IssuedAt: time.Now().Add(-time.Minute).Unix(),
		}, nil
	})
}

// FakeIAPAuthInterceptorOptions provides the GRPC server options for fake IAP authentication
// and should only be used for testing.
func FakeIAPAuthInterceptorOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.UnaryInterceptor(grpcauth.UnaryServerInterceptor(FakeIAPAuthFunc())),
		grpc.StreamInterceptor(grpcauth.StreamServerInterceptor(FakeIAPAuthFunc())),
	}
}
