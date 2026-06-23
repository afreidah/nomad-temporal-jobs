// -------------------------------------------------------------------------------
// Shared - NewVaultWithRefresher Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// With a token from VAULT_TOKEN (not a file), the refresher goroutine early-
// returns, so the constructor's happy path is unit-coverable without a server.
// t.Context() cancels at test cleanup, stopping the goroutine.
// -------------------------------------------------------------------------------

package shared

import (
	"log/slog"
	"testing"
)

func TestNewVaultWithRefresher(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")

	vc, err := NewVaultWithRefresher(t.Context(), slog.Default())
	if err != nil {
		t.Fatalf("NewVaultWithRefresher: %v", err)
	}
	if vc == nil {
		t.Fatal("NewVaultWithRefresher returned nil")
	}
}
