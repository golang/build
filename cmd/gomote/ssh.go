// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/gophers"
)

func legacySSH(args []string) error {
	if activeGroup != nil {
		return fmt.Errorf("command does not support groups")
	}

	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "ssh usage: gomote ssh <instance>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var mutable bool
	fs.BoolVar(&mutable, "i-will-not-break-the-host", false, "required for older host configs with reused filesystems; using this says that you are aware that your changes to the machine's root filesystem affect future builds. This is a no-op for the newer, safe host configs.")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
	}
	name := fs.Arg(0)
	_, err := remoteClient(name)
	if err != nil {
		return err
	}
	// gomoteUser extracts "gopher" from "user-gopher-linux-amd64-0".
	gomoteUser := strings.Split(name, "-")[1]
	githubUser := gophers.GitHubOfGomoteUser(gomoteUser)

	sshUser := name
	if mutable {
		sshUser = "mutable-" + sshUser
	}

	ssh, err := exec.LookPath("ssh")
	if err != nil {
		log.Printf("No 'ssh' binary found in path so can't run:")
	}
	fmt.Printf("$ ssh -p 2222 %s@farmer.golang.org # auth using https://github.com/%s.keys\n", sshUser, githubUser)

	// Best effort, where supported:
	syscall.Exec(ssh, []string{"ssh", "-p", "2222", sshUser + "@farmer.golang.org"}, os.Environ())
	return nil
}

func ssh(args []string) error {
	if activeGroup != nil {
		return fmt.Errorf("command does not support groups")
	}

	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "ssh usage: gomote ssh <instance>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
	}

	name := fs.Arg(0)
	sshKeyDir, err := sshConfigDirectory()
	if err != nil {
		return err
	}
	pubKey, priKey, err := localKeyPair(sshKeyDir)
	if err != nil {
		return err
	}
	pubKeyBytes, err := os.ReadFile(pubKey)
	if err != nil {
		return err
	}
	ctx := context.Background()
	client := gomoteServerClient(ctx)
	resp, err := client.SignSSHKey(ctx, &protos.SignSSHKeyRequest{
		GomoteId:     name,
		PublicSshKey: []byte(pubKeyBytes),
	})
	if err != nil {
		return fmt.Errorf("unable to retrieve SSH certificate: %s", statusFromError(err))
	}
	certPath, err := writeCertificateToDisk(resp.GetSignedPublicSshKey())
	if err != nil {
		return err
	}
	return sshConnect(name, priKey, certPath)
}

func sshConfigDirectory() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("unable to retrieve user configuration directory: %s", err)
	}
	sshConfigDir := filepath.Join(configDir, "gomote", ".ssh")
	err = os.MkdirAll(sshConfigDir, 0700)
	if err != nil {
		return "", fmt.Errorf("unable to create user SSH configuration directory: %s", err)
	}
	return sshConfigDir, nil
}

func localKeyPair(sshDir string) (string, string, error) {
	priKey := filepath.Join(sshDir, "id_ed25519")
	pubKey := filepath.Join(sshDir, "id_ed25519.pub")
	if !fileExists(priKey) || !fileExists(pubKey) {
		log.Printf("local ssh keys do not exist, attempting to create them")
		if err := createLocalKeyPair(pubKey, priKey); err != nil {
			return "", "", fmt.Errorf("unable to create local SSH key pair: %s", err)
		}
	}
	return pubKey, priKey, nil
}

func createLocalKeyPair(pubKey, priKey string) error {
	cmd := exec.Command("ssh-keygen", "-o", "-a", "256", "-t", "ed25519", "-f", priKey)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func writeCertificateToDisk(b []byte) (string, error) {
	tmpDir := filepath.Join(os.TempDir(), ".gomote")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return "", fmt.Errorf("unable to create temp directory for certficates: %s", err)
	}
	tf, err := ioutil.TempFile(tmpDir, "id_ed25519-*-cert.pub")
	if err != nil {
		return "", err
	}
	if err := tf.Chmod(0600); err != nil {
		return "", err
	}
	if _, err := tf.Write(b); err != nil {
		return "", err
	}
	return tf.Name(), tf.Close()
}

func sshConnect(name string, priKey, certPath string) error {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("path to ssh not found: %s", err)
	}
	cli := []string{"-o", fmt.Sprintf("CertificateFile=%s", certPath), "-i", priKey, "-p", "2222", name + "@farmer.golang.org"}
	fmt.Printf("$ %s %s\n", ssh, strings.Join(cli, " "))
	cmd := exec.Command(ssh, cli...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unable to ssh into instance: %s", err)
	}
	return nil
}

func fileExists(path string) bool {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false
	}
	return true
}
