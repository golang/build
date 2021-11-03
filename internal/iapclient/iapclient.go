// Package iapclient enables programmatic access to IAP-secured services. See
// https://cloud.google.com/iap/docs/authentication-howto.
//
// Login will be done as necessary using offline browser-based authentication,
// similarly to gcloud auth login. Credentials will be stored in the user's
// config directory.
package iapclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var gomoteConfig = &oauth2.Config{
	// Gomote client ID and secret.
	ClientID:     "872405196845-cc4c60gbf7mrmutpocsgl1asjb65du73.apps.googleusercontent.com",
	ClientSecret: "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",
	Endpoint:     google.Endpoint,
	RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
	Scopes:       []string{"openid email"},
}

func login(ctx context.Context) (*oauth2.Token, error) {
	const xsrfToken = "unused" // We don't actually get redirects, so we have no chance to check this.
	codeURL := gomoteConfig.AuthCodeURL(xsrfToken, oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser:\n\n\t%v\n\nEnter verification code: ", codeURL)
	var code string
	fmt.Scanln(&code)
	refresh, err := gomoteConfig.Exchange(ctx, code, oauth2.AccessTypeOffline)
	if err != nil {
		return nil, err
	}
	if err := writeToken(refresh); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save token, you will be asked to log in again: %v\n", err)
	}
	return refresh, nil
}

func writeToken(refresh *oauth2.Token) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	refreshBytes, err := json.Marshal(refresh)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, "gomote/iap-refresh-token"), refreshBytes, 0600)
}

func cachedToken() (*oauth2.Token, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	refreshBytes, err := os.ReadFile(filepath.Join(configDir, "gomote/iap-refresh-token"))
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
	return &refreshToken, nil
}

// TokenSource returns a TokenSource that can be used to access Go's
// IAP-protected sites. It will prompt for login if necessary.
func TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
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
	const audience = "872405196845-b6fu2qpi0fehdssmc8qo47h2u3cepi0e.apps.googleusercontent.com" // Go build IAP client ID.
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

type jwtTokenSource struct {
	conf     *oauth2.Config
	audience string
	refresh  *oauth2.Token
}

// Exchange a refresh token for a JWT that works with IAP. As of writing, there
// isn't anything to do this in the oauth2 library or google.golang.org/api/idtoken.
func (s *jwtTokenSource) Token() (*oauth2.Token, error) {
	v := url.Values{}
	v.Set("client_id", s.conf.ClientID)
	v.Set("client_secret", s.conf.ClientSecret)
	v.Set("refresh_token", s.refresh.RefreshToken)
	v.Set("grant_type", "refresh_token")
	v.Set("audience", s.audience)
	req, err := http.NewRequest("POST", s.conf.Endpoint.TokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IAP token exchange failed: status %v, body %q", resp.Status, body)
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
