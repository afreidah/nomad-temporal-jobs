// Package registrygc implements the saga-style Docker registry
// garbage-collection workflow and its registry-specific activity. The generic
// find / scale / wait / measure saga steps it orchestrates live in the shared
// nodes package; this package adds the long-running garbage-collect run, the
// blob-count parser, and the config/result types. The saga scales the registry
// offline, garbage-collects, and always scales it back via deferred
// compensation.
package registrygc
