# -------------------------------------------------------------------------------
# GitHub Token Renewer Worker - Build and Push
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Sets this worker's image name, package, and runtime profile, then includes
# the shared build rules in ../_common.mk. Pure-Go (Vault/Consul/GitHub over
# HTTP, no SSH or Docker), so it uses the non-root distroless profile and
# assembles both arches without emulation.
# -------------------------------------------------------------------------------

IMAGE          := github-token-renewer
PKG            := ghtokenrenewer
RUNTIME_TARGET := runtime-distroless-nonroot

include ../_common.mk
