# -------------------------------------------------------------------------------
# Backup Worker - Container Image
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Builds the backup-worker binary. Nomad and Consul snapshots are taken
# through their Go APIs, so only the PostgreSQL 18 client (pg_dump/pg_dumpall,
# which have no Go-native equivalent) is bundled. Uses Debian for access to
# the official PostgreSQL apt repo.
# -------------------------------------------------------------------------------

# -------------------------------------------------------------------------
# STAGE 1: BUILD
# -------------------------------------------------------------------------

FROM golang:1.26.4-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY shared/ shared/
COPY backup/ backup/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o backup-worker ./backup/worker

# -------------------------------------------------------------------------
# STAGE 2: RUNTIME
# -------------------------------------------------------------------------

FROM debian:bookworm-slim

# ca-certificates + the PostgreSQL 18 client (matches the server version;
# pg_dump/pg_dumpall have no Go-native equivalent). curl/gnupg are pulled only
# to add the pgdg apt repo and purged in the same layer so they don't ship.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gnupg \
    && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | gpg --dearmor -o /usr/share/keyrings/postgresql-keyring.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/postgresql-keyring.gpg] http://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" > /etc/apt/sources.list.d/pgdg.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends postgresql-client-18 \
    && apt-get purge -y curl gnupg \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/backup-worker /usr/local/bin/

# Pre-create backup directories (mounted as volumes at runtime)
RUN mkdir -p /mnt/gdrive/nomad-snapshots \
    /mnt/gdrive/consul-snapshots \
    /mnt/gdrive/postgres-backups \
    /mnt/gdrive/registry-backups

ENTRYPOINT ["backup-worker"]
