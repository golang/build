// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gerrit_test

import (
	"fmt"

	"golang.org/x/build/gerrit"
	"golang.org/x/net/context"
)

func Example() {
	c := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
	info, err := c.GetProjectInfo(context.TODO(), "go")
	if err != nil {
		panic(err)
	}
	fmt.Println(info.Description)
}
