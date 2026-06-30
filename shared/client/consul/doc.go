// Package consul is an instrumented Consul API client whose ACL token is
// fetched from Vault through the shared Vault client, so a worker carries no
// static Consul credentials. Address and TLS come from the standard CONSUL_*
// environment variables (default: the node's local agent), and the HTTP
// transport is OTel-instrumented so Consul calls appear in the Tempo service
// graph.
package consul
