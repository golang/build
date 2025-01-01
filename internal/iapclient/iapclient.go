// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package iapclient enables programmatic access to IAP-secured services. See
// https://cloud.google.com/iap/docs/authentication-howto.
//
// Login will be done as necessary using offline browser-based authentication,
// similarly to gcloud auth login. Credentials will be stored in the user's
// config directory.
package iapclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"
)

var gomoteConfig = &oauth2.Config{
	// Gomote client ID and secret.
	ClientID:     "872405196845-odamr0j3kona7rp7fima6h4ummnd078t.apps.googleusercontent.com",
	ClientSecret: "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",
	Endpoint:     google.Endpoint,
	Scopes:       []string{"email openid profile"},
}

func login(ctx context.Context) (*oauth2.Token, error) {
	resp, err := http.PostForm("https://oauth2.googleapis.com/device/code", url.Values{
		"client_id": []string{gomoteConfig.ClientID},
		"scope":     gomoteConfig.Scopes,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status on device code request %v", resp.Status)
	}
	codeResp := &codeResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&codeResp); err != nil {
		return nil, err
	}
	fmt.Printf("Please visit %v in your browser and enter verification code:\n %v\n", codeResp.VerificationURL, codeResp.UserCode)

	tick := time.NewTicker(time.Duration(codeResp.Interval) * time.Second)
	defer tick.Stop()

	refresh := &oauth2.Token{}
outer:
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
			resp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
				"client_id":     []string{gomoteConfig.ClientID},
				"client_secret": []string{gomoteConfig.ClientSecret},
				"device_code":   []string{codeResp.DeviceCode},
				"grant_type":    []string{"urn:ietf:params:oauth:grant-type:device_code"},
			})
			if err != nil {
				return nil, err
			}
			if resp.StatusCode == http.StatusPreconditionRequired {
				continue
			}
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("unexpected status on token request %v", resp.Status)
			}
			if err := json.NewDecoder(resp.Body).Decode(refresh); err != nil {
				return nil, err
			}
			break outer
		}
	}

	if err := writeToken(refresh); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save token, you will be asked to log in again: %v\n", err)
	}
	return refresh, nil
}

// https://developers.google.com/identity/protocols/oauth2/limited-input-device#step-2:-handle-the-authorization-server-response
type codeResponse struct {
	DeviceCode      string `json:"device_code"`
	Interval        int    `json:"interval"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
}

const (
	configSubDir = "gomote"
	tokenFile    = "iap-refresh-tv-token"
)

func writeToken(refresh *oauth2.Token) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	refreshBytes, err := json.Marshal(refresh)
	if err != nil {
		return err
	}
	err = os.MkdirAll(filepath.Join(configDir, configSubDir), 0755)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, configSubDir, tokenFile), refreshBytes, 0600)
}

func removeToken() error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(configDir, configSubDir, tokenFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func cachedToken() (*oauth2.Token, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	refreshBytes, err := os.ReadFile(filepath.Join(configDir, configSubDir, tokenFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var refreshToken oauth2.Token
	if err := json.Unmarshal(refreshBytes, &refreshToken); err != nil {
		return nil, err
	}
	if !refreshToken.Valid() {
		return nil, nil
	}
	return &refreshToken, nil
}

// TokenSourceForceLogin returns a TokenSource that can be used to access Go's
// IAP-protected sites. It will delete any existing authentication token
// credentials and prompt for login.
func TokenSourceForceLogin(ctx context.Context) (oauth2.TokenSource, error) {
	if err := removeToken(); err != nil {
		return nil, fmt.Errorf("failed to delete existing token file: %s", err)
	}
	return TokenSource(ctx)
}

// TokenSource returns a TokenSource that can be used to access Go's
// IAP-protected sites. It will prompt for login if necessary.
func TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	const audience = "872405196845-b6fu2qpi0fehdssmc8qo47h2u3cepi0e.apps.googleusercontent.com" // Go build IAP client ID.

	if metadata.OnGCE() {
		if project, err := metadata.ProjectID(); err == nil && (project == "symbolic-datum-552" || project == "go-security-trybots") {
			return idtoken.NewTokenSource(ctx, audience)
		}
	}
	refresh, err := cachedToken()
	if err != nil {
		return nil, err
	}
	if refresh == nil {
		refresh, err = login(ctx)
		if err != nil {
			return nil, err
		}
	}
	tokenSource := oauth2.ReuseTokenSource(nil, &jwtTokenSource{gomoteConfig, audience, refresh})
	// Eagerly request a token to verify we're good. The source will cache it.
	if _, err := tokenSource.Token(); err != nil {
		return nil, err
	}
	return tokenSource, nil
}

// HTTPClient returns an http.Client that can be used to access Go's
// IAP-protected sites. It will prompt for login if necessary.
func HTTPClient(ctx context.Context) (*http.Client, error) {
	ts, err := TokenSource(ctx)
	if err != nil {
		return nil, err
	}
	return oauth2.NewClient(ctx, ts), nil
}

// GRPCClient returns a *gprc.ClientConn that can access Go's IAP-protected
// servers. It will prompt for login if necessary.
func GRPCClient(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	ts, err := TokenSource(ctx)
	if err != nil {
		return nil, err
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: strings.HasPrefix(addr, "localhost:")})),
		grpc.WithDefaultCallOptions(grpc.PerRPCCredentials(oauth.TokenSource{TokenSource: ts})),
		grpc.WithBlock(),
	}
	return grpc.DialContext(ctx, addr, opts...)
}

type jwtTokenSource struct {
	conf     *oauth2.Config
	audience string
	refresh  *oauth2.Token
}

// Token exchanges a refresh token for a JWT that works with IAP. As of writing, there
// isn't anything to do this in the oauth2 library or google.golang.org/api/idtoken.
func (s *jwtTokenSource) Token() (*oauth2.Token, error) {
	resp, err := http.PostForm(s.conf.Endpoint.TokenURL, url.Values{
		"client_id":     []string{s.conf.ClientID},
		"client_secret": []string{s.conf.ClientSecret},
		"refresh_token": []string{s.refresh.RefreshToken},
		"grant_type":    []string{"refresh_token"},
		"audience":      []string{s.audience},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		tokenRespErr := struct {
			Err         string `json:"error"`
			Description string `json:"error_description"`
		}{}
		err := fmt.Errorf("IAP token exchange failed: status %v, body %q", resp.Status, body)
		if unmarshErr := json.Unmarshal(body, &tokenRespErr); unmarshErr == nil && tokenRespErr.Err == "invalid_grant" {
			return nil, AuthenticationError{Err: err, Description: tokenRespErr.Description}
		}
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var token jwtTokenJSON
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, err
	}
	return &oauth2.Token{
		TokenType:   "Bearer",
		AccessToken: token.IDToken,
	}, nil
}

type jwtTokenJSON struct {
	IDToken string `json:"id_token"`
}

// AuthenticationError records an authentication error.
type AuthenticationError struct {
	Description string
	Err         error
}

func (ar AuthenticationError) Error() string { return ar.Description }
func (ar AuthenticationError) Unwrap() error { return ar.Err }
