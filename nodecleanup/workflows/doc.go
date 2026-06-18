// Package workflows holds the cleanup worker's four independent
// infrastructure-maintenance workflows, each on its own schedule and sharing
// the cleanup-task-queue: Cleanup (orphaned Nomad data-directory removal
// across client nodes), RegistryGC, AptlyCleanup, and PostgresMaintenance.
// RegistryGC and AptlyCleanup are sagas -- scale a job down, do the work,
// then scale back via deferred compensation. Pure orchestration -- all I/O
// happens in activities.
package workflows
