# -------------------------------------------------------------------------------
# Cert Acquirer Worker - Container Image
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Builds the cert-acquirer-worker binary that issues the wildcard certificate
# via ACME DNS-01 and publishes it to Vault. Pure Go (lego library, no CLI
# tools), so the runtime stage is distroless static: no shell or package
# manager, runs as a non-root user, and carries only the CA bundle the worker
# needs for outbound TLS to Let's Encrypt, Cloudflare, Vault, and Temporal.
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
COPY certacquirer/ certacquirer/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o cert-acquirer-worker ./certacquirer/worker

# -------------------------------------------------------------------------
# STAGE 2: RUNTIME
# -------------------------------------------------------------------------

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /build/cert-acquirer-worker /usr/local/bin/

# --- explicit non-root (the :nonroot tag already sets uid 65532; stated here
#     so it is visible to readers and static scanners) ---
USER nonroot:nonroot

ENTRYPOINT ["cert-acquirer-worker"]
