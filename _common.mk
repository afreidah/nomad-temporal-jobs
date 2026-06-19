# -------------------------------------------------------------------------------
# Shared Worker Build Rules
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Common build/push/test/lint targets for every worker domain. A domain
# Makefile sets IMAGE (the registry image name), PKG (the domain's Go package
# directory), and RUNTIME_TARGET (the runtime profile stage in the root
# Dockerfile), then includes this file. Optionally it sets BUILD_ARGS for extra
# --build-arg flags (e.g. TRIVY_VERSION). The image tag is derived from git
# (git describe), so there is nothing to hand-bump; the build context is the
# repo root so the shared packages and the single Dockerfile are available.
# -------------------------------------------------------------------------------

REGISTRY ?= registry.munchbox.cc

ifeq ($(IMAGE),)
$(error IMAGE not set -- set it in the domain Makefile before including _common.mk)
endif
ifeq ($(PKG),)
$(error PKG not set -- set it in the domain Makefile before including _common.mk)
endif
ifeq ($(RUNTIME_TARGET),)
$(error RUNTIME_TARGET not set -- set it in the domain Makefile before including _common.mk)
endif

# --- image tag derived from git; computed from the nearest tag + commit, so
# --- no manual bump is ever needed. Override with VERSION=... if necessary.
VERSION ?= $(strip $(shell git describe --tags --always --dirty 2>/dev/null))
ifeq ($(VERSION),)
$(error git describe produced no version -- is this a git checkout with tags?)
endif

FULL_TAG   := $(REGISTRY)/$(IMAGE):$(VERSION)
# Nomad jobs pin `:latest`; the git-describe tag is the immutable, traceable
# alias for the exact same image. Both are pushed together.
LATEST_TAG := $(REGISTRY)/$(IMAGE):latest
# Every image is multi-arch (our Nomad clients are a mix of amd64 and arm64).
# The Go build cross-compiles via $BUILDPLATFORM/$TARGETARCH, so it never
# emulates. Pure-Go distroless profiles (cleanup, cert) assemble both arches
# for free; backup (apt) and trivy (apk) have RUN steps in their runtime stage,
# so their arm64 leg emulates under QEMU -- the build host needs binfmt
# registered (one-time: `docker run --privileged --rm tonistiigi/binfmt --install arm64`).
# Override per worker (set PLATFORMS before the include) to scope down if needed.
PLATFORMS  ?= linux/amd64,linux/arm64
BUILD_CTX  := ..
DOCKERFILE := $(BUILD_CTX)/Dockerfile

# Flags shared by build and push: select the runtime profile and the worker's
# Go package. BUILD_ARGS (optional, set by the domain Makefile) is appended.
BUILD_FLAGS := --target $(RUNTIME_TARGET) --build-arg PKG=$(PKG) $(BUILD_ARGS)

help: ## Display available Make targets
	@echo ""
	@echo "Available targets:"
	@echo ""
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "} {printf "  %-20s %s\n", $$1, $$2}'
	@echo ""

builder: ## Ensure the Buildx builder exists
	@docker buildx inspect munchbox-builder >/dev/null 2>&1 || \
		docker buildx create --name munchbox-builder --driver-opt network=host --use
	@docker buildx inspect --bootstrap

build: ## Build for local architecture
	docker build $(BUILD_FLAGS) -t $(FULL_TAG) -t $(LATEST_TAG) -f $(DOCKERFILE) $(BUILD_CTX)

push: builder ## Build and push image to registry
	docker buildx build \
	  --platform $(PLATFORMS) \
	  $(BUILD_FLAGS) \
	  -f $(DOCKERFILE) \
	  -t $(FULL_TAG) \
	  -t $(LATEST_TAG) \
	  --output type=image,push=true,registry.insecure=true \
	  $(BUILD_CTX)

test: ## Run Go tests
	cd $(BUILD_CTX) && go test ./$(PKG)/... ./shared/...

lint: ## Run Go linter
	cd $(BUILD_CTX) && go vet ./$(PKG)/... ./shared/...

.PHONY: help builder build push test lint
.DEFAULT_GOAL := help
