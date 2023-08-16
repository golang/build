// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// bootstapswarm will bootstrap the swarming bot depending
// on the environment that it is run on.
//
// On GCE: bootstrapswarm will retrieve authentication credentials
// from the GCE metadata service and use those credentials to download
// the swarming bot. It will then start the swarming bot in a directory
// within the user's home directory.
//
// Requirements:
// - Python3 installed and in the calling user's PATH.
//
// Not on GCE: bootstrapswarm will read the token file and retrieve the
// the luci machine token. It will use that token to authenticate and
// download the swarming bot. It will then start the swarming bot in a
// directory within the user's home directory.
//
// Requirements:
//   - Python3 installed and in the calling user's PATH.
//   - luci_machine_tokend running as root in a cron job.
//     https://chromium.googlesource.com/infra/luci/luci-go/+/refs/heads/main/tokenserver
//     Further instructions can be found at https://github.com/golang/go/wiki/DashboardBuilders
//     The default locations for the token files should be used if possible:
//     Most OS: /var/lib/luci_machine_tokend/token.json
//     Windows: C:\luci_machine_tokend\token.json
//   - bootstrapswarm should not be run as a privileged user.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"cloud.google.com/go/compute/metadata"
)

var (
	tokenFilePath = flag.String("token-file-path", defaultTokenLocation(), "Path to the token file (used when not on GCE)")
	hostname      = flag.String("hostname", os.Getenv("HOSTNAME"), "Hostname of machine to bootstrap (required)")
	swarming      = flag.String("swarming", "chromium-swarm.appspot.com", "Swarming server to connect to")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: bootstrapswarm")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *hostname == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx := context.Background()
	if err := bootstrap(ctx, *hostname, *tokenFilePath); err != nil {
		log.Fatal(err)
	}
}

var httpClient = http.DefaultClient

func bootstrap(ctx context.Context, hostname, tokenPath string) error {
	httpHeaders := map[string]string{"X-Luci-Swarming-Bot-ID": hostname}
	if metadata.OnGCE() {
		log.Println("Bootstrapping the swarming bot with GCE authentication")
		log.Println("retrieving the GCE VM token")
		token, err := retrieveGCEVMToken(ctx)
		if err != nil {
			return fmt.Errorf("unable to retrieve GCE Machine Token: %w", err)
		}
		httpHeaders["X-Luci-Gce-Vm-Token"] = token
	} else {
		log.Println("Bootstrapping the swarming bot with certificate authentication")
		log.Println("retrieving the luci-machine-token from the token file")
		tokBytes, err := os.ReadFile(tokenPath)
		if err != nil {
			return fmt.Errorf("unable to read file %q: %w", tokenPath, err)
		}
		type token struct {
			LuciMachineToken string `json:"luci_machine_token"`
		}
		var tok token
		if err := json.Unmarshal(tokBytes, &tok); err != nil {
			return fmt.Errorf("unable to unmarshal token %s: %w", tokenPath, err)
		}
		if tok.LuciMachineToken == "" {
			return fmt.Errorf("unable to retrieve machine token from token file %s", tokenPath)
		}
		httpHeaders["X-Luci-Machine-Token"] = tok.LuciMachineToken
	}
	log.Println("Downloading the swarming bot")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+*swarming+"/bot_code", nil)
	if err != nil {
		return fmt.Errorf("http.NewRequest: %w", err)
	}
	for k, v := range httpHeaders {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("client.Do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status code %d", resp.StatusCode)
	}
	botBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("io.ReadAll: %w", err)
	}
	botPath, err := writeToWorkDirectory(botBytes, "swarming_bot.zip")
	if err != nil {
		return fmt.Errorf("unable to save swarming bot to disk: %w", err)
	}
	log.Printf("Starting the swarming bot %s", botPath)
	cmd := exec.CommandContext(ctx, "python3", botPath, "start_bot")
	// swarming client checks the SWARMING_BOT_ID environment variable for hostname overrides.
	cmd.Env = append(os.Environ(), fmt.Sprintf("SWARMING_BOT_ID=%s", hostname))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command execution %s: %s", cmd, err)
	}
	return nil
}

// writeToWorkDirectory writes a file to the swarming working directory and returns the path
// to where the file was written.
func writeToWorkDirectory(b []byte, filename string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("os.UserHomeDir: %w", err)
	}
	workDir := filepath.Join(homeDir, ".swarming")
	if err := os.Mkdir(workDir, 0755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("os.Mkdir(%s): %w", workDir, err)
	}
	path := filepath.Join(workDir, filename)
	if err = os.WriteFile(path, b, 0644); err != nil {
		return "", fmt.Errorf("os.WriteFile(%s): %w", path, err)
	}
	return path, nil
}

// retrieveGCEVMToken retrieves a GCE VM token from the GCP metadata service.
func retrieveGCEVMToken(ctx context.Context) (string, error) {
	const url = `http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=https://chromium-swarm.appspot.com&format=full`
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("http.NewRequest: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("client.Do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status code %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("io.ReadAll: %w", err)
	}
	return string(b), nil
}

func defaultTokenLocation() string {
	out := "/var/lib/luci_machine_tokend/token.json"
	if runtime.GOOS == "windows" {
		return `C:\luci_machine_tokend\token.json`
	}
	return out
}
