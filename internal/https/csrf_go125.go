// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.25

package https

// This implementation of CrossOriginProtection is
// an alias of net/http.CrossOriginProtection.
//
// TODO: Delete after go.mod is updated to 1.25.0 or higher.

import "net/http"

// CrossOriginProtection is [http.CrossOriginProtection].
type CrossOriginProtection = http.CrossOriginProtection
