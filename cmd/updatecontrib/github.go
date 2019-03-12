// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GitHubInfo is a subset of the GH API info.
type GitHubInfo struct {
	ID    int
	Name  string
	Login string
}

// FetchGitHubInfo fetches information about the GitHub user associated
// with who. If no such user exists, it returns nil.
func FetchGitHubInfo(who *acLine) (*GitHubInfo, error) {
	id, err := fetchGitHubUserID(who)
	if err != nil {
		return nil, err
	}
	if id == "" {
		// There is no GitHub user associated with who.
		return nil, nil
	}

	cacheDir, err := githubCacheDir()
	if err != nil {
		return nil, err
	}
	cacheFile := filepath.Join(cacheDir, fmt.Sprintf("user-id-%s", id))
	if slurp, err := ioutil.ReadFile(cacheFile); err == nil {
		res := &GitHubInfo{}
		if err := json.Unmarshal(slurp, res); err != nil {
			return nil, fmt.Errorf("%s: %v", cacheFile, err)
		}
		return res, nil
	}

	jsonURL := fmt.Sprintf("https://api.github.com/user/%s", id) // undocumented but it works
	req, _ := http.NewRequest("GET", jsonURL, nil)
	if token, err := ioutil.ReadFile(githubTokenFile()); err == nil {
		req.Header.Set("Authorization", "token "+strings.TrimSpace(string(token)))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %v", jsonURL, res.Status)
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", jsonURL, err)
	}
	jres := &GitHubInfo{}
	if err := json.Unmarshal(body, jres); err != nil {
		return nil, fmt.Errorf("%s: %v", jsonURL, err)
	}
	if jres.ID == 0 {
		return nil, fmt.Errorf("%s: malformed response", jsonURL)
	}

	os.MkdirAll(cacheDir, 0700)
	ioutil.WriteFile(cacheFile, body, 0600)

	return jres, nil
}

// fetchGitHubUserID fetches the ID of the GitHub user associated
// with who. If no such user exists, it returns the empty string.
func fetchGitHubUserID(who *acLine) (string, error) {
	org, repo := githubOrgRepo(who.firstRepo)

	cacheDir, err := githubCacheDir()
	if err != nil {
		return "", err
	}
	cacheFile := filepath.Join(cacheDir, fmt.Sprintf("%s-%s-%s-id", org, repo, who.firstCommit))
	if slurp, err := ioutil.ReadFile(cacheFile); err == nil {
		return string(slurp), nil
	}

	jsonURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", org, repo, who.firstCommit)
	req, _ := http.NewRequest("GET", jsonURL, nil)
	if token, err := ioutil.ReadFile(githubTokenFile()); err == nil {
		req.Header.Set("Authorization", "token "+strings.TrimSpace(string(token)))
	}
	var jres struct {
		Author struct {
			ID int
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("%s: %v", jsonURL, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&jres); err != nil {
		return "", fmt.Errorf("%s: %v", jsonURL, err)
	}
	if jres.Author.ID == 0 {
		return "", nil // not a registered GitHub user
	}

	os.MkdirAll(cacheDir, 0700)
	ioutil.WriteFile(cacheFile, []byte(strconv.Itoa(jres.Author.ID)), 0600)

	return strconv.Itoa(jres.Author.ID), nil
}

func githubCacheDir() (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userCacheDir, "updatecontrib-github"), nil
}

func githubTokenFile() string {
	return filepath.Join(os.Getenv("HOME"), ".github-updatecontrib-token")
}
