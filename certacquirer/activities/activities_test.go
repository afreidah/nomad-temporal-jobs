// -------------------------------------------------------------------------------
// Cert Acquirer Activities - Unit Tests
//
// Author: Alex Freidah
//
// Covers the Vault-backed and classification logic that does not require a
// live ACME endpoint: ACME account load/save round-trips, the staged-publish
// promotion, the missing-stage guard, and rate-limit error classification.
// A fake Vault stands in for the narrow vaultKV surface.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/registration"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// -------------------------------------------------------------------------
// FAKE VAULT
// -------------------------------------------------------------------------

// fakeVault implements vaultKV against in-memory maps and records writes.
type fakeVault struct {
	kv       map[string]map[string]any
	fields   map[string]string
	writes   map[string]map[string]any
	readErr  error
	maybeErr error
	fieldErr error
	writeErr error
}

func newFakeVault() *fakeVault {
	return &fakeVault{
		kv:     map[string]map[string]any{},
		fields: map[string]string{},
		writes: map[string]map[string]any{},
	}
}

func (f *fakeVault) ReadKV(_ context.Context, path string) (map[string]any, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	d, ok := f.kv[path]
	if !ok {
		return nil, errors.New("secret not found")
	}
	return d, nil
}

func (f *fakeVault) ReadKVMaybe(_ context.Context, path string) (map[string]any, bool, error) {
	if f.maybeErr != nil {
		return nil, false, f.maybeErr
	}
	d, ok := f.kv[path]
	return d, ok, nil
}

func (f *fakeVault) ReadKVField(_ context.Context, path, field string) (string, error) {
	if f.fieldErr != nil {
		return "", f.fieldErr
	}
	return f.fields[path+"|"+field], nil
}

func (f *fakeVault) WriteKV(_ context.Context, path string, data map[string]any) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes[path] = data
	return nil
}

// -------------------------------------------------------------------------
// ACME ACCOUNT
// -------------------------------------------------------------------------

func TestLoadAccount_NotFound(t *testing.T) {
	acts := New(Config{Vault: newFakeVault()})

	user, err := acts.loadAccount(context.Background())
	if err != nil {
		t.Fatalf("loadAccount: %v", err)
	}
	if user != nil {
		t.Errorf("user = %v, want nil for a missing account", user)
	}
}

func TestLoadAccount_VaultError(t *testing.T) {
	fake := newFakeVault()
	fake.maybeErr = errors.New("vault unreachable")
	acts := New(Config{Vault: fake})

	if _, err := acts.loadAccount(context.Background()); err == nil {
		t.Fatal("loadAccount must surface a real Vault error, not read it as a missing account")
	}
}

func TestLoadAccount_Existing(t *testing.T) {
	fake := newFakeVault()
	acts := New(Config{Vault: fake})

	key, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	acc := persistedAccount{
		Email:        "ops@munchbox.cc",
		KeyPEM:       string(certcrypto.PEMEncode(key)),
		Registration: &registration.Resource{URI: "https://acme.test/acct/1"},
	}
	body, _ := json.Marshal(acc)
	fake.kv[acts.cfg.AccountPath] = map[string]any{"account": string(body)}

	user, err := acts.loadAccount(context.Background())
	if err != nil {
		t.Fatalf("loadAccount: %v", err)
	}
	if user == nil {
		t.Fatal("user = nil, want loaded account")
	}
	if user.email != "ops@munchbox.cc" {
		t.Errorf("email = %q, want ops@munchbox.cc", user.email)
	}
	if user.GetPrivateKey() == nil {
		t.Error("account key did not round-trip")
	}
	if user.registration == nil || user.registration.URI != "https://acme.test/acct/1" {
		t.Errorf("registration = %v, want URI https://acme.test/acct/1", user.registration)
	}
}

func TestLoadAccount_BadJSON(t *testing.T) {
	fake := newFakeVault()
	acts := New(Config{Vault: fake})
	fake.kv[acts.cfg.AccountPath] = map[string]any{"account": "not-json"}

	if _, err := acts.loadAccount(context.Background()); err == nil {
		t.Fatal("loadAccount must error on undecodable account JSON")
	}
}

func TestEnsureUser_NewAccount(t *testing.T) {
	acts := New(Config{Vault: newFakeVault()})

	user, isNew, err := acts.ensureUser(context.Background(), "ops@munchbox.cc")
	if err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
	if !isNew {
		t.Error("isNew = false, want true for a missing account")
	}
	if user == nil || user.email != "ops@munchbox.cc" || user.GetPrivateKey() == nil {
		t.Errorf("user = %+v, want a fresh account with the requested email and a key", user)
	}
}

func TestEnsureUser_ExistingAccount(t *testing.T) {
	fake := newFakeVault()
	acts := New(Config{Vault: fake})

	key, _ := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	body, _ := json.Marshal(persistedAccount{Email: "ops@munchbox.cc", KeyPEM: string(certcrypto.PEMEncode(key))})
	fake.kv[acts.cfg.AccountPath] = map[string]any{"account": string(body)}

	user, isNew, err := acts.ensureUser(context.Background(), "someone-else@munchbox.cc")
	if err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
	if isNew {
		t.Error("isNew = true, want false when an account already exists")
	}
	if user.email != "ops@munchbox.cc" {
		t.Errorf("email = %q, want the persisted account's email", user.email)
	}
}

func TestEnsureUser_VaultError(t *testing.T) {
	fake := newFakeVault()
	fake.maybeErr = errors.New("vault unreachable")
	acts := New(Config{Vault: fake})

	if _, _, err := acts.ensureUser(context.Background(), "ops@munchbox.cc"); err == nil {
		t.Fatal("ensureUser must surface a Vault error rather than generating a new account")
	}
}

func TestSaveAccount_RoundTrip(t *testing.T) {
	fake := newFakeVault()
	acts := New(Config{Vault: fake})

	key, _ := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	user := &acmeUser{
		email:        "ops@munchbox.cc",
		key:          key,
		registration: &registration.Resource{URI: "https://acme.test/acct/9"},
	}
	if err := acts.saveAccount(context.Background(), user); err != nil {
		t.Fatalf("saveAccount: %v", err)
	}

	written, ok := fake.writes[acts.cfg.AccountPath]
	if !ok {
		t.Fatalf("nothing written to %s", acts.cfg.AccountPath)
	}
	raw, _ := written["account"].(string)
	var acc persistedAccount
	if err := json.Unmarshal([]byte(raw), &acc); err != nil {
		t.Fatalf("persisted account is not valid JSON: %v", err)
	}
	if acc.Email != "ops@munchbox.cc" || acc.Registration.URI != "https://acme.test/acct/9" {
		t.Errorf("persisted account = %+v, want email/URI preserved", acc)
	}
	if _, err := certcrypto.ParsePEMPrivateKey([]byte(acc.KeyPEM)); err != nil {
		t.Errorf("persisted key not parseable: %v", err)
	}
}

// -------------------------------------------------------------------------
// PUBLISH
// -------------------------------------------------------------------------

func TestPublishWildcardCert_PromotesStaging(t *testing.T) {
	fake := newFakeVault()
	acts := New(Config{Vault: fake})
	fake.kv[acts.cfg.StagingPath] = map[string]any{"cert": "CERTPEM", "key": "KEYPEM"}

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(acts)
	if _, err := env.ExecuteActivity(acts.PublishWildcardCert); err != nil {
		t.Fatalf("PublishWildcardCert: %v", err)
	}

	got := fake.writes[acts.cfg.PublishPath]
	if got["cert"] != "CERTPEM" || got["key"] != "KEYPEM" {
		t.Errorf("published = %v, want the staged cert+key", got)
	}
}

func TestPublishWildcardCert_MissingStaging(t *testing.T) {
	fake := newFakeVault()
	acts := New(Config{Vault: fake})
	fake.kv[acts.cfg.StagingPath] = map[string]any{"cert": "", "key": ""}

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(acts)
	_, err := env.ExecuteActivity(acts.PublishWildcardCert)
	if err == nil {
		t.Fatal("want error when staged cert/key are empty")
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || appErr.Type() != "StagedCertMissing" {
		t.Errorf("error = %v, want non-retryable StagedCertMissing", err)
	}
}

// -------------------------------------------------------------------------
// CLASSIFICATION
// -------------------------------------------------------------------------

func TestClassifyACME(t *testing.T) {
	rl := classifyACME(errors.New("acme: error 429 - rate limit exceeded for new orders"))
	var appErr *temporal.ApplicationError
	if !errors.As(rl, &appErr) || appErr.Type() != errRateLimited {
		t.Errorf("rate-limit error = %v, want non-retryable %s", rl, errRateLimited)
	}

	plain := errors.New("dns propagation timeout")
	if out := classifyACME(plain); !errors.Is(out, plain) {
		t.Errorf("non-rate-limit error should pass through retryable, got %v", out)
	}
	if classifyACME(nil) != nil {
		t.Error("classifyACME(nil) must be nil")
	}
}

func TestIsRateLimit(t *testing.T) {
	if !isRateLimit(errors.New("Error 429: rateLimited")) {
		t.Error("expected rateLimited string to classify as rate limit")
	}
	if isRateLimit(errors.New("connection refused")) {
		t.Error("connection refused should not classify as rate limit")
	}
}
