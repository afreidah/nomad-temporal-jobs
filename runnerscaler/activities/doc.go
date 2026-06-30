// Package activities implements the runner-scaler activities: read the watched
// repo list and runner profiles from Consul KV, discover queued self-hosted
// Actions jobs on GitHub, and dispatch (and reap) one ephemeral Nomad ci-runner
// per job. The registration token is minted inside the dispatch activity so it
// never transits Temporal workflow history. GitHub, Consul, and Nomad are
// reached through narrow consumer interfaces satisfied by the shared clients.
package activities
