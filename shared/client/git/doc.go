// Package git is a GitHub App client. It authenticates as the App (a JWT signed
// with the App private key) to mint short-lived, repo-scoped installation
// tokens and to write repository Actions secrets (NaCl sealed boxes, the format
// GitHub requires). The HTTP transport is OTel-instrumented so GitHub calls
// appear in the Tempo service graph.
package git
