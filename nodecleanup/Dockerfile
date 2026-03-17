# -------------------------------------------------------------------------------
# Node Cleanup Worker - Container Image
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Builds the cleanup-worker binary for removing orphaned Nomad data
# directories via SSH. Lightweight Alpine image with no CLI tools needed
# since all remote operations execute via SSH on the target nodes.
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
COPY nodecleanup/ nodecleanup/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o cleanup-worker ./nodecleanup/worker

# -------------------------------------------------------------------------
# STAGE 2: RUNTIME
# -------------------------------------------------------------------------

FROM alpine:3.21

RUN apk add --no-cache ca-certificates procps

COPY --from=builder /build/cleanup-worker /usr/local/bin/

ENTRYPOINT ["cleanup-worker"]
