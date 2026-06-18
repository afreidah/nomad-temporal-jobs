# -------------------------------------------------------------------------------
# Shared Worker Build Rules
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Common build/push/test/lint targets for every worker domain. A domain
# Makefile sets IMAGE (the registry image name), PKG (the domain's Go package
# directory), and RUNTIME_TARGET (the runtime profile stage in the root
# Dockerfile), then includes this file. Optionally it sets BUILD_ARGS for extra
# --build-arg flags (e.g. TRIVY_VERSION). The image tag is owned by the domain's
# own .version; the build context is the repo root so the shared packages and
# the single Dockerfile are available.
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

# --- image tag is owned by this dir's .version; never inherited from env ---
VERSION := $(strip $(shell cat .version 2>/dev/null))
ifeq ($(VERSION),)
$(error .version missing in $(CURDIR) -- each worker owns its own image tag)
endif

FULL_TAG   := $(REGISTRY)/$(IMAGE):$(VERSION)
# amd64 only for now: our Nomad clients are amd64, so we skip the emulated
# arm64 leg entirely. Re-add linux/arm64 here when arm clients land -- the
# builder already cross-compiles, so there's no emulation penalty.
PLATFORMS  := linux/amd64
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
	docker build $(BUILD_FLAGS) -t $(FULL_TAG) -f $(DOCKERFILE) $(BUILD_CTX)

push: builder ## Build and push image to registry
	docker buildx build \
	  --platform $(PLATFORMS) \
	  $(BUILD_FLAGS) \
	  -f $(DOCKERFILE) \
	  -t $(FULL_TAG) \
	  --output type=image,push=true,registry.insecure=true \
	  $(BUILD_CTX)

test: ## Run Go tests
	cd $(BUILD_CTX) && go test ./$(PKG)/... ./shared/...

lint: ## Run Go linter
	cd $(BUILD_CTX) && go vet ./$(PKG)/... ./shared/...

.PHONY: help builder build push test lint
.DEFAULT_GOAL := help
