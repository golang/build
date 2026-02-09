// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type regexpList []*regexp.Regexp

func (x *regexpList) String() string {
	var s strings.Builder
	for i, r := range *x {
		if i != 0 {
			s.WriteString(",")
		}
		s.WriteString(r.String())
	}
	return s.String()
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

type regexpMap map[string]*regexp.Regexp

func (x *regexpMap) Set(s string) error {
	if *x == nil {
		*x = regexpMap{}
	}
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("missing key, expected key=value in %q", s)
	}
	re, err := regexp.Compile("(?m)" + v)
	if err != nil {
		// Get an error without our modifications.
		_, err2 := regexp.Compile(v)
		if err2 != nil {
			err = err2
		}
		return err
	}
	(*x)[k] = re
	return nil
}

func (x *regexpMap) String() string {
	var result []string
	for k, v := range *x {
		result = append(result, fmt.Sprintf("%v=%v", k, v))
	}
	return strings.Join(result, ",")
}

func (x *regexpMap) Matches(data []byte) []string {
	var matches []string
	for k, r := range *x {
		if r.Match(data) {
			matches = append(matches, k)
		}
	}
	sort.Strings(matches)
	return matches
}
