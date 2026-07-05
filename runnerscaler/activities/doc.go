// Package activities implements the runner-scaler activities: read the per-repo
// config from Consul KV, discover queued self-hosted Actions jobs on GitHub
// (through the App installation for app-mode repos, or a PAT from the secret
// store for vault-mode repos), and dispatch (and reap) ephemeral Nomad runners,
// reconciling by (repo, labels) depth across every dispatched job. A
// registration token is minted inside the dispatch activity only for app-mode,
// so it never transits Temporal workflow history; vault-mode dispatches a
// self-registering job with none. GitHub, Consul, Vault, and Nomad are reached
// through narrow consumer interfaces satisfied by the shared clients.
package activities
