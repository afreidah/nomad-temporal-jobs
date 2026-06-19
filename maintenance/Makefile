# -------------------------------------------------------------------------------
# Maintenance Worker - Build and Push
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Sets this worker's image name, package, and runtime profile, then includes
# the shared build rules in ../_common.mk. One binary hosts the cleanup,
# registry-GC, aptly-cleanup, and postgres-maintenance workflows.
# -------------------------------------------------------------------------------

IMAGE          := cleanup-worker
PKG            := maintenance
RUNTIME_TARGET := runtime-distroless-root

include ../_common.mk
