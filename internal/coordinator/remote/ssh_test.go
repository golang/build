// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package remote

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/crypto/ssh"
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
