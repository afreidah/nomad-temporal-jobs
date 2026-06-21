// -------------------------------------------------------------------------------
// Cert Acquirer - ACME User Accessor Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers the lego registration.User accessors on acmeUser.
// -------------------------------------------------------------------------------

package activities

import (
	"testing"

	"github.com/go-acme/lego/v4/registration"
)

func TestAcmeUserAccessors(t *testing.T) {
	reg := &registration.Resource{}
	u := &acmeUser{email: "ops@example.com", registration: reg}

	if u.GetEmail() != "ops@example.com" {
		t.Errorf("GetEmail = %q, want ops@example.com", u.GetEmail())
	}
	if u.GetRegistration() != reg {
		t.Error("GetRegistration should return the stored registration resource")
	}
	if u.GetPrivateKey() != nil {
		t.Error("GetPrivateKey should be nil when no key is set")
	}
}
