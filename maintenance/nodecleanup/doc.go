// Package nodecleanup implements the orphaned-data-directory cleanup workflow
// and its activities: discover every ready Nomad client node, then over
// SSH/SFTP remove job data directories that no longer correspond to a running
// allocation, honoring a grace period and excluding live runtime dirs. An
// optional Docker prune runs through the Docker API tunneled over the same SSH
// connection. All remote work uses native Go clients -- no remote shell.
package nodecleanup
