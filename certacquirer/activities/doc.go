// Package activities implements the Temporal activities for the cert-acquirer
// worker: issuing the *.munchbox.cc wildcard via ACME DNS-01 (Cloudflare,
// go-acme/lego) and publishing the result to Vault. Issuance and publish are
// separate activities -- the issued cert+key are staged in Vault so a publish
// failure never re-runs ACME issuance (Let's Encrypt rate limits), and the
// private key never transits Temporal workflow history.
package activities
