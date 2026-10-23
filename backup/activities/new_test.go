// -------------------------------------------------------------------------------
// Backup Activities - Constructor Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// New wires only lazy clients (Nomad/S3/Postgres/Consul construct without any
// network I/O), so the valid, invalid-config, and bad-client-address paths are
// all unit-coverable.
// -------------------------------------------------------------------------------

package activities

import "testing"

func TestNewActivities(t *testing.T) {
	a, err := New(Config{
		S3Endpoint:  "https://s3.example.com",
		S3Bucket:    "backups",
		S3AccessKey: "AK",
		S3SecretKey: "SK",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil || a.store == nil || a.nomad == nil || a.pg == nil || a.consul == nil {
		t.Fatal("New returned incomplete Activities")
	}
}

func TestNewActivities_InvalidConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected an error for missing S3 config")
	}
}

func validConfig() Config {
	return Config{
		S3Endpoint:  "https://s3.example.com",
		S3Bucket:    "backups",
		S3AccessKey: "AK",
		S3SecretKey: "SK",
	}
}

// An unparseable NOMAD_ADDR fails Nomad client construction.
func TestNewActivities_BadNomadAddr(t *testing.T) {
	t.Setenv("NOMAD_ADDR", "http://[::1")
	if _, err := New(validConfig()); err == nil {
		t.Fatal("expected an error for an invalid NOMAD_ADDR")
	}
}

// An unknown CONSUL_HTTP_ADDR scheme fails Consul client construction.
func TestNewActivities_BadConsulAddr(t *testing.T) {
	t.Setenv("CONSUL_HTTP_ADDR", "ftp://consul.example.com")
	if _, err := New(validConfig()); err == nil {
		t.Fatal("expected an error for an unknown CONSUL_HTTP_ADDR scheme")
	}
}
