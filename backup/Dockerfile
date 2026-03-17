# -------------------------------------------------------------------------------
# Backup Worker - Container Image
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Builds the backup-worker and workflow-trigger binaries with HashiCorp CLI
# tools (nomad, consul) and PostgreSQL 18 client for snapshot operations.
# Uses Debian for access to the official PostgreSQL apt repo.
# -------------------------------------------------------------------------------

# -------------------------------------------------------------------------
# STAGE 1: BUILD
# -------------------------------------------------------------------------

FROM golang:1.26-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY shared/ shared/
COPY backup/ backup/
COPY trivyscan/ trivyscan/
COPY nodecleanup/ nodecleanup/
COPY cmd/ cmd/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o backup-worker ./backup/worker
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o workflow-trigger ./cmd/trigger

# -------------------------------------------------------------------------
# STAGE 2: RUNTIME
# -------------------------------------------------------------------------

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    bash \
    unzip \
    procps \
    tar \
    gzip \
    gnupg \
    lsb-release \
    && rm -rf /var/lib/apt/lists/*

# --- PostgreSQL 18 client (matches server version) ---
RUN curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | gpg --dearmor -o /usr/share/keyrings/postgresql-keyring.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/postgresql-keyring.gpg] http://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" > /etc/apt/sources.list.d/pgdg.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends postgresql-client-18 \
    && rm -rf /var/lib/apt/lists/*

# --- Install Nomad CLI ---
ARG NOMAD_VERSION=1.11.1
ARG TARGETARCH
RUN curl -fsSL https://releases.hashicorp.com/nomad/${NOMAD_VERSION}/nomad_${NOMAD_VERSION}_linux_${TARGETARCH}.zip -o nomad.zip \
    && unzip -o nomad.zip \
    && mv nomad /usr/local/bin/ \
    && rm -f nomad.zip LICENSE.txt \
    && chmod +x /usr/local/bin/nomad

# --- Install Consul CLI ---
ARG CONSUL_VERSION=1.22.1
RUN curl -fsSL https://releases.hashicorp.com/consul/${CONSUL_VERSION}/consul_${CONSUL_VERSION}_linux_${TARGETARCH}.zip -o consul.zip \
    && unzip -o consul.zip \
    && mv consul /usr/local/bin/ \
    && rm -f consul.zip LICENSE.txt \
    && chmod +x /usr/local/bin/consul

COPY --from=builder /build/backup-worker /usr/local/bin/
COPY --from=builder /build/workflow-trigger /usr/local/bin/

# Pre-create backup directories (mounted as volumes at runtime)
RUN mkdir -p /mnt/gdrive/nomad-snapshots \
    /mnt/gdrive/consul-snapshots \
    /mnt/gdrive/postgres-backups \
    /mnt/gdrive/registry-backups

# SSH directory for cleanup workflow (key mounted at runtime)
RUN mkdir -p /root/.ssh && chmod 700 /root/.ssh

ENTRYPOINT ["backup-worker"]
