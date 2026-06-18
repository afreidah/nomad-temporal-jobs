# -------------------------------------------------------------------------------
# Trivy Scan Worker - Build and Push
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Sets this worker's image name, package, and runtime profile, then includes
# the shared build rules in ../_common.mk.
# -------------------------------------------------------------------------------

IMAGE          := trivy-scan-worker
PKG            := trivyscan
RUNTIME_TARGET := runtime-trivy
BUILD_ARGS     := --build-arg TRIVY_VERSION=0.69.3

include ../_common.mk
