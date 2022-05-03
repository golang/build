// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	gssh "github.com/gliderlabs/ssh"
	"golang.org/x/build/internal/gophers"
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

// SSHServer is the SSH server that the coordinator provides.
type SSHServer struct {
	server *gssh.Server
}

// NewSSHServer creates an SSH server used to access remote buildlet sessions.
func NewSSHServer(addr string, hostPrivateKey, caPrivateKey []byte, handler gssh.Handler, sp *SessionPool) (*SSHServer, error) {
	hostSigner, err := ssh.ParsePrivateKey(hostPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH host key: %v; not running SSH server", err)
	}
	CASigner, err := ssh.ParsePrivateKey(caPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH host key: %v; not running SSH server", err)
	}
	return &SSHServer{
		server: &gssh.Server{
			Addr:             addr,
			Handler:          handler,
			PublicKeyHandler: chainSSHPublicKeyHandlers(handleSSHPublicKeyAuth, handleCertificateAuthFunc(sp, CASigner)),
			HostSigners:      []gssh.Signer{hostSigner},
		},
	}, nil
}

// ListenAndServe attempts to start the SSH server. This blocks until the server stops.
func (ss *SSHServer) ListenAndServe() error {
	return ss.server.ListenAndServe()
}

// Close imediately closes all active listeners and connections.
func (ss *SSHServer) Close() error {
	return ss.server.Close()
}

// serve attempts to start the SSH server and listens with the passed in net.Listener. This blocks
// until the server stops. This should be used while testing the server.
func (ss *SSHServer) serve(l net.Listener) error {
	return ss.server.Serve(l)
}

// WriteSSHPrivateKeyToTempFile writes a key to a temporary file on the local file system. It also
// sets the permissions on the file to what is expected by OpenSSH implementations of SSH.
func WriteSSHPrivateKeyToTempFile(key []byte) (path string, err error) {
	tf, err := ioutil.TempFile("", "ssh-priv-key")
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

// chainSSHPublicKeyHandlers allows you to chain public key authentication handlers. Each handler is
// attempted until one succeeds.
func chainSSHPublicKeyHandlers(handlers ...gssh.PublicKeyHandler) gssh.PublicKeyHandler {
	return func(ctx gssh.Context, key gssh.PublicKey) bool {
		for _, handler := range handlers {
			if handler(ctx, key) {
				return true
			}
		}
		return false
	}
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

		_, err := sp.Session(sessionID)
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
		return true
	}
}

func handleSSHPublicKeyAuth(ctx gssh.Context, key gssh.PublicKey) bool {
	inst := ctx.User() // expected to be of form "user-USER-goos-goarch-etc"
	user := UserFromGomoteInstanceName(inst)
	if user == "" {
		return false
	}
	// Map the gomote username to the github username, and use the
	// github user's public ssh keys for authentication. This is
	// mostly of laziness and pragmatism, not wanting to invent or
	// maintain a new auth mechanism or password/key registry.
	githubUser := gophers.GitHubOfGomoteUser(user)
	keys := githubPublicKeys(githubUser)
	for _, authKey := range keys {
		if gssh.KeysEqual(key, authKey.PublicKey) {
			log.Printf("for instance %q, github user %q key matched: %s", inst, githubUser, authKey.AuthorizedLine)
			return true
		}
	}
	return false
}

// authorizedKey is a Github user's SSH authorized key, in both string and parsed format.
type authorizedKey struct {
	AuthorizedLine string // e.g. "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILj8HGIG9NsT34PHxO8IBq0riSBv7snp30JM8AanBGoV"
	PublicKey      ssh.PublicKey
}

func githubPublicKeys(user string) []authorizedKey {
	// TODO: caching, rate limiting.
	req, err := http.NewRequest("GET", "https://github.com/"+user+".keys", nil)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("getting %s github keys: %v", user, err)
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil
	}
	var keys []authorizedKey
	bs := bufio.NewScanner(res.Body)
	for bs.Scan() {
		key, _, _, _, err := ssh.ParseAuthorizedKey(bs.Bytes())
		if err != nil {
			log.Printf("parsing github user %q key %q: %v", user, bs.Text(), err)
			continue
		}
		keys = append(keys, authorizedKey{
			PublicKey:      key,
			AuthorizedLine: strings.TrimSpace(bs.Text()),
		})
	}
	if err := bs.Err(); err != nil {
		return nil
	}
	return keys
}

// UserFromGomoteInstanceName returns the username part of a gomote
// remote instance name.
//
// The instance name is of two forms. The normal form is:
//
//	user-bradfitz-linux-amd64-0
//
// The overloaded form to convey that the user accepts responsibility
// for changes to the underlying host is to prefix the same instance
// name with the string "mutable-", such as:
//
//	mutable-user-bradfitz-darwin-amd64-10_8-0
//
// The mutable part is ignored by this function.
func UserFromGomoteInstanceName(name string) string {
	name = strings.TrimPrefix(name, "mutable-")
	if !strings.HasPrefix(name, "user-") {
		return ""
	}
	user := name[len("user-"):]
	hyphen := strings.IndexByte(user, '-')
	if hyphen == -1 {
		return ""
	}
	return user[:hyphen]
}
