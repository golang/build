// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command writefilegenpowerscript reads an ASCII input file and a
// target windows path, and writes out a Windows PowerShell script
// that will write the content of the file to the specified location.
// Notes:
// - this program is limited to writing text files; trying
//   to write a binary would most likely not end well.
// - it is assumed that we want linux-style ("\n") and not
//   windows-style ("\r\n") line endings

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

var (
	infileflag  = flag.String("input-file", "", "Path to the input file")
	tgtpathflag = flag.String("windows-target-path", "", "Target full pathname to write on Windows")
	outfileflag = flag.String("output-file", "", "Name of script to write")
	ownerflag   = flag.String("set-owner", "", "Username to assign as owner of windows file (optional)")
	denyflag    = flag.String("deny-user-read", "", "Username to which we'll deny read permission on windows file after creation (optional)")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: writefilegenpowerscript -input-file AAA -output-file BBB -windows-target-path C:\\CCC")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *infileflag == "" || *outfileflag == "" || *tgtpathflag == "" {
		flag.Usage()
		os.Exit(2)
	}
	// vet the windows path
	if !strings.HasPrefix(*tgtpathflag, "C:\\") {
		fmt.Fprintf(os.Stderr, "warning: suspicious windows target path %q, does not start with C drive prefix\n", *tgtpathflag)
	}

	// Slurp in the input file.
	var lines []string
	if content, err := os.ReadFile(*infileflag); err != nil {
		log.Fatalf("reading %s: error %v", *infileflag, err)
	} else {
		lines = strings.Split(string(content), "\n")
	}

	// Open output file
	of, err := os.OpenFile(*outfileflag, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatalf("opening %s for write: error %v", *outfileflag, err)
	}

	// Write content.
	fmt.Fprintf(of, "Set-StrictMode -Version Latest\n")
	fmt.Fprintf(of, "$path = \"%s\"\n", *tgtpathflag)
	line0 := lines[0]
	fmt.Fprintf(of, "$line0 = \"%s\"\n", line0)
	fmt.Fprintf(of, "$line0 | Out-File -Encoding ascii $path\n")
	for i := 1; i < len(lines)-1; i++ {
		line := lines[i]
		fmt.Fprintf(of, "Add-Content -Encoding ascii -Path $path -Value \"%s\"\n", line)
	}

	// The file we wrote has windows-style line endings; emit a
	// separate step to convert back to linux-style.
	fmt.Fprintf(of, "((Get-Content $path) -join \"`n\") + \"`n\" | Set-Content -NoNewline $path\n")

	// Honor the -set-owner and/or -deny-user-read flag if set.
	if *ownerflag != "" {
		fmt.Fprintf(of, "icacls $path /setowner %s\n", *ownerflag)
	}
	if *denyflag != "" {
		fmt.Fprintf(of, "icacls $path /deny %s:r\n", *denyflag)
	}

	// We're done.
	if err := of.Close(); err != nil {
		log.Fatalf("closing %s: error %v", *outfileflag, err)
	}
}
