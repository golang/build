// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectSSHTLS(t *testing.T) {
	testCases := []struct {
		desc         string
		authUser     string
		dialer       func(context.Context) (net.Conn, error)
		key          string
		keyPair      KeyPair
		password     string
		user         string
		wantAuthUser string
	}{
		{
			desc:         "tls-without-authuser",
			authUser:     "",
			key:          "key-foo",
			keyPair:      createKeyPair(t),
			password:     "foo",
			user:         "kate",
			wantAuthUser: "gomote",
		},
		{
			desc:         "tls-with-authuser",
			authUser:     "george",
			key:          "key-foo",
			keyPair:      createKeyPair(t),
			password:     "foo",
			user:         "kate",
			wantAuthUser: "george",
		},
		{
			desc:         "tls-with-configured-dialer",
			authUser:     "",
			dialer:       func(_ context.Context) (net.Conn, error) { return nil, errors.New("test error") },
			key:          "key-foo",
			keyPair:      createKeyPair(t),
			password:     "foo",
			user:         "kate",
			wantAuthUser: "gomote",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if gotUser := r.Header.Get("X-Go-Ssh-User"); gotUser != tc.user {
					t.Errorf("r.Header.Get(X-Go-Ssh-User) = %q; want %q", gotUser, tc.user)
				}
				if gotKey := r.Header.Get("X-Go-Authorized-Key"); gotKey != tc.key {
					t.Errorf("r.Header.Get(X-Go-Authorized-Key) = %q; want %q", gotKey, tc.key)
				}
				if gotAuthUser, gotAuthPass, gotOk := r.BasicAuth(); !gotOk || gotAuthUser != tc.wantAuthUser || gotAuthPass != tc.password {
					t.Errorf("Request.BasicAuth() = %q, %q, %t; want %q, %q, true", gotAuthUser, gotAuthPass, gotOk, tc.wantAuthUser, tc.password)
				}
				w.WriteHeader(http.StatusSwitchingProtocols)
			}))
			cert, err := tls.X509KeyPair([]byte(tc.keyPair.CertPEM), []byte(tc.keyPair.KeyPEM))
			if err != nil {
				t.Fatalf("tls.X509KeyPair([]byte(%q), []byte(%q)) = %v, %q; want no error", tc.keyPair.CertPEM, tc.keyPair.KeyPEM, cert, err)
			}
			ts.TLS = &tls.Config{
				Certificates: []tls.Certificate{cert},
			}
			ts.StartTLS()
			defer ts.Close()
			c := client{
				ipPort:   strings.TrimPrefix(ts.URL, "https://"),
				tls:      tc.keyPair,
				password: tc.password,
				authUser: tc.authUser,
				dialer:   tc.dialer,
			}
			gotConn, gotErr := c.ConnectSSH(tc.user, tc.key)
			if gotErr != nil {
				t.Fatalf("Client.ConnectSSH(%s, %s) = %v, %v; want no error", tc.user, tc.key, gotConn, gotErr)
			}
		})
	}
}

func TestConnectSSHNonTLS(t *testing.T) {
	testCases := []struct {
		desc      string
		authUser  string
		basicAuth bool
		dialer    func(context.Context) (net.Conn, error)
		key       string
		password  string
		user      string
		wantErr   bool
	}{
		{
			desc:      "non-tls-without-authuser",
			authUser:  "gomote",
			basicAuth: false,
			key:       "key-foo",
			password:  "foo",
			user:      "kate",
			wantErr:   false,
		},
		{
			desc:      "non-tls--with-authuser",
			authUser:  "gomote",
			basicAuth: true,
			key:       "key-foo",
			password:  "foo",
			user:      "kate",
			wantErr:   false,
		},
		{
			desc:      "non-tls-with-configured-dialer",
			authUser:  "gomote",
			basicAuth: true,
			dialer: func(context.Context) (net.Conn, error) {
				return nil, errors.New("test error")
			},
			key:      "key-foo",
			password: "foo",
			user:     "kate",
			wantErr:  true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if gotUser := r.Header.Get("X-Go-Ssh-User"); gotUser != tc.user {
					t.Errorf("r.Header.Get(X-Go-Ssh-User) = %q; want %q", gotUser, tc.user)
				}
				if gotKey := r.Header.Get("X-Go-Authorized-Key"); gotKey != tc.key {
					t.Errorf("r.Header.Get(X-Go-Authorized-Key) = %q; want %q", gotKey, tc.key)
				}
				if gotAuthUser, gotAuthPass, gotOk := r.BasicAuth(); gotOk || gotAuthUser != "" || gotAuthPass != "" {
					t.Errorf("Request.BasicAuth() = %q, %q, %t; want %q, %q, %t", gotAuthUser, gotAuthPass, gotOk, tc.user, tc.password, tc.basicAuth)
				}
				w.WriteHeader(http.StatusSwitchingProtocols)
			}))
			defer ts.Close()
			c := client{
				ipPort:   strings.TrimPrefix(ts.URL, "http://"),
				password: tc.password,
				authUser: tc.authUser,
				dialer:   tc.dialer,
			}
			gotConn, gotErr := c.ConnectSSH(tc.user, tc.key)
			if (gotErr != nil) != tc.wantErr {
				t.Fatalf("Client.ConnectSSH(%q, %q) = %v, %v; want net.Conn, error=%t", tc.user, tc.key, gotConn, gotErr, tc.wantErr)
			}
		})
	}
}

func createKeyPair(t *testing.T) KeyPair {
	kp, err := NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair() = %v, %s; want no error", kp, err)
	}
	return kp
}
