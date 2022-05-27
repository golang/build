// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "regexp"

type regexpList []*regexp.Regexp

func (x *regexpList) String() string {
	s := ""
	for i, r := range *x {
		if i != 0 {
			s += ","
		}
		s += r.String()
	}
	return s
}

func (x *regexpList) Set(s string) error {
	re, err := regexp.Compile("(?m)" + s)
	if err != nil {
		// Get an error without our modifications.
		_, err2 := regexp.Compile(s)
		if err2 != nil {
			err = err2
		}
		return err
	}
	*x = append(*x, re)
	return nil
}

func (x *regexpList) AllMatch(data []byte) bool {
	for _, r := range *x {
		if !r.Match(data) {
			return false
		}
	}
	return true
}

func (x *regexpList) AnyMatchString(data string) bool {
	for _, r := range *x {
		if r.MatchString(data) {
			return true
		}
	}
	return false
}

func (x *regexpList) Matches(data []byte) [][]int {
	matches := [][]int{}
	for _, r := range *x {
		matches = append(matches, r.FindAllSubmatchIndex(data, -1)...)
	}
	return matches
}
