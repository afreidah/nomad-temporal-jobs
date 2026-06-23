// -------------------------------------------------------------------------------
// Cert Acquirer - IssueWildcardCert Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives the issuance orchestration against a fake certIssuer (no live ACME),
// covering the new-account register+persist path, the existing-account skip, and
// the register/obtain error paths. The real lego adapter lives in acme.go.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"errors"
	"testing"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/registration"
	"go.temporal.io/sdk/testsuite"
)

// fakeIssuer is a fake certIssuer that records calls and returns canned results.
type fakeIssuer struct {
	registerErr error
	obtainErr   error
	registered  bool
	cert, key   []byte
}

func (f *fakeIssuer) Register(context.Context) (*registration.Resource, error) {
	f.registered = true
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return &registration.Resource{URI: "https://acme.test/acct/1"}, nil
}

func (f *fakeIssuer) Obtain(context.Context, []string) (certPEM, keyPEM []byte, err error) {
	if f.obtainErr != nil {
		return nil, nil, f.obtainErr
	}
	return f.cert, f.key, nil
}

func issueActs(fake *fakeVault, issuer *fakeIssuer) *Activities {
	a := New(Config{Vault: fake})
	a.newIssuer = func(context.Context, *acmeUser) (certIssuer, error) { return issuer, nil }
	return a
}

func runIssue(t *testing.T, a *Activities, req IssueRequest) error {
	t.Helper()
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.IssueWildcardCert)
	_, err := env.ExecuteActivity(a.IssueWildcardCert, req)
	return err
}

func TestIssueWildcardCert_NewAccount(t *testing.T) {
	fake := newFakeVault()
	issuer := &fakeIssuer{cert: []byte("CERTPEM"), key: []byte("KEYPEM")}
	a := issueActs(fake, issuer)

	if err := runIssue(t, a, IssueRequest{Domains: []string{"*.munchbox.cc"}, Email: "a@b.c"}); err != nil {
		t.Fatalf("IssueWildcardCert: %v", err)
	}
	if !issuer.registered {
		t.Error("a new account must be registered with the CA")
	}
	if _, ok := fake.writes[a.cfg.AccountPath]; !ok {
		t.Error("new account was not persisted to Vault")
	}
	staged := fake.writes[a.cfg.StagingPath]
	if staged["cert"] != "CERTPEM" || staged["key"] != "KEYPEM" {
		t.Errorf("staged cert/key = %v, want CERTPEM/KEYPEM", staged)
	}
}

func TestIssueWildcardCert_ExistingAccount(t *testing.T) {
	fake := newFakeVault()
	issuer := &fakeIssuer{cert: []byte("C"), key: []byte("K")}
	a := issueActs(fake, issuer)

	// Seed a persisted account so ensureUser reports isNew=false.
	key, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.saveAccount(context.Background(), &acmeUser{email: "a@b.c", key: key}); err != nil {
		t.Fatal(err)
	}
	fake.kv[a.cfg.AccountPath] = fake.writes[a.cfg.AccountPath]

	if err := runIssue(t, a, IssueRequest{Domains: []string{"*.munchbox.cc"}, Email: "a@b.c"}); err != nil {
		t.Fatalf("IssueWildcardCert: %v", err)
	}
	if issuer.registered {
		t.Error("an existing account must not be re-registered")
	}
}

func TestIssueWildcardCert_RegisterError(t *testing.T) {
	a := issueActs(newFakeVault(), &fakeIssuer{registerErr: errors.New("ACME down")})
	if err := runIssue(t, a, IssueRequest{Domains: []string{"*.munchbox.cc"}, Email: "a@b.c"}); err == nil {
		t.Fatal("expected error when account registration fails")
	}
}

func TestIssueWildcardCert_ObtainError(t *testing.T) {
	fake := newFakeVault()
	issuer := &fakeIssuer{obtainErr: errors.New("challenge failed")}
	a := issueActs(fake, issuer)

	if err := runIssue(t, a, IssueRequest{Domains: []string{"*.munchbox.cc"}, Email: "a@b.c"}); err == nil {
		t.Fatal("expected error when certificate obtain fails")
	}
	if _, ok := fake.writes[a.cfg.StagingPath]; ok {
		t.Error("nothing should be staged when obtain fails")
	}
}
