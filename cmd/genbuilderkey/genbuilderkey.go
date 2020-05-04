// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The genbuilderkey binary generates a builder key or gomote user key
// from the build system's master key.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/build/internal/secret"
)

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: genbuilderkey <Host Type>")
	fmt.Fprintln(os.Stderr, "Master builder key should be available to genbuilderkey by either:")
	fmt.Fprintln(os.Stderr, " - File: $HOME/keys/gobuilder-master.key")
	fmt.Fprintln(os.Stderr, " - Secret Management: executing genbuilderkey with access to secret management")
	flag.PrintDefaults()
}

func main() {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	fmt.Println(key(flag.Arg(0)))
}

func key(principal string) string {
	h := hmac.New(md5.New, getMasterKey())
	io.WriteString(h, principal)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func getMasterKey() []byte {
	v, err := getMasterKeyFromSecretManager()
	if err == nil {
		return []byte(strings.TrimSpace(v))
	}
	key, err := ioutil.ReadFile(filepath.Join(os.Getenv("HOME"), "keys/gobuilder-master.key"))
	if err == nil {
		return bytes.TrimSpace(key)
	}
	log.Fatalf("no builder master key found")
	panic("not reachable")
}

// getMasterKeyFromSecretManager retrieves the master key from the secret
// manager service.
func getMasterKeyFromSecretManager() (string, error) {
	sc, err := secret.NewClient()
	if err != nil {
		return "", err
	}
	defer sc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return sc.Retrieve(ctx, secret.NameBuilderMasterKey)
}
