// Package vault creates a Vault API client authenticated with the worker's
// Nomad Workload Identity token, so a worker carries only its identity and no
// static service tokens. The other shared clients pull their own credentials
// through it, a background refresher reloads the token as Nomad rotates it, and
// the HTTP transport is OTel-instrumented so Vault calls appear in the Tempo
// service graph.
package vault
