package main

import (
	"testing"
)

func TestWixVersionMajor(t *testing.T) {
	major, minor, build := wixVersion("go1")

	if major != 1 {
		t.Fatalf("Incorrect major version: %v", major)
	}

	if minor != 0 {
		t.Fatalf("Incorrect minor version: %v", minor)
	}

	if build != 0 {
		t.Fatalf("Incorrect build version: %v", build)
	}
}

func TestWixVersionMajorMinor(t *testing.T) {
	major, minor, build := wixVersion("go1.34")

	if major != 1 {
		t.Fatalf("Incorrect major version: %v", major)
	}

	if minor != 34 {
		t.Fatalf("Incorrect minor version: %v", minor)
	}

	if build != 0 {
		t.Fatalf("Incorrect build version: %v", build)
	}
}

func TestWixVersionMajorMinorBuild(t *testing.T) {
	major, minor, build := wixVersion("go1.34.7")

	if major != 1 {
		t.Fatalf("Incorrect major version: %v", major)
	}

	if minor != 34 {
		t.Fatalf("Incorrect minor version: %v", minor)
	}

	if build != 7 {
		t.Fatalf("Incorrect build version: %v", build)
	}
}

func TestWixIsWinXPSupported(t *testing.T) {
	if wixIsWinXPSupported("go1.9") != true {
		t.Fatal("Expected Windows XP to be supported")
	}
	if wixIsWinXPSupported("go1.10") != true {
		t.Fatal("Expected Windows XP to be supported")
	}
	if wixIsWinXPSupported("go1.11") != false {
		t.Fatal("Expected Windows XP to be unsupported")
	}
	if wixIsWinXPSupported("go1.12") != false {
		t.Fatal("Expected Windows XP to be unsupported")
	}
}
