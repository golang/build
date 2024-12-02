// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/creack/pty"
	gssh "github.com/gliderlabs/ssh"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/envutil"
	"golang.org/x/crypto/ssh"
)

// SignPublicSSHKey signs a public SSH key using the certificate authority. These keys are intended for use with the specified gomote and owner.
// The public SSH are intended to be used in OpenSSH certificate authentication with the gomote SSH server.
func SignPublicSSHKey(ctx context.Context, caPriKey ssh.Signer, rawPubKey []byte, sessionID, ownerID string, d time.Duration) ([]byte, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(rawPubKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse public key=%w", err)
	}
	cert := &ssh.Certificate{
		Key:             pubKey,
		Serial:          1,
		CertType:        ssh.UserCert,
		KeyId:           "go_build",
		ValidPrincipals: []string{fmt.Sprintf("%s@farmer.golang.org", sessionID), ownerID},
		ValidAfter:      uint64(time.Now().Unix()),
		ValidBefore:     uint64(time.Now().Add(d).Unix()),
		Permissions: ssh.Permissions{
			Extensions: map[string]string{
				"permit-X11-forwarding":   "",
				"permit-agent-forwarding": "",
				"permit-port-forwarding":  "",
				"permit-pty":              "",
				"permit-user-rc":          "",
			},
		},
	}
	if err := cert.SignCert(rand.Reader, caPriKey); err != nil {
		return nil, fmt.Errorf("cerificate.SignCert() = %w", err)
	}
	mCert := ssh.MarshalAuthorizedKey(cert)
	return mCert, nil
}

// SSHKeyPair generates a set of ecdsa256 SSH Keys. The public key is serialized for inclusion in
// an OpenSSH authorized_keys file. The private key is PEM encoded.
func SSHKeyPair() (privateKey []byte, publicKey []byte, err error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	public, err := ssh.NewPublicKey(&private.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	publicKey = ssh.MarshalAuthorizedKey(public)
	priKeyByt, err := x509.MarshalECPrivateKey(private)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to marshal private key=%w", err)
	}
	privateKey = pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: priKeyByt,
	})
	return
}

// SSHOption are options to set for the SSH server.
type SSHOption func(*SSHServer)

// EnableLUCIOption sets the configuration needed for swarming bots to connect to the
// SSH server.
func EnableLUCIOption() SSHOption {
	return func(s *SSHServer) {
		s.server.Handler = s.HandleIncomingSSHPostAuthSwarming
	}
}

// SSHServer is the SSH server that the coordinator provides.
type SSHServer struct {
	gomotePublicKey    string
	privateHostKeyFile string
	server             *gssh.Server
	sessionPool        *SessionPool
}

// NewSSHServer creates an SSH server used to access remote buildlet sessions.
func NewSSHServer(addr string, hostPrivateKey, gomotePublicKey, caPrivateKey []byte, sp *SessionPool, opts ...SSHOption) (*SSHServer, error) {
	hostSigner, err := ssh.ParsePrivateKey(hostPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH host key: %v; not configuring SSH server", err)
	}
	CASigner, err := ssh.ParsePrivateKey(caPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH host key: %v; not configuring SSH server", err)
	}
	privateHostKeyFile, err := WriteSSHPrivateKeyToTempFile(hostPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("error writing ssh private key to temp file: %v; not configuring SSH server", err)
	}
	if len(gomotePublicKey) == 0 {
		return nil, errors.New("invalid gomote public key")
	}
	s := &SSHServer{
		gomotePublicKey:    string(gomotePublicKey),
		privateHostKeyFile: privateHostKeyFile,
		sessionPool:        sp,
		server: &gssh.Server{
			Addr:             addr,
			PublicKeyHandler: handleCertificateAuthFunc(sp, CASigner),
			HostSigners:      []gssh.Signer{hostSigner},
		},
	}
	s.server.Handler = s.HandleIncomingSSHPostAuth
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// ListenAndServe attempts to start the SSH server. This blocks until the server stops.
func (ss *SSHServer) ListenAndServe() error {
	return ss.server.ListenAndServe()
}

// Close immediately closes all active listeners and connections.
func (ss *SSHServer) Close() error {
	return ss.server.Close()
}

// serve attempts to start the SSH server and listens with the passed in net.Listener. This blocks
// until the server stops. This should be used while testing the server.
func (ss *SSHServer) serve(l net.Listener) error {
	return ss.server.Serve(l)
}

// HandleIncomingSSHPostAuth handles post-authentication requests for the SSH server. This handler uses
// Sessions for session management.
func (ss *SSHServer) HandleIncomingSSHPostAuth(s gssh.Session) {
	inst := s.User()
	ptyReq, winCh, isPty := s.Pty()
	if !isPty {
		fmt.Fprintf(s, "scp etc not yet supported; https://golang.org/issue/21140\n")
		return
	}
	rs, err := ss.sessionPool.Session(inst)
	if err != nil {
		fmt.Fprintf(s, "unknown instance %q", inst)
		return
	}
	hostConf, ok := dashboard.Hosts[rs.HostType]
	if !ok {
		fmt.Fprintf(s, "instance %q has unknown host type %q\n", inst, rs.HostType)
		return
	}
	bconf, ok := dashboard.Builders[rs.BuilderType]
	if !ok {
		fmt.Fprintf(s, "instance %q has unknown builder type %q\n", inst, rs.BuilderType)
		return
	}

	ctx, cancel := context.WithCancel(s.Context())
	defer cancel()
	if err := ss.sessionPool.KeepAlive(ctx, inst); err != nil {
		log.Printf("ssh: KeepAlive on session=%s failed: %s", inst, err)
	}

	sshUser := hostConf.SSHUsername
	useLocalSSHProxy := bconf.GOOS() != "plan9"
	if sshUser == "" && useLocalSSHProxy {
		fmt.Fprintf(s, "instance %q host type %q does not have SSH configured\n", inst, rs.HostType)
		return
	}
	if !hostConf.IsHermetic() {
		fmt.Fprintf(s, "WARNING: instance %q host type %q is not currently\n", inst, rs.HostType)
		fmt.Fprintf(s, "configured to have a hermetic filesystem per boot.\n")
		fmt.Fprintf(s, "You must be careful not to modify machine state\n")
		fmt.Fprintf(s, "that will affect future builds.\n")
	}
	log.Printf("connecting to ssh to instance %q ...", inst)
	fmt.Fprint(s, "# Welcome to the gomote ssh proxy.\n")
	fmt.Fprint(s, "# Connecting to/starting remote ssh...\n")
	fmt.Fprint(s, "#\n")

	var localProxyPort int
	bc, err := ss.sessionPool.BuildletClient(inst)
	if err != nil {
		fmt.Fprintf(s, "failed to connect to ssh on %s: %v\n", inst, err)
		return
	}
	if useLocalSSHProxy {
		sshConn, err := bc.ConnectSSH(sshUser, ss.gomotePublicKey)
		log.Printf("buildlet(%q).ConnectSSH = %T, %v", inst, sshConn, err)
		if err != nil {
			fmt.Fprintf(s, "failed to connect to ssh on %s: %v\n", inst, err)
			return
		}
		defer sshConn.Close()

		// Now listen on some localhost port that we'll proxy to sshConn.
		// The openssh ssh command line tool will connect to this IP.
		ln, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			fmt.Fprintf(s, "local listen error: %v\n", err)
			return
		}
		localProxyPort = ln.Addr().(*net.TCPAddr).Port
		log.Printf("ssh local proxy port for %s: %v", inst, localProxyPort)
		var lnCloseOnce sync.Once
		lnClose := func() { lnCloseOnce.Do(func() { ln.Close() }) }
		defer lnClose()

		// Accept at most one connection from localProxyPort and proxy
		// it to sshConn.
		go func() {
			c, err := ln.Accept()
			lnClose()
			if err != nil {
				return
			}
			defer c.Close()
			errc := make(chan error, 1)
			go func() {
				_, err := io.Copy(c, sshConn)
				errc <- err
			}()
			go func() {
				_, err := io.Copy(sshConn, c)
				errc <- err
			}()
			err = <-errc
		}()
	}
	workDir, err := bc.WorkDir(ctx)
	if err != nil {
		fmt.Fprintf(s, "Error getting WorkDir: %v\n", err)
		return
	}
	ip, _, ipErr := net.SplitHostPort(bc.IPPort())

	fmt.Fprint(s, "# `gomote push` and the builders use:\n")
	fmt.Fprintf(s, "# - workdir: %s\n", workDir)
	fmt.Fprintf(s, "# - GOROOT: %s/go\n", workDir)
	fmt.Fprintf(s, "# - GOPATH: %s/gopath\n", workDir)
	fmt.Fprintf(s, "# - env: %s\n", strings.Join(bconf.Env(), " ")) // TODO: shell quote?
	fmt.Fprint(s, "# Happy debugging.\n")

	log.Printf("ssh to %s: starting ssh -p %d for %s@localhost", inst, localProxyPort, sshUser)
	var cmd *exec.Cmd
	switch bconf.GOOS() {
	default:
		cmd = exec.Command("ssh",
			"-p", strconv.Itoa(localProxyPort),
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "StrictHostKeyChecking=no",
			"-i", ss.privateHostKeyFile,
			sshUser+"@localhost")
	case "plan9":
		fmt.Fprintf(s, "# Plan9 user/pass: glenda/glenda123\n")
		if ipErr != nil {
			fmt.Fprintf(s, "# Failed to get IP out of %q: %v\n", bc.IPPort(), ipErr)
			return
		}
		cmd = exec.Command("/usr/local/bin/drawterm",
			"-a", ip, "-c", ip, "-u", "glenda", "-k", "user=glenda")
	}
	envutil.SetEnv(cmd, "TERM="+ptyReq.Term)
	f, err := pty.Start(cmd)
	if err != nil {
		log.Printf("running ssh client to %s: %v", inst, err)
		return
	}
	defer f.Close()
	go func() {
		for win := range winCh {
			setWinsize(f, win.Width, win.Height)
		}
	}()
	go func() {
		ss.setupRemoteSSHEnv(bconf, workDir, f)
		io.Copy(f, s) // stdin
	}()
	io.Copy(s, f) // stdout
	cmd.Process.Kill()
	cmd.Wait()
}

// HandleIncomingSSHPostAuthSwarming handles post-authentication requests for the SSH server. This handler uses
// Sessions for session management.
func (ss *SSHServer) HandleIncomingSSHPostAuthSwarming(s gssh.Session) {
	inst := s.User()
	ptyReq, winCh, isPty := s.Pty()
	if !isPty {
		fmt.Fprintf(s, "scp etc not yet supported; https://go.dev/issue/21140\n")
		return
	}
	rs, err := ss.sessionPool.Session(inst)
	if err != nil {
		fmt.Fprintf(s, "unknown instance %q", inst)
		return
	}
	ctx, cancel := context.WithCancel(s.Context())
	defer cancel()
	if err := ss.sessionPool.KeepAlive(ctx, inst); err != nil {
		log.Printf("ssh: KeepAlive on session=%s failed: %s", inst, err)
	}

	sshUser := "swarming"
	isPlan9 := strings.Contains(rs.HostType, "plan9")
	useLocalSSHProxy := !isPlan9
	if sshUser == "" && useLocalSSHProxy {
		fmt.Fprintf(s, "instance %q host type %q does not have SSH configured\n", inst, rs.HostType)
		return
	}
	// TODO(go.dev/issue/64064) do we still need hermetic checks?
	log.Printf("connecting to ssh to instance %q ...", inst)
	fmt.Fprint(s, "# Welcome to the gomote ssh proxy.\n")
	fmt.Fprint(s, "# Connecting to/starting remote ssh...\n")
	fmt.Fprint(s, "#\n")

	var localProxyPort int
	bc, err := ss.sessionPool.BuildletClient(inst)
	if err != nil {
		fmt.Fprintf(s, "failed to connect to ssh on %s: %v\n", inst, err)
		return
	}
	if useLocalSSHProxy {
		sshConn, err := bc.ConnectSSH(sshUser, ss.gomotePublicKey)
		log.Printf("buildlet(%q).ConnectSSH = %T, %v", inst, sshConn, err)
		if err != nil {
			fmt.Fprintf(s, "failed to connect to ssh on %s: %v\n", inst, err)
			return
		}
		defer sshConn.Close()

		// Now listen on some localhost port that we'll proxy to sshConn.
		// The openssh ssh command line tool will connect to this IP.
		ln, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			fmt.Fprintf(s, "local listen error: %v\n", err)
			return
		}
		localProxyPort = ln.Addr().(*net.TCPAddr).Port
		log.Printf("ssh local proxy port for %s: %v", inst, localProxyPort)
		var lnCloseOnce sync.Once
		lnClose := func() { lnCloseOnce.Do(func() { ln.Close() }) }
		defer lnClose()

		// Accept at most one connection from localProxyPort and proxy
		// it to sshConn.
		go func() {
			c, err := ln.Accept()
			lnClose()
			if err != nil {
				return
			}
			defer c.Close()
			errc := make(chan error, 1)
			go func() {
				_, err := io.Copy(c, sshConn)
				errc <- err
			}()
			go func() {
				_, err := io.Copy(sshConn, c)
				errc <- err
			}()
			err = <-errc
		}()
	}
	workDir, err := bc.WorkDir(ctx)
	if err != nil {
		fmt.Fprintf(s, "Error getting WorkDir: %v\n", err)
		return
	}
	ip, _, ipErr := net.SplitHostPort(bc.IPPort())

	fmt.Fprint(s, "# `gomote push` and the builders use:\n")
	fmt.Fprintf(s, "# - workdir: %s\n", workDir)
	fmt.Fprintf(s, "# - GOROOT: %s/go\n", workDir)
	fmt.Fprintf(s, "# - GOPATH: %s/gopath\n", workDir)
	fmt.Fprint(s, "# Happy debugging.\n")

	log.Printf("ssh to %s: starting ssh -p %d for %s@localhost", inst, localProxyPort, sshUser)
	cmd := exec.Command("ssh",
		"-p", strconv.Itoa(localProxyPort),
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-i", ss.privateHostKeyFile,
		sshUser+"@localhost")
	if isPlan9 {
		fmt.Fprintf(s, "# Plan9 user/pass: glenda/glenda123\n")
		if ipErr != nil {
			fmt.Fprintf(s, "# Failed to get IP out of %q: %v\n", bc.IPPort(), ipErr)
			return
		}
		cmd = exec.Command("/usr/local/bin/drawterm",
			"-a", ip, "-c", ip, "-u", "glenda", "-k", "user=glenda")
	}

	envutil.SetEnv(cmd, "TERM="+ptyReq.Term)
	f, err := pty.Start(cmd)
	if err != nil {
		log.Printf("running ssh client to %s: %v", inst, err)
		return
	}
	defer f.Close()
	go func() {
		for win := range winCh {
			setWinsize(f, win.Width, win.Height)
		}
	}()
	go io.Copy(f, s) // stdin
	io.Copy(s, f)    // stdout
	cmd.Process.Kill()
	cmd.Wait()
}

// setupRemoteSSHEnv sets up environment variables on the remote system.
// This makes the new SSH session easier to use for Go testing.
func (ss *SSHServer) setupRemoteSSHEnv(bconf *dashboard.BuildConfig, workDir string, f io.Writer) {
	switch bconf.GOOS() {
	default:
		// A Unix system.
		for _, env := range bconf.Env() {
			fmt.Fprintln(f, env)
			if idx := strings.Index(env, "="); idx > 0 {
				fmt.Fprintf(f, "export %s\n", env[:idx])
			}
		}
		fmt.Fprintf(f, "GOPATH=%s/gopath\n", workDir)
		fmt.Fprintf(f, "PATH=$PATH:%s/go/bin\n", workDir)
		fmt.Fprintf(f, "export GOPATH PATH\n")
		fmt.Fprintf(f, "cd %s/go/src\n", workDir)
	case "windows":
		for _, env := range bconf.Env() {
			fmt.Fprintf(f, "set %s\n", env)
		}
		fmt.Fprintf(f, `set GOPATH=%s\gopath`+"\n", workDir)
		fmt.Fprintf(f, `set PATH=%%PATH%%;%s\go\bin`+"\n", workDir)
		fmt.Fprintf(f, `cd %s\go\src`+"\n", workDir)
	case "plan9":
		// TODO
	}
}

// WriteSSHPrivateKeyToTempFile writes a key to a temporary file on the local file system. It also
// sets the permissions on the file to what is expected by OpenSSH implementations of SSH.
func WriteSSHPrivateKeyToTempFile(key []byte) (path string, err error) {
	tf, err := os.CreateTemp("", "ssh-priv-key")
	if err != nil {
		return "", err
	}
	if err := tf.Chmod(0600); err != nil {
		return "", err
	}
	if _, err := tf.Write(key); err != nil {
		return "", err
	}
	return tf.Name(), tf.Close()
}

// handleCertificateAuthFunc creates a function that authenticates the session using OpenSSH certificate
// authentication. The passed in certificate is tested to ensure it is valid, signed by the CA and
// corresponds to an existing session.
func handleCertificateAuthFunc(sp *SessionPool, caKeySigner ssh.Signer) gssh.PublicKeyHandler {
	return func(ctx gssh.Context, key gssh.PublicKey) bool {
		sessionID := ctx.User()
		cert, ok := key.(*ssh.Certificate)
		if !ok {
			log.Printf("public key is not a certificate session=%s", sessionID)
			return false
		}
		if cert.CertType != ssh.UserCert {
			log.Printf("certificate not user cert session=%s", sessionID)
			return false
		}
		if !bytes.Equal(cert.SignatureKey.Marshal(), caKeySigner.PublicKey().Marshal()) {
			log.Printf("certificate is not signed by recognized Certificate Authority session=%s", sessionID)
			return false
		}

		ses, err := sp.Session(sessionID)
		if err != nil {
			log.Printf("HandleCertificateAuth: unable to retrieve session=%s: %s", sessionID, err)
			return false
		}
		certChecker := &ssh.CertChecker{}
		wantPrincipal := fmt.Sprintf("%s@farmer.golang.org", sessionID)
		if err := certChecker.CheckCert(wantPrincipal, cert); err != nil {
			log.Printf("certChecker.CheckCert(%s, user_certificate) = %s", wantPrincipal, err)
			return false
		}
		for _, principal := range cert.ValidPrincipals {
			if principal == ses.OwnerID {
				return true
			}
		}
		log.Printf("HandleCertificateAuth: unable to verify ownerID in certificate principals")
		return false
	}
}

// authorizedKey is a Github user's SSH authorized key, in both string and parsed format.
type authorizedKey struct {
	AuthorizedLine string // e.g. "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILj8HGIG9NsT34PHxO8IBq0riSBv7snp30JM8AanBGoV"
	PublicKey      ssh.PublicKey
}

func setWinsize(f *os.File, w, h int) {
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{uint16(h), uint16(w), 0, 0})))
}
