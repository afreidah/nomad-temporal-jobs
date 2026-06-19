// Package nodes holds the node-, job-, and saga-level primitives shared across
// the maintenance worker's workflows: the NodeInfo descriptor, the SSH target
// helper, the HumanBytes formatter, and the generic find / scale / wait /
// measure saga activities that the registry-GC and aptly-cleanup sagas both
// build on. Keeping these in one place is what lets those two sagas share an
// identical scale-down / do-work / scale-back skeleton.
package nodes
