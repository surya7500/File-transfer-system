package main

import "testing"

func TestMakeQRProducesPNG(t *testing.T) {
	b, err := makeQR("http://192.168.1.12:5000")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 100 {
		t.Fatalf("unexpectedly small PNG: %d", len(b))
	}
	if string(b[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("output is not PNG")
	}
}

func TestPINNormalization(t *testing.T) {
	if got := normalizePIN("12345 67890"); got != "1234567890" {
		t.Fatalf("got %q", got)
	}
	if got := normalizePIN("12345-67890"); got != "1234567890" {
		t.Fatalf("got %q", got)
	}
}
