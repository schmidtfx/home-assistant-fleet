package secrets

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	b, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := b.Seal([]byte("token"), []byte("inst-1"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := b.Open(ct, []byte("inst-1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "token" {
		t.Fatalf("got %q", pt)
	}
}

func TestAADMismatch(t *testing.T) {
	b, _ := New(bytes.Repeat([]byte{7}, 32))
	ct, _ := b.Seal([]byte("token"), []byte("inst-1"))
	if _, err := b.Open(ct, []byte("inst-2")); err == nil {
		t.Fatal("expected error when AAD differs")
	}
}

func TestBadKey(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}
