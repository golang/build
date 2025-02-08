// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package influx provides common constants for setting up and access the
// performance monitoring InfluxDB instance.
package influx

const (
	// Org is the Influx organization name.
	Org = "golang"

	// Bucker is the Influx bucket name.
	Bucket = "perf"
)

// The names of the password/token secrets in Google Secret Manager.
const (
	AdminPassSecretName   = "influx-admin-pass"
	AdminTokenSecretName  = "influx-admin-token"
	ReaderPassSecretName  = "influx-reader-pass"
	ReaderTokenSecretName = "influx-reader-token"
)
