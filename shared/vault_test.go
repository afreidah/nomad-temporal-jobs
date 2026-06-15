// -------------------------------------------------------------------------------
// Shared Vault Client - Unit Tests
//
// Author: Alex Freidah
//
// Covers client construction: a CA cert must not break TLS setup (the OTel
// transport wrapper has to be applied after Vault configures TLS, not before),
// and a missing Workload Identity token surfaces ErrNoVaultToken.
// -------------------------------------------------------------------------------

package shared

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCA generates a throwaway self-signed CA and returns its PEM path.
func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	return path
}

func TestNewVaultClient_WithCACert(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault.example:8200")
	t.Setenv("VAULT_CACERT", writeTestCA(t))
	t.Setenv("VAULT_TOKEN", "test-token")
	t.Setenv("VAULT_TOKEN_FILE", "")

	vc, err := NewVaultClient()
	if err != nil {
		t.Fatalf("NewVaultClient with a CA cert must succeed: %v", err)
	}
	if vc == nil {
		t.Fatal("client is nil")
	}
}

func TestNewVaultClient_NoToken(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault.example:8200")
	t.Setenv("VAULT_CACERT", "")
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_TOKEN_FILE", "")

	if _, err := NewVaultClient(); !errors.Is(err, ErrNoVaultToken) {
		t.Errorf("error = %v, want ErrNoVaultToken", err)
	}
}
