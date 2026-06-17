# -------------------------------------------------------------------------------
# Trivy Scan Worker - Container Image
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Builds the trivy-scan-worker binary and the Trivy CLI it shells to. Trivy is
# fetched in the build stage so curl/tar never reach the runtime image, leaving
# a minimal Alpine that carries only the two binaries plus the CA bundle.
# Alpine (not distroless) because the Trivy client needs a writable cache dir
# at runtime.
# -------------------------------------------------------------------------------

# -------------------------------------------------------------------------
# STAGE 1: BUILD
# -------------------------------------------------------------------------

FROM golang:1.26.4-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates curl

COPY go.mod go.sum ./
RUN go mod download

COPY shared/ shared/
COPY trivyscan/ trivyscan/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o trivy-scan-worker ./trivyscan/worker

# --- Fetch the Trivy CLI here so curl/tar stay out of the runtime image ---
ARG TRIVY_VERSION=0.69.3
RUN ARCH=$(uname -m) \
    && if [ "$ARCH" = "aarch64" ]; then TRIVY_ARCH="ARM64"; else TRIVY_ARCH="64bit"; fi \
    && curl -fsSL https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${TRIVY_ARCH}.tar.gz -o /tmp/trivy.tar.gz \
    && tar -xzf /tmp/trivy.tar.gz -C /usr/local/bin trivy

# -------------------------------------------------------------------------
# STAGE 2: RUNTIME
# -------------------------------------------------------------------------

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /usr/local/bin/trivy /usr/local/bin/trivy
COPY --from=builder /build/trivy-scan-worker /usr/local/bin/

ENTRYPOINT ["trivy-scan-worker"]
