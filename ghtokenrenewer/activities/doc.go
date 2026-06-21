// Package activities implements the GitHub token-renewer activities: read the
// managed repo list from Consul KV, and for each repo mint a short-lived GitHub
// App installation token and write it to the repo's Actions secret. Re-minting
// on every scheduled run keeps the secret continuously valid, replacing
// hand-rotated Personal Access Tokens. GitHub and Consul are reached through
// narrow consumer interfaces satisfied by the shared clients.
package activities
