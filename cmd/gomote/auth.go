// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/build/auth"
	"golang.org/x/build/buildlet"
	"golang.org/x/oauth2"
	"google.golang.org/api/compute/v1"
)

func username() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("USERNAME")
	}
	return os.Getenv("USER")
}

func homeDir() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
	}
	return os.Getenv("HOME")
}

func configDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "Gomote")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gomote")
	}
	return filepath.Join(homeDir(), ".config", "gomote")
}

func projTokenSource() oauth2.TokenSource {
	ts, err := auth.ProjectTokenSource(*proj, compute.ComputeScope)
	if err != nil {
		log.Fatalf("Failed to get OAuth2 token source for project %s: %v", *proj, err)
	}
	return ts
}

func userKeyPair() buildlet.KeyPair {
	keyDir := configDir()
	crtFile := filepath.Join(keyDir, "gomote.crt")
	keyFile := filepath.Join(keyDir, "gomote.key")
	_, crtErr := os.Stat(crtFile)
	_, keyErr := os.Stat(keyFile)
	if crtErr == nil && keyErr == nil {
		return buildlet.KeyPair{
			CertPEM: slurpString(crtFile),
			KeyPEM:  slurpString(keyFile),
		}
	}
	check := func(what string, err error) {
		if err != nil {
			log.Printf("%s: %v", what, err)
		}
	}
	check("making key dir", os.MkdirAll(keyDir, 0700))
	kp, err := buildlet.NewKeyPair()
	if err != nil {
		log.Fatalf("Error generating new key pair: %v", err)
	}
	check("writing cert file: ", ioutil.WriteFile(crtFile, []byte(kp.CertPEM), 0600))
	check("writing key file: ", ioutil.WriteFile(keyFile, []byte(kp.KeyPEM), 0600))
	return kp
}

func slurpString(f string) string {
	slurp, err := ioutil.ReadFile(f)
	if err != nil {
		log.Fatal(err)
	}
	return string(slurp)
}

func userToken() string {
	if *user == "" {
		panic("userToken called with user flag empty")
	}
	keyDir := configDir()
	baseFile := "user-" + *user + ".token"
	if *dev {
		baseFile = "dev-" + baseFile
	}
	tokenFile := filepath.Join(keyDir, baseFile)
	slurp, err := ioutil.ReadFile(tokenFile)
	if os.IsNotExist(err) {
		log.Printf("Missing file %s for user %q. Change --user or obtain a token and place it there.",
			tokenFile, *user)
	}
	if err != nil {
		log.Fatal(err)
	}
	return strings.TrimSpace(string(slurp))
}
