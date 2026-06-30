// -------------------------------------------------------------------------------
// Shared GitHub App Client - sealSecret Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers the NaCl sealed-box encryption used for repo Actions secrets: a value
// sealed for a public key must decrypt back with the matching private key, and
// malformed keys must error.
// -------------------------------------------------------------------------------

package git

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

func TestSealSecret_RoundTrip(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub[:])

	sealed, err := sealSecret(pubB64, "ghs_supersecret")
	if err != nil {
		t.Fatalf("sealSecret: %v", err)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatalf("sealed value is not base64: %v", err)
	}
	opened, ok := box.OpenAnonymous(nil, ciphertext, pub, priv)
	if !ok {
		t.Fatal("OpenAnonymous failed to decrypt the sealed secret")
	}
	if string(opened) != "ghs_supersecret" {
		t.Errorf("decrypted %q, want ghs_supersecret", opened)
	}
}

func TestSealSecret_BadKey(t *testing.T) {
	if _, err := sealSecret("not valid base64!!", "x"); err == nil {
		t.Error("expected an error for non-base64 public key")
	}
	if _, err := sealSecret(base64.StdEncoding.EncodeToString([]byte("too-short")), "x"); err == nil {
		t.Error("expected an error for a wrong-length public key")
	}
}
