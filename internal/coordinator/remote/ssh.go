// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

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
