// Package workflows holds the cert-acquirer orchestration: issue the wildcard
// via ACME DNS-01, then publish it to the Vault path Traefik reads. The two
// steps are separate activities with different retry policies -- issuance gets
// few attempts with long backoff (slow DNS-01 propagation, ACME rate limits),
// publish gets fast retries. Pure orchestration -- all I/O happens in activities.
package workflows
