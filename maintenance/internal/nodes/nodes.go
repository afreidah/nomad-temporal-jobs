// -------------------------------------------------------------------------------
// Shared Node Primitives - Descriptor, SSH Target, Byte Formatter
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// The small value types and helpers every maintenance workflow needs: the
// NodeInfo descriptor returned by node/job discovery, the SSH target builder,
// and the human-friendly byte formatter used for before/after/reclaimed sizes.
// -------------------------------------------------------------------------------

package nodes

import (
	"fmt"

	"munchbox/temporal-workers/shared/client/ssh"
)

// NodeInfo contains information about a Nomad client node needed for SSH
// connection and remote maintenance execution.
type NodeInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	HTTPAddr string `json:"http_addr"` // Nomad agent HTTP address (e.g., "10.200.0.11:4646")
	IsOracle bool   `json:"is_oracle"` // Oracle nodes use ubuntu user instead of root
}

// Target builds the SSH target for a node. The worker connects as root
// everywhere -- the Vault SSH CA issues a root principal the oracle hosts
// accept too -- so there is no per-node user or sudo handling, and root reaches
// root-owned data dirs and the docker socket directly.
func Target(node NodeInfo) ssh.SSHTarget {
	return ssh.SSHTarget{Host: node.Address, User: "root"}
}

// HumanBytes renders a byte count in a compact human-friendly form (KiB, MiB,
// GiB) matching the shape of `du -h`. Exported so workflows can format
// before/after/reclaimed sizes consistently.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"KiB", "MiB", "GiB", "TiB"}
	if exp >= len(suffixes) {
		exp = len(suffixes) - 1
	}
	val := float64(n) / float64(div)
	if val >= 100 {
		return fmt.Sprintf("%.0f%s", val, suffixes[exp])
	}
	return fmt.Sprintf("%.1f%s", val, suffixes[exp])
}
