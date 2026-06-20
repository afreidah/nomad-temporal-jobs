// -------------------------------------------------------------------------------
// Shared SSH Client - Credential Parsing Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers the credential helpers split out of NewSSHClient: key parsing, the
// cert-or-key auth builder (a present-but-broken cert is a hard error, not a
// silent fall-through), and the host-CA verification callback.
// -------------------------------------------------------------------------------

package shared

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// testSigner returns an ed25519 SSH signer and its private key in PEM form.
func testSigner(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return signer, pem.EncodeToMemory(block)
}

func TestLoadSigner(t *testing.T) {
	_, keyPEM := testSigner(t)

	if _, err := loadSigner(writeTemp(t, "id", keyPEM)); err != nil {
		t.Errorf("loadSigner on a valid key: %v", err)
	}
	if _, err := loadSigner(writeTemp(t, "bad", []byte("not a key"))); err == nil {
		t.Error("loadSigner should error on garbage")
	}
	if _, err := loadSigner(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("loadSigner should error on a missing file")
	}
}

func TestBuildAuthMethods(t *testing.T) {
	signer, _ := testSigner(t)

	t.Run("key only when no cert path", func(t *testing.T) {
		auth, err := buildAuthMethods(signer, "")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(auth) != 1 {
			t.Errorf("got %d auth methods, want 1 (key only)", len(auth))
		}
	})

	t.Run("valid cert yields cert then key", func(t *testing.T) {
		caSigner, _ := testSigner(t)
		cert := &ssh.Certificate{
			Key:         signer.PublicKey(),
			CertType:    ssh.UserCert,
			ValidBefore: ssh.CertTimeInfinity,
		}
		if err := cert.SignCert(rand.Reader, caSigner); err != nil {
			t.Fatalf("sign cert: %v", err)
		}
		path := writeTemp(t, "id-cert.pub", ssh.MarshalAuthorizedKey(cert))
		auth, err := buildAuthMethods(signer, path)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(auth) != 2 {
			t.Errorf("got %d auth methods, want 2 (cert + key)", len(auth))
		}
	})

	// A cert path that can't yield a valid certificate must be a hard error,
	// never a silent fallback to key-only auth.
	errCases := []struct {
		name string
		path string
	}{
		{"broken cert", writeTemp(t, "broken", []byte("garbage"))},
		{"non-certificate public key", writeTemp(t, "pub", ssh.MarshalAuthorizedKey(signer.PublicKey()))},
		{"missing cert file", filepath.Join(t.TempDir(), "nope")},
	}
	for _, c := range errCases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := buildAuthMethods(signer, c.path); err == nil {
				t.Errorf("%s: expected an error, not a silent fallback", c.name)
			}
		})
	}
}

func TestHostCACallback(t *testing.T) {
	signer, _ := testSigner(t)

	cb, err := hostCACallback(writeTemp(t, "ca.pub", ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if err != nil || cb == nil {
		t.Errorf("hostCACallback on a valid CA: cb=%v err=%v", cb, err)
	}
	if _, err := hostCACallback(writeTemp(t, "bad", []byte("garbage"))); err == nil {
		t.Error("hostCACallback should error on a garbage CA")
	}
	if _, err := hostCACallback(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("hostCACallback should error on a missing file")
	}
}
