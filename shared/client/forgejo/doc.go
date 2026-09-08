// Package forgejo is the Forgejo Actions surface the runner scaler needs:
// discover queued jobs waiting on a runner, and mint a runner registration
// token so a dispatched runner can register itself and then exit.
//
// Forgejo's Actions API is Gitea-compatible but not GitHub-compatible, so this
// is a small hand-rolled client rather than a reuse of the GitHub one. It
// deliberately exposes the same two methods the scaler's consumer interfaces
// declare, so a Forgejo instance drops into the existing poll-and-dispatch loop
// beside the GitHub App and PAT clients.
package forgejo
