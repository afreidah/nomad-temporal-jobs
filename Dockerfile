# syntax=docker/dockerfile:1
# -------------------------------------------------------------------------------
# Unified Worker Image Build
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# One shared builder stage, one runtime stage per runtime *profile*. A worker
# selects its profile with --target and its Go package with --build-arg PKG=<dir>
# (both supplied by _common.mk). The compiled binary is always /out/worker, so
# every image shares a uniform `worker` entrypoint.
#
# The builder pins to the host arch ($BUILDPLATFORM) and cross-compiles to the
# requested $TARGETARCH, so multi-arch never emulates the Go toolchain. We
# currently build linux/amd64 only (see PLATFORMS in _common.mk); re-adding
# linux/arm64 there is a one-line change with no emulation penalty.
#
# BuildKit cache mounts persist the module + build cache across builds and
# across all workers, so the std lib, deps, and shared/ compile once for the
# whole fleet rather than once per image.
# -------------------------------------------------------------------------------

# ---- shared builder: native host toolchain, cross-compiles to TARGET arch ----
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS builder
ARG PKG
ARG TARGETOS
ARG TARGETARCH
WORKDIR /build
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY shared/ shared/
COPY ${PKG}/ ${PKG}/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /out/worker "./${PKG}/worker"

# ---- profile: pure-Go, non-root (certacquirer + any future pure-Go worker) ----
FROM gcr.io/distroless/static-debian12:nonroot AS runtime-distroless-nonroot
COPY --from=builder /out/worker /usr/local/bin/worker
USER nonroot:nonroot
ENTRYPOINT ["worker"]

# ---- profile: pure-Go, root (maintenance: reads SSH key from root-owned mount) ----
# Pinned by digest for reproducible builds (bump via renovate/dependabot).
FROM gcr.io/distroless/static-debian12@sha256:9c346e4be81b5ca7ff31a0d89eaeade58b0f95cfd3baed1f36083ddb47ca3160 AS runtime-distroless-root
COPY --from=builder /out/worker /usr/local/bin/worker
ENTRYPOINT ["worker"]

# ---- profile: backup (PostgreSQL 18 client; pg_dump/pg_dumpall have no Go-native
#      equivalent). Debian for access to the official pgdg apt repo. curl/gnupg
#      are pulled only to add the repo and purged in the same layer. ----
FROM debian:bookworm-slim AS runtime-backup
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gnupg \
    && curl -fsSL --proto '=https' --proto-redir '=https' https://www.postgresql.org/media/keys/ACCC4CF8.asc | gpg --dearmor -o /usr/share/keyrings/postgresql-keyring.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/postgresql-keyring.gpg] https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" > /etc/apt/sources.list.d/pgdg.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends postgresql-client-18 \
    && apt-get purge -y curl gnupg \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/worker /usr/local/bin/worker
# Pre-create backup directories (mounted as volumes at runtime).
RUN mkdir -p /mnt/gdrive/nomad-snapshots \
    /mnt/gdrive/consul-snapshots \
    /mnt/gdrive/postgres-backups \
    /mnt/gdrive/registry-backups
ENTRYPOINT ["worker"]

# ---- trivy CLI download, isolated so curl/tar never reach a runtime image.
#      The release tarball is verified against the published SHA256 checksum. ----
FROM alpine:3.21 AS trivy-dl
ARG TRIVY_VERSION
RUN apk add --no-cache curl tar
RUN set -eux; \
    case "$(uname -m)" in aarch64) ta=ARM64;; *) ta=64bit;; esac; \
    f="trivy_${TRIVY_VERSION}_Linux-${ta}.tar.gz"; \
    base="https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}"; \
    cd /tmp; \
    curl -fsSL --proto '=https' --proto-redir '=https' "$base/$f" -o "$f"; \
    curl -fsSL --proto '=https' --proto-redir '=https' "$base/trivy_${TRIVY_VERSION}_checksums.txt" -o sums.txt; \
    grep "Linux-${ta}.tar.gz\$" sums.txt | sha256sum -c -; \
    tar -xzf "$f" -C /usr/local/bin trivy

# ---- profile: trivyscan (trivy CLI; non-root with a writable cache dir) ----
FROM alpine:3.21 AS runtime-trivy
RUN apk add --no-cache ca-certificates && adduser -D -u 65532 worker
COPY --from=trivy-dl /usr/local/bin/trivy /usr/local/bin/trivy
COPY --from=builder  /out/worker          /usr/local/bin/worker
USER worker
ENV HOME=/home/worker TRIVY_CACHE_DIR=/home/worker/.cache/trivy
ENTRYPOINT ["worker"]
