// -------------------------------------------------------------------------------
// Backup Activities - Constructor Test
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// New wires only lazy clients (Nomad/S3/Postgres/Consul construct without any
// network I/O), so both the valid and invalid-config paths are unit-coverable.
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
