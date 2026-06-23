package crypto

import (
	"bytes"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"login":"user","password":"p@ss w0rd"}`)
	enc, err := c.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(enc, plain) {
		t.Fatal("ciphertext equals plaintext")
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatalf("round trip mismatch: got %q", dec)
	}
}

func TestEncryptNonceVaries(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := New(key)
	a, _ := c.Encrypt([]byte("same"))
	b, _ := c.Encrypt([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions produced identical output (nonce reuse)")
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	if _, err := New("not-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64 key")
	}
	if _, err := New("c2hvcnQ="); err == nil { // "short" -> 5 bytes
		t.Fatal("expected error for wrong-length key")
	}
}
