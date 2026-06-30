// -------------------------------------------------------------------------------
// Shared GitHub Client - Repo List Helpers
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Parsing helpers shared by the workers that read an owner/repo watch list from
// Consul KV (the token renewer and the runner scaler), kept here so the parse
// logic lives once rather than in each worker's activities.
// -------------------------------------------------------------------------------

package git

import "strings"

// ParseRepoList splits a newline-separated owner/repo list, dropping blank lines
// and # comments and trimming surrounding whitespace.
func ParseRepoList(s string) []string {
	var repos []string
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		repos = append(repos, line)
	}
	return repos
}

// SplitRepo splits "owner/repo" into its two non-empty parts.
func SplitRepo(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}
