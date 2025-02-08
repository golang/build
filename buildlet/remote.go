// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/build"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/types"
)

type UserPass struct {
	Username string // "user-$USER"
	Password string // buildlet key
}

// A CoordinatorClient makes calls to the build coordinator.
type CoordinatorClient struct {
	// Auth specifies how to authenticate to the coordinator.
	Auth UserPass

	// Instance optionally specifies the build coordinator to connect
	// to. If zero, the production coordinator is used.
	Instance build.CoordinatorInstance

	mu sync.Mutex
	hc *http.Client
}

func (cc *CoordinatorClient) instance() build.CoordinatorInstance {
	if cc.Instance == "" {
		return build.ProdCoordinator
	}
	return cc.Instance
}

func (cc *CoordinatorClient) client() (*http.Client, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.hc != nil {
		return cc.hc, nil
	}
	cc.hc = &http.Client{
		Transport: &http.Transport{
			Dial:    defaultDialer(),
			DialTLS: cc.instance().TLSDialer(),
		},
	}
	return cc.hc, nil
}

// CreateBuildlet creates a new buildlet of the given builder type on
// cc.
//
// This takes a builderType (instead of a hostType), but the
// returned buildlet can be used as any builder that has the same
// underlying buildlet type. For instance, a linux-amd64 buildlet can
// act as either linux-amd64 or linux-386-387.
//
// It may expire at any time.
// To release it, call Client.Close.
func (cc *CoordinatorClient) CreateBuildlet(builderType string) (RemoteClient, error) {
	return cc.CreateBuildletWithStatus(builderType, nil)
}

const (
	// GomoteCreateStreamVersion is the gomote protocol version at which JSON streamed responses started.
	GomoteCreateStreamVersion = "20191119"

	// GomoteCreateMinVersion is the oldest "gomote create" protocol version that's still supported.
	GomoteCreateMinVersion = "20160922"
)

// CreateBuildletWithStatus is like CreateBuildlet but accepts an optional status callback.
func (cc *CoordinatorClient) CreateBuildletWithStatus(builderType string, status func(types.BuildletWaitStatus)) (RemoteClient, error) {
	hc, err := cc.client()
	if err != nil {
		return nil, err
	}
	ipPort, _ := cc.instance().TLSHostPort() // must succeed if client did
	form := url.Values{
		"version":     {GomoteCreateStreamVersion}, // checked by cmd/coordinator/remote.go
		"builderType": {builderType},
	}
	req, _ := http.NewRequest("POST",
		"https://"+ipPort+"/buildlet/create",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(cc.Auth.Username, cc.Auth.Password)
	// TODO: accept a context for deadline/cancelation
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		slurp, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("%s: %s", res.Status, slurp)
	}

	// TODO: delete this once the server's been deployed with it.
	// This code only exists for compatibility for a day or two at most.
	if res.Header.Get("X-Supported-Version") < GomoteCreateStreamVersion {
		var rb RemoteBuildlet
		if err := json.NewDecoder(res.Body).Decode(&rb); err != nil {
			return nil, err
		}
		return cc.NamedBuildlet(rb.Name)
	}

	type msg struct {
		Error    string                    `json:"error"`
		Buildlet *RemoteBuildlet           `json:"buildlet"`
		Status   *types.BuildletWaitStatus `json:"status"`
	}
	bs := bufio.NewScanner(res.Body)
	for bs.Scan() {
		line := bs.Bytes()
		var m msg
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, err
		}
		if m.Error != "" {
			return nil, errors.New(m.Error)
		}
		if m.Buildlet != nil {
			if m.Buildlet.Name == "" {
				return nil, fmt.Errorf("buildlet: coordinator's /buildlet/create returned an unnamed buildlet")
			}
			return cc.NamedBuildlet(m.Buildlet.Name)
		}
		if m.Status != nil {
			if status != nil {
				status(*m.Status)
			}
			continue
		}
		log.Printf("buildlet: unknown message type from coordinator's /buildlet/create endpoint: %q", line)
		continue
	}
	err = bs.Err()
	if err == nil {
		err = errors.New("buildlet: coordinator's /buildlet/create ended its response stream without a terminal message")
	}
	return nil, err
}

type RemoteBuildlet struct {
	HostType    string // "host-linux-bullseye"
	BuilderType string // "linux-386-387"
	Name        string // "buildlet-adg-openbsd-386-2"
	Created     time.Time
	Expires     time.Time
}

func (cc *CoordinatorClient) RemoteBuildlets() ([]RemoteBuildlet, error) {
	hc, err := cc.client()
	if err != nil {
		return nil, err
	}
	ipPort, _ := cc.instance().TLSHostPort() // must succeed if client did
	req, _ := http.NewRequest("GET", "https://"+ipPort+"/buildlet/list", nil)
	req.SetBasicAuth(cc.Auth.Username, cc.Auth.Password)
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		slurp, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("%s: %s", res.Status, slurp)
	}
	var ret []RemoteBuildlet
	if err := json.NewDecoder(res.Body).Decode(&ret); err != nil {
		return nil, err
	}
	return ret, nil
}

// NamedBuildlet returns a buildlet client for the named remote buildlet.
// Names are not validated. Use Client.Status to check whether the client works.
func (cc *CoordinatorClient) NamedBuildlet(name string) (RemoteClient, error) {
	hc, err := cc.client()
	if err != nil {
		return nil, err
	}
	ipPort, _ := cc.instance().TLSHostPort() // must succeed if client did
	c := &client{
		baseURL:        "https://" + ipPort,
		remoteBuildlet: name,
		httpClient:     hc,
		authUser:       cc.Auth.Username,
		password:       cc.Auth.Password,
	}
	c.setCommon()
	return c, nil
}

var (
	flagsRegistered bool
	gomoteUserFlag  string
)

// RegisterFlags registers "user" and "staging" flags that control the
// behavior of NewCoordinatorClientFromFlags. These are used by remote
// client commands like gomote.
func RegisterFlags() {
	if !flagsRegistered {
		buildenv.RegisterFlags()
		flag.StringVar(&gomoteUserFlag, "user", username(), "gomote server username")
		flagsRegistered = true
	}
}

// username finds the user's username in the environment.
func username() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("USERNAME")
	}
	return os.Getenv("USER")
}

// configDir finds the OS-dependent config dir.
func configDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "Gomote")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gomote")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "gomote")
}

// userToken reads the gomote token from the user's home directory.
func userToken() (string, error) {
	if gomoteUserFlag == "" {
		panic("userToken called with user flag empty")
	}
	keyDir := configDir()
	userPath := filepath.Join(keyDir, "user-"+gomoteUserFlag+".user")
	b, err := os.ReadFile(userPath)
	if err == nil {
		gomoteUserFlag = string(bytes.TrimSpace(b))
	}
	baseFile := "user-" + gomoteUserFlag + ".token"
	if buildenv.FromFlags() == buildenv.Staging {
		baseFile = "staging-" + baseFile
	}
	tokenFile := filepath.Join(keyDir, baseFile)
	slurp, err := os.ReadFile(tokenFile)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("Missing file %s for user %q. Change --user or obtain a token and place it there.",
			tokenFile, gomoteUserFlag)
	}
	return strings.TrimSpace(string(slurp)), err
}

// NewCoordinatorClientFromFlags constructs a CoordinatorClient for the current user.
func NewCoordinatorClientFromFlags() (*CoordinatorClient, error) {
	if !flagsRegistered {
		return nil, errors.New("RegisterFlags not called")
	}
	inst := build.ProdCoordinator
	env := buildenv.FromFlags()
	if env == buildenv.Staging {
		inst = build.StagingCoordinator
	} else if env == buildenv.Development {
		inst = "localhost:8119"
	}

	if gomoteUserFlag == "" {
		return nil, errors.New("user flag must be specified")
	}
	tok, err := userToken()
	if err != nil {
		return nil, err
	}
	return &CoordinatorClient{
		Auth: UserPass{
			Username: "user-" + gomoteUserFlag,
			Password: tok,
		},
		Instance: inst,
	}, nil
}
