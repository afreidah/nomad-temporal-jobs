// Package sonarcloud is a thin native HTTP client over the handful of SonarCloud
// (SonarQube Cloud) user-token endpoints there is no Go SDK for: mint a token,
// revoke one, and list the authenticated user's token names. A master user token
// held in Vault authenticates every call; the token-renewer worker uses it to
// mint a fresh token per managed repo and write it to that repo's SONAR_TOKEN
// secret.
package sonarcloud
