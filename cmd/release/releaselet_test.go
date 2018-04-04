package main

import (
	"testing"
)

func TestWixVersionMajor(t *testing.T) {
	parts := wixVersion("go1")

	if parts[0] != 1 {
		t.Fatalf("Incorrect major version: %v", parts)
	}
}

func TestWixVersionMajorMinor(t *testing.T) {
	parts := wixVersion("go1.34")

	if parts[0] != 1 {
		t.Fatalf("Incorrect major version: %v", parts)
	}

	if parts[1] != 34 {
		t.Fatalf("Incorrect minor version: %v", parts)
	}
}

func TestWixVersionMajorMinorBuild(t *testing.T) {
	parts := wixVersion("go1.34.7")

	if parts[0] != 1 {
		t.Fatalf("Incorrect major version: %v", parts)
	}

	if parts[1] != 34 {
		t.Fatalf("Incorrect minor version: %v", parts)
	}

	if parts[2] != 7 {
		t.Fatalf("Incorrect build version: %v", parts)
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
