# -------------------------------------------------------------------------------
# Trivy Scan Worker - Container Image
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Builds the trivy-scan-worker binary and packages it with the Trivy CLI for
# vulnerability scanning. Runs as a Temporal worker listening on the
# trivy-task-queue.
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
COPY trivyscan/ trivyscan/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o trivy-scan-worker ./trivyscan/worker

# -------------------------------------------------------------------------
# STAGE 2: RUNTIME
# -------------------------------------------------------------------------

FROM alpine:3.21

RUN apk add --no-cache ca-certificates curl procps

# --- Install Trivy CLI ---
ARG TRIVY_VERSION=0.69.3
RUN ARCH=$(uname -m) \
    && if [ "$ARCH" = "aarch64" ]; then TRIVY_ARCH="ARM64"; else TRIVY_ARCH="64bit"; fi \
    && curl -fsSL https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${TRIVY_ARCH}.tar.gz -o trivy.tar.gz \
    && tar -xzf trivy.tar.gz \
    && mv trivy /usr/local/bin/ \
    && rm -rf trivy.tar.gz LICENSE README.md contrib

COPY --from=builder /build/trivy-scan-worker /usr/local/bin/

ENTRYPOINT ["trivy-scan-worker"]
