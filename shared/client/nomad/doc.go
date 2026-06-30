// Package nomad is an instrumented Nomad API client exposing the operations
// workers perform: discovering running images and client nodes, finding a job's
// node, scaling jobs, and waiting on allocation counts. The HTTP transport is
// OTel-instrumented so Nomad calls appear in the Tempo service graph.
package nomad
