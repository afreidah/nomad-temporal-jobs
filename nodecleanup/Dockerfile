# -------------------------------------------------------------------------------
# Node Cleanup Worker - Container Image
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Builds the cleanup-worker binary. Every remote operation is pure Go now --
# SSH/SFTP via x/crypto + pkg/sftp, Docker via the moby API client, Nomad via
# its API -- so there are no CLI tools to bundle and the runtime stage is
# distroless static: no shell, no package manager, minimal attack surface. Runs
# as root because the worker reads its SSH key/cert from the root-owned mount
# under /root/.ssh.
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
COPY nodecleanup/ nodecleanup/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o cleanup-worker ./nodecleanup/worker

# -------------------------------------------------------------------------
# STAGE 2: RUNTIME
# -------------------------------------------------------------------------

FROM gcr.io/distroless/static-debian12

COPY --from=builder /build/cleanup-worker /usr/local/bin/

ENTRYPOINT ["cleanup-worker"]
