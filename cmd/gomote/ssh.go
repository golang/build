// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/build/internal/gomote/protos"
)

// sshServer is the ssh proxy through which ssh and scp
// reach buildlets.
const sshServer = "gomotessh.golang.org"

func ssh(args []string) error {
	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	fs.Usage = func() {
		usageLogger.Print("ssh usage: gomote ssh [-n] <instance> [cmd...]")
		fs.PrintDefaults()
		os.Exit(1)
	}
	printOnly := fs.Bool("n", false, "print the ssh command line but do not run it")
	fs.Parse(args)

	var name string
	var remoteCmd []string
	if fs.NArg() >= 1 {
		name = fs.Arg(0)
		remoteCmd = fs.Args()[1:]
	} else if activeGroup != nil {
		if len(activeGroup.Instances) != 1 {
			return fmt.Errorf("command only supports groups with exactly one member")
		}
		name = activeGroup.Instances[0]
	} else {
		fs.Usage()
	}

	priKey, certPath, err := sshCertificate(name)
	if err != nil {
		return err
	}
	return sshConnect(name, priKey, certPath, remoteCmd, *printOnly)
}

// scp copies files to or from buildlets.
// Arguments of the form instance:path name files on that instance;
// all other arguments, including scp flags, pass through to scp.
func scp(args []string) error {
	instances, rewritten := scpArgs(args)
	if len(instances) == 0 {
		usageLogger.Print("scp usage: gomote scp [scp-args] [<instance>:]file... [<instance>:]file")
		os.Exit(1)
	}
	scpPath, err := exec.LookPath("scp")
	if err != nil {
		return fmt.Errorf("path to scp not found: %w", err)
	}
	cli := []string{"-P", "2222"}
	var priKey string
	for _, inst := range instances {
		pk, certPath, err := sshCertificate(inst)
		if err != nil {
			return err
		}
		priKey = pk
		cli = append(cli, "-o", "CertificateFile="+certPath)
	}
	cli = append(cli, "-i", priKey)
	cli = append(cli, rewritten...)
	fmt.Printf("$ %s\n", shellJoin(append([]string{scpPath}, cli...)))
	cmd := exec.Command(scpPath, cli...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unable to scp: %w", err)
	}
	return nil
}

// scpArgs rewrites the arguments for an scp command, replacing
// arguments of the form instance:path with instance@sshServer:path
// and returning the instances mentioned along with the rewritten
// argument list.
func scpArgs(args []string) (instances, rewritten []string) {
	for _, arg := range args {
		inst, ok := scpInstance(arg)
		if !ok {
			rewritten = append(rewritten, arg)
			continue
		}
		if !slices.Contains(instances, inst) {
			instances = append(instances, inst)
		}
		rewritten = append(rewritten, inst+"@"+sshServer+strings.TrimPrefix(arg, inst))
	}
	return instances, rewritten
}

// scpInstance reports the instance name if arg has the remote form
// instance:path, using scp's own rule: an argument is remote if it
// contains a colon before any slash. Flags and arguments that
// already name a user with @ are left alone.
func scpInstance(arg string) (string, bool) {
	if strings.HasPrefix(arg, "-") {
		return "", false
	}
	i := strings.IndexAny(arg, ":/@")
	if i <= 0 || arg[i] != ':' {
		return "", false
	}
	return arg[:i], true
}

// sshCertificate signs the local SSH public key for the named
// instance, returning the paths of the local private key and the
// signed certificate.
func sshCertificate(name string) (priKey, certPath string, err error) {
	sshKeyDir, err := sshConfigDirectory()
	if err != nil {
		return "", "", err
	}
	pubKey, priKey, err := localKeyPair(sshKeyDir)
	if err != nil {
		return "", "", err
	}
	pubKeyBytes, err := os.ReadFile(pubKey)
	if err != nil {
		return "", "", err
	}
	ctx := context.Background()
	client := gomoteServerClient(ctx)
	resp, err := client.SignSSHKey(ctx, &protos.SignSSHKeyRequest{
		GomoteId:     name,
		PublicSshKey: []byte(pubKeyBytes),
	})
	if err != nil {
		return "", "", fmt.Errorf("unable to retrieve SSH certificate: %w", err)
	}
	certPath, err = writeCertificateToDisk(resp.GetSignedPublicSshKey())
	if err != nil {
		return "", "", err
	}
	return priKey, certPath, nil
}

func sshConfigDirectory() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("unable to retrieve user configuration directory: %w", err)
	}
	sshConfigDir := filepath.Join(configDir, "gomote", ".ssh")
	err = os.MkdirAll(sshConfigDir, 0700)
	if err != nil {
		return "", fmt.Errorf("unable to create user SSH configuration directory: %w", err)
	}
	return sshConfigDir, nil
}

func localKeyPair(sshDir string) (string, string, error) {
	priKey := filepath.Join(sshDir, "id_ed25519")
	pubKey := filepath.Join(sshDir, "id_ed25519.pub")
	if !fileExists(priKey) || !fileExists(pubKey) {
		log.Printf("local ssh keys do not exist, attempting to create them")
		if err := createLocalKeyPair(pubKey, priKey); err != nil {
			return "", "", fmt.Errorf("unable to create local SSH key pair: %w", err)
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
		return "", fmt.Errorf("unable to create temp directory for certficates: %w", err)
	}
	tf, err := os.CreateTemp(tmpDir, "id_ed25519-*-cert.pub")
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

func sshConnect(name string, priKey, certPath string, remoteCmd []string, printOnly bool) error {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("path to ssh not found: %w", err)
	}
	cli := []string{"-o", fmt.Sprintf("CertificateFile=%s", certPath), "-i", priKey, "-p", "2222", name + "@" + sshServer}
	cli = append(cli, remoteCmd...)
	if printOnly {
		fmt.Println(shellJoin(append([]string{ssh}, cli...)))
		return nil
	}
	fmt.Printf("$ %s\n", shellJoin(append([]string{ssh}, cli...)))
	cmd := exec.Command(ssh, cli...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unable to ssh into instance: %w", err)
	}
	return nil
}

// shellQuote quotes s as needed for use in a shell command line.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$&|;<>()*?[]#~`!{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellJoin joins args into a shell command line, quoting as needed.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func fileExists(path string) bool {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false
	}
	return true
}
