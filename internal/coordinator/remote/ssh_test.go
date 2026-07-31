// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/buildlet"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/nettest"
)

func TestSignPublicSSHKey(t *testing.T) {
	signer, err := ssh.ParsePrivateKey([]byte(devCertCAPrivate))
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey() = %s", err)
	}
	ownerID := "accounts.google.com:userIDvalue"
	sessionID := "user-maria-linux-amd64-12"
	gotPubKey, err := SignPublicSSHKey(context.Background(), signer, []byte(devCertClientPublic), sessionID, ownerID, time.Minute)
	if err != nil {
		t.Fatalf("SignPublicSSHKey(...) = _, %s; want no error", err)
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(gotPubKey)
	if err != nil {
		t.Fatalf("ssh.ParseAuthorizedKey(...) = %s; want no error", err)
	}
	certChecker := &ssh.CertChecker{}
	wantPrinciple := fmt.Sprintf("%s@farmer.golang.org", sessionID)
	pubKeyCert := pubKey.(*ssh.Certificate)
	if err := certChecker.CheckCert(wantPrinciple, pubKeyCert); err != nil {
		t.Fatalf("certChecker.CheckCert(%s, %+v) = %s", wantPrinciple, pubKeyCert, err)
	}
	if diff := cmp.Diff(pubKeyCert.SignatureKey.Marshal(), signer.PublicKey().Marshal()); diff != "" {
		t.Fatalf("Public Keys mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleCertificateAuthFunc(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr, sp, s := setupSSHServer(t, ctx)
	defer s.Close()

	ownerID := "accounts.google.com:userIDvalue"
	sessionID := sp.AddSession(ownerID, "maria", "linux-amd64", "xyz", "", &buildlet.FakeClient{})
	certSigner := parsePrivateKey(t, []byte(devCertCAPrivate))
	clientPubKey, err := SignPublicSSHKey(ctx, certSigner, []byte(devCertClientPublic), sessionID, ownerID, time.Minute)
	if err != nil {
		t.Fatalf("SignPublicSSHKey(...) = _, %s; want no error", err)
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(clientPubKey)
	if err != nil {
		t.Fatalf("ParsePublicKey(...) = _, %s; want no error", err)
	}
	cert := pubKey.(*ssh.Certificate)
	clientCertSigner := parsePrivateKey(t, []byte(devCertClientPrivate))
	clientSigner, err := ssh.NewCertSigner(cert, clientCertSigner)
	if err != nil {
		t.Fatalf("NewCertSigner(...) = _, %s; want no error", err)
	}
	clientConfig := &ssh.ClientConfig{
		User: sessionID,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, clientConfig)
	if err != nil {
		t.Fatalf("Dial(...) = _, %s; want no error", err)
	}
	client.Close()
}

func TestHandleCertificateAuthFuncErrors(t *testing.T) {
	t.Run("no certificate", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		addr, sp, s := setupSSHServer(t, ctx)
		defer s.Close()

		ownerID := "accounts.google.com:userIDvalue"
		sessionID := sp.AddSession(ownerID, "maria", "linux-amd64", "xyz", "", &buildlet.FakeClient{})
		clientSigner := parsePrivateKey(t, []byte(devCertClientPrivate))
		clientConfig := &ssh.ClientConfig{
			User: sessionID,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(clientSigner),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}
		_, err := ssh.Dial("tcp", addr, clientConfig)
		if err == nil {
			t.Fatal("Dial(...) = client, nil; want error")
		}
	})

	t.Run("wrong certificate signer", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		addr, sp, s := setupSSHServer(t, ctx)
		defer s.Close()

		ownerID := "accounts.google.com:userIDvalue"
		sessionID := sp.AddSession(ownerID, "maria", "linux-amd64", "xyz", "", &buildlet.FakeClient{})
		certSigner := parsePrivateKey(t, []byte(devCertAlternateClientPrivate))
		clientPubKey, err := SignPublicSSHKey(ctx, certSigner, []byte(devCertClientPublic), sessionID, ownerID, time.Minute)
		if err != nil {
			t.Fatalf("SignPublicSSHKey(...) = _, %s; want no error", err)
		}
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey(clientPubKey)
		if err != nil {
			t.Fatalf("ParsePublicKey(...) = _, %s; want no error", err)
		}
		cert := pubKey.(*ssh.Certificate)
		clientCertSigner := parsePrivateKey(t, []byte(devCertClientPrivate))
		clientSigner, err := ssh.NewCertSigner(cert, clientCertSigner)
		if err != nil {
			t.Fatalf("NewCertSigner(...) = _, %s; want no error", err)
		}
		clientConfig := &ssh.ClientConfig{
			User: sessionID,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(clientSigner),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}
		_, err = ssh.Dial("tcp", addr, clientConfig)
		if err == nil {
			t.Fatalf("Dial(...) = _, %s; want no error", err)
		}
	})

	t.Run("wrong user", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		addr, sp, s := setupSSHServer(t, ctx)
		defer s.Close()

		ownerID := "accounts.google.com:userIDvalue"
		sessionID := sp.AddSession(ownerID, "maria", "linux-amd64", "xyz", "", &buildlet.FakeClient{})
		certSigner := parsePrivateKey(t, []byte(devCertCAPrivate))
		clientPubKey, err := SignPublicSSHKey(ctx, certSigner, []byte(devCertClientPublic), sessionID, ownerID, time.Minute)
		if err != nil {
			t.Fatalf("SignPublicSSHKey(...) = _, %s; want no error", err)
		}
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey(clientPubKey)
		if err != nil {
			t.Fatalf("ParsePublicKey(...) = _, %s; want no error", err)
		}
		cert := pubKey.(*ssh.Certificate)
		clientCertSigner := parsePrivateKey(t, []byte(devCertClientPrivate))
		clientSigner, err := ssh.NewCertSigner(cert, clientCertSigner)
		if err != nil {
			t.Fatalf("NewCertSigner(...) = _, %s; want no error", err)
		}
		clientConfig := &ssh.ClientConfig{
			User: sessionID + "_i_do_not_exist",
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(clientSigner),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}
		_, err = ssh.Dial("tcp", addr, clientConfig)
		if err == nil {
			t.Fatal("Dial(...) = _, nil; want error")
		}
	})

	t.Run("wrong principle", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		addr, sp, s := setupSSHServer(t, ctx)
		defer s.Close()

		ownerID := "accounts.google.com:userIDvalue"
		sessionID := sp.AddSession(ownerID, "maria", "linux-amd64", "xyz", "", &buildlet.FakeClient{})
		certSigner := parsePrivateKey(t, []byte(devCertCAPrivate))
		clientPubKey, err := SignPublicSSHKey(ctx, certSigner, []byte(devCertClientPublic), sessionID+"WRONG", ownerID, time.Minute)
		if err != nil {
			t.Fatalf("SignPublicSSHKey(...) = _, %s; want no error", err)
		}
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey(clientPubKey)
		if err != nil {
			t.Fatalf("ParsePublicKey(...) = _, %s; want no error", err)
		}
		cert := pubKey.(*ssh.Certificate)
		clientCertSigner := parsePrivateKey(t, []byte(devCertClientPrivate))
		clientSigner, err := ssh.NewCertSigner(cert, clientCertSigner)
		if err != nil {
			t.Fatalf("NewCertSigner(...) = _, %s; want no error", err)
		}
		clientConfig := &ssh.ClientConfig{
			User: sessionID,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(clientSigner),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}
		_, err = ssh.Dial("tcp", addr, clientConfig)
		if err == nil {
			t.Fatal("Dial(...) = _, nil; want error")
		}
	})

	t.Run("wrong owner", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		addr, sp, s := setupSSHServer(t, ctx)
		defer s.Close()

		ownerID := "accounts.google.com:userIDvalue"
		sessionID := sp.AddSession(ownerID, "maria", "linux-amd64", "xyz", "", &buildlet.FakeClient{})
		certSigner := parsePrivateKey(t, []byte(devCertCAPrivate))
		clientPubKey, err := SignPublicSSHKey(ctx, certSigner, []byte(devCertClientPublic), sessionID, ownerID+"WRONG", time.Minute)
		if err != nil {
			t.Fatalf("SignPublicSSHKey(...) = _, %s; want no error", err)
		}
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey(clientPubKey)
		if err != nil {
			t.Fatalf("ParsePublicKey(...) = _, %s; want no error", err)
		}
		cert := pubKey.(*ssh.Certificate)
		clientCertSigner := parsePrivateKey(t, []byte(devCertClientPrivate))
		clientSigner, err := ssh.NewCertSigner(cert, clientCertSigner)
		if err != nil {
			t.Fatalf("NewCertSigner(...) = _, %s; want no error", err)
		}
		clientConfig := &ssh.ClientConfig{
			User: sessionID,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(clientSigner),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}
		_, err = ssh.Dial("tcp", addr, clientConfig)
		if err == nil {
			t.Fatal("Dial(...) = _, nil; want error")
		}
	})
}

func setupSSHServer(t *testing.T, ctx context.Context) (addr string, sp *SessionPool, s *SSHServer) {
	sp = NewSessionPool(ctx)
	l, err := nettest.NewLocalListener("tcp")
	if err != nil {
		t.Fatalf("nettest.NewLocalListener(tcp) = _, %s; want no error", err)
	}
	addr = l.Addr().String()
	s, err = NewSSHServer(addr, []byte(devCertAlternateClientPrivate), []byte(devCertCAPublic), []byte(devCertCAPrivate), sp)
	if err != nil {
		t.Fatalf("NewSSHServer(...) = %s; want no error", err)
	}
	go s.serve(l)
	if err != nil {
		t.Fatalf("server.serve(l) = %s; want no error", err)
	}
	return
}

func parsePrivateKey(t *testing.T, pemEncoded []byte) ssh.Signer {
	cert, err := ssh.ParsePrivateKey(pemEncoded)
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey() = _, %s; want no error", err)
	}
	return cert
}

const (
	// devCertCAPrivate is a private SSH CA certificate to be used for development.
	devCertCAPrivate = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACCVd2FJ3Db/oV53iRDt1RLscTn41hYXbunuCWIlXze2WAAAAJhjy3ePY8t3
jwAAAAtzc2gtZWQyNTUxOQAAACCVd2FJ3Db/oV53iRDt1RLscTn41hYXbunuCWIlXze2WA
AAAEALuUJMb/rEaFNa+vn5RejeoBiiViyda7djgEvMnQ8fRJV3YUncNv+hXneJEO3VEuxx
OfjWFhdu6e4JYiVfN7ZYAAAAE3Rlc3R1c2VyQGdvbGFuZy5vcmcBAg==
-----END OPENSSH PRIVATE KEY-----`

	// devCertCAPublic is a public SSH CA certificate to be used for development.
	devCertCAPublic = `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJV3YUncNv+hXneJEO3VEuxxOfjWFhdu6e4JYiVfN7ZY testuser@golang.org`

	// devCertClientPrivate is a private SSH certificate to be used for development.
	devCertClientPrivate = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBxCM6ADdHnjTIHG/IpMa3z32CLwtu3BDUR3k2NNbI3owAAAKDFZ7xtxWe8
bQAAAAtzc2gtZWQyNTUxOQAAACBxCM6ADdHnjTIHG/IpMa3z32CLwtu3BDUR3k2NNbI3ow
AAAECidrOyYbTlYxyBSPP7W/UHk3Si2dgWSfkT+eEIETcvqHEIzoAN0eeNMgcb8ikxrfPf
YIvC27cENRHeTY01sjejAAAAFnRlc3RfY2xpZW50QGdvbGFuZy5vcmcBAgMEBQYH
-----END OPENSSH PRIVATE KEY-----`

	// devCertClientPublic is a public SSH certificate to be used for development.
	devCertClientPublic = `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHEIzoAN0eeNMgcb8ikxrfPfYIvC27cENRHeTY01sjej test_client@golang.org`

	// devCertAlternateClientPrivate is a private SSH certificate to be used for development.
	devCertAlternateClientPrivate = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACDOj8K2lbCSv+LojNcrUf0XH1vqknuEZBkAceiBHuNuEQAAAKDYNRtZ2DUb
WQAAAAtzc2gtZWQyNTUxOQAAACDOj8K2lbCSv+LojNcrUf0XH1vqknuEZBkAceiBHuNuEQ
AAAEDS4G3tQt5S4v7CD+DVyT/mwOKgIScIgFOpFt/EsCXL9M6PwraVsJK/4uiM1ytR/Rcf
W+qSe4RkGQBx6IEe424RAAAAF3Rlc3RfZGlzY2FyZEBnb2xhbmcub3JnAQIDBAUG
-----END OPENSSH PRIVATE KEY-----`

	// devCertAlternateClientPublic is a public SSH to be used for development.
	devCertAlternateClientPublic = `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIM6PwraVsJK/4uiM1ytR/RcfW+qSe4RkGQBx6IEe424R test_discard@golang.org`
)

func TestBuildletSSHArgs(t *testing.T) {
	common := []string{"-p", "1234", "-o", "UserKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=no", "-o", "LogLevel=ERROR", "-i", "/key"}
	tests := []struct {
		subsystem string
		rawCmd    string
		want      []string
	}{
		{"", "", append(common, "swarming@localhost")},
		{"", "hostname -f", append(common, "swarming@localhost", "hostname -f")},
		{"sftp", "", append(common, "-s", "swarming@localhost", "sftp")},
	}
	for _, tt := range tests {
		got := buildletSSHArgs(1234, "/key", "swarming", tt.subsystem, tt.rawCmd)
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("buildletSSHArgs(subsystem=%q, cmd=%q) mismatch (-want +got):\n%s", tt.subsystem, tt.rawCmd, diff)
		}
	}
}

// TestSSHHandleDirect exercises the direct (no pty) proxy path end to
// end: a real ssh or scp client connects to a real SSHServer, which
// authenticates the client's certificate and proxies the session to a
// fake buildlet sshd (an in-process x/crypto/ssh server standing in
// for the sshd on a swarming bot).
func TestSSHHandleDirect(t *testing.T) {
	// The proxy runs on Linux, and this test needs an ssh client that
	// can connect to a local server and run commands through it. The
	// macOS builders cannot even exec /usr/bin/scp, so limit the test
	// to the system the proxy is deployed on.
	if runtime.GOOS != "linux" {
		t.Skipf("skipping on %s: the ssh proxy runs on linux", runtime.GOOS)
	}
	requireCommand(t, "ssh", "-V")

	// The gomote key pair: the proxy's host key, its identity when
	// connecting to the buildlet sshd, and the key that sshd authorizes.
	gomotePriv, gomotePub, err := SSHKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	authorizedGomoteKey, _, _, _, err := ssh.ParseAuthorizedKey(gomotePub)
	if err != nil {
		t.Fatal(err)
	}
	caPriv, _, err := SSHKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := ssh.ParsePrivateKey(caPriv)
	if err != nil {
		t.Fatal(err)
	}
	buildletHostPriv, _, err := SSHKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	buildletSigner, err := ssh.ParsePrivateKey(buildletHostPriv)
	if err != nil {
		t.Fatal(err)
	}

	// A session pool holding one fake buildlet.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp := NewSessionPool(ctx)
	defer sp.Close()
	fc := &directFakeClient{
		t:          t,
		hostSigner: buildletSigner,
		authorized: authorizedGomoteKey,
	}
	const ownerID = "accounts.google.com:tester"
	sess := sp.AddSession(ownerID, "user", "gotip-linux-amd64", "host-linux-amd64", "task123", fc)

	// The proxy under test.
	ss, err := NewSSHServer("localhost:0", gomotePriv, gomotePub, caPriv, sp, EnableLUCIOption())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	go ss.serve(ln)
	defer ss.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	// A client key, certified by the CA for this session and owner,
	// as the gomote command does via the SignSSHKey RPC.
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := SignPublicSSHKey(ctx, caSigner, ssh.MarshalAuthorizedKey(sshPub), sess, ownerID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(dir, "id_ed25519-cert.pub")
	if err := os.WriteFile(certFile, cert, 0o600); err != nil {
		t.Fatal(err)
	}

	opts := []string{
		"-o", "CertificateFile=" + certFile,
		"-i", keyFile,
		"-o", "IdentitiesOnly=yes",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
	}
	target := sess + "@localhost"

	// The property the mote protocol needs: every byte passes through
	// in both directions unmodified, including the ones a cooked pty
	// would eat (\x00, \x03, \x13, \x7f, \r).
	const binary = "mote server hello \x00\x01\xfe\xff\n\x03\x04\x11\x13\x7f\r\nrest"

	tests := []struct {
		name   string
		stdin  string
		cmd    []string // remote command; none means ssh with no arguments, a shell reading stdin
		stdout string
		stderr string
		code   int
	}{
		{name: "exec", cmd: []string{"echo hello direct"}, stdout: "hello direct\n"},
		{name: "noArgs", stdin: "echo from-shell\nexit 0\n", stdout: "from-shell\n"},
		{name: "exitStatus", cmd: []string{"exit 7"}, code: 7},
		{name: "stderr", cmd: []string{"echo out; echo err >&2"}, stdout: "out\n", stderr: "err\n"},
		{name: "binarySafe", stdin: binary, cmd: []string{"cat"}, stdout: binary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string{"-p", port}, opts...), target)
			args = append(args, tt.cmd...)
			cmd := exec.Command("ssh", args...)
			var outb, errb bytes.Buffer
			cmd.Stdin = strings.NewReader(tt.stdin)
			cmd.Stdout = &outb
			cmd.Stderr = &errb
			err := cmd.Run()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("ssh: %v", err)
			}
			if code != tt.code || outb.String() != tt.stdout || errb.String() != tt.stderr {
				t.Errorf("code=%d stdout=%q stderr=%q, want %d, %q, %q",
					code, outb.String(), errb.String(), tt.code, tt.stdout, tt.stderr)
			}
		})
	}

	t.Run("scp", func(t *testing.T) {
		if fc.sftpServer() == "" {
			t.Skip("no sftp-server binary found")
		}
		requireCommand(t, "scp")
		src := filepath.Join(dir, "src.bin")
		data := []byte("scp test \x00\x01\xfe\xff\r\n data")
		if err := os.WriteFile(src, data, 0o644); err != nil {
			t.Fatal(err)
		}
		remote := filepath.Join(dir, "remote.bin") // "remote" is the local fs: sftp-server runs in-process
		back := filepath.Join(dir, "back.bin")
		for _, files := range [][]string{
			{src, target + ":" + remote},
			{target + ":" + remote, back},
		} {
			args := append(append([]string{"-P", port}, opts...), files...)
			out, err := exec.Command("scp", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("scp %v: %v\n%s", files, err, out)
			}
		}
		got, err := os.ReadFile(back)
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("round trip = %q, %v; want %q", got, err, data)
		}
	})
}

// requireCommand skips the test unless the named command is installed
// and can be run. Running it is the part worth checking: some builders
// have an ssh and an scp in PATH that fail to exec.
func requireCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("no %s command: %v", name, err)
	}
	// The command may report usage and exit with a status, which is
	// fine; only a failure to run it at all disqualifies the system.
	var execErr *exec.Error
	if err := exec.Command(path, args...).Run(); errors.As(err, &execErr) {
		t.Skipf("cannot run %s: %v", path, err)
	}
}

// A directFakeClient is a buildlet client whose ConnectSSH returns a
// connection to an in-process fake buildlet sshd.
type directFakeClient struct {
	buildlet.FakeClient
	t          *testing.T
	hostSigner ssh.Signer
	authorized ssh.PublicKey
}

// sftpServer returns the path of the OpenSSH sftp-server binary,
// or "" if none is installed.
func (c *directFakeClient) sftpServer() string {
	for _, p := range []string{
		"/usr/libexec/sftp-server",         // macOS
		"/usr/lib/openssh/sftp-server",     // Debian
		"/usr/libexec/openssh/sftp-server", // Fedora
		"/usr/lib/ssh/sftp-server",         // Arch
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (c *directFakeClient) ConnectSSH(user, authorizedPubKey string) (net.Conn, error) {
	if user != "swarming" {
		return nil, fmt.Errorf("ConnectSSH user = %q, want swarming", user)
	}
	c1, c2 := net.Pipe()
	go c.serveSSH(c2)
	return c1, nil
}

// serveSSH runs the fake buildlet sshd on conn: public key
// authentication with the gomote key, and sessions that run exec and
// shell requests locally and hand the sftp subsystem to sftp-server.
// A session requesting a pty is an error: the direct proxy path must
// not allocate one.
func (c *directFakeClient) serveSSH(conn net.Conn) {
	defer conn.Close()
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), c.authorized.Marshal()) {
				return nil, fmt.Errorf("unauthorized key")
			}
			return nil, nil
		},
	}
	config.AddHostKey(c.hostSigner)
	sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		c.t.Logf("fake sshd: handshake: %v", err)
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go c.serveSession(ch, chReqs)
	}
}

func (c *directFakeClient) serveSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		var cmd *exec.Cmd
		switch req.Type {
		default:
			req.Reply(false, nil)
			continue
		case "env":
			req.Reply(true, nil)
			continue
		case "pty-req":
			c.t.Errorf("fake sshd: unexpected pty-req on direct session")
			req.Reply(false, nil)
			continue
		case "exec":
			var p struct{ Command string }
			ssh.Unmarshal(req.Payload, &p)
			cmd = exec.Command("/bin/sh", "-c", p.Command)
		case "shell":
			cmd = exec.Command("/bin/sh")
		case "subsystem":
			var p struct{ Name string }
			ssh.Unmarshal(req.Payload, &p)
			server := c.sftpServer()
			if p.Name != "sftp" || server == "" {
				req.Reply(false, nil)
				continue
			}
			cmd = exec.Command(server)
		}
		req.Reply(true, nil)
		// Drain any remaining requests so the connection loop
		// does not stall while the command runs.
		go func() {
			for req := range reqs {
				req.Reply(false, nil)
			}
		}()
		c.runCommand(ch, cmd)
		return
	}
	ch.Close()
}

func (c *directFakeClient) runCommand(ch ssh.Channel, cmd *exec.Cmd) {
	defer ch.Close()
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.t.Errorf("fake sshd: %v", err)
		return
	}
	go func() {
		io.Copy(stdin, ch)
		stdin.Close()
	}()
	err = cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		c.t.Errorf("fake sshd: running command: %v", err)
		return
	}
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
}
