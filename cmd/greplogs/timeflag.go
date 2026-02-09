// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"time"
)

const (
	rfc3339Date     = "2006-01-02"
	rfc3339DateTime = "2006-01-02T15:04:05"
)

// A timeFlag is a flag.Getter that parses a time.Time
// from either an RFC-3339 date or an RFC-3339 date and time.
//
// Fractional seconds and explicit time zones are not allowed.
type timeFlag struct {
	Time time.Time
}

var _ = flag.Getter((*timeFlag)(nil))

func (tf *timeFlag) Set(s string) error {
	if s == "" {
		tf.Time = time.Time{}
		return nil
	}

	t, err := time.Parse(rfc3339Date, s)
	if err != nil {
		t, err = time.Parse(rfc3339DateTime, s)
	}
	if err == nil {
		tf.Time = t
	}
	return err
}

func (tf *timeFlag) String() string {
	if tf.Time.IsZero() {
		return ""
	}
	if tf.Time.Hour() == 0 && tf.Time.Minute() == 0 && tf.Time.Second() == 0 {
		return tf.Time.Format(rfc3339Date)
	}
	return tf.Time.Format(rfc3339DateTime)
}

func (tf *timeFlag) Get() any {
	return tf.Time
}
