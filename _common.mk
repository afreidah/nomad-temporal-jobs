# -------------------------------------------------------------------------------
# Shared Worker Build Rules
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Common build/push/test/lint targets for every worker domain. A domain
# Makefile sets IMAGE (the registry image name) and PKG (the domain's Go
# package directory) and then includes this file. The image tag is owned by
# the domain's own .version; the build context is the parent directory so the
# shared packages are available.
# -------------------------------------------------------------------------------

REGISTRY ?= registry.munchbox.cc

ifeq ($(IMAGE),)
$(error IMAGE not set -- set it in the domain Makefile before including _common.mk)
endif
ifeq ($(PKG),)
$(error PKG not set -- set it in the domain Makefile before including _common.mk)
endif

# --- image tag is owned by this dir's .version; never inherited from env ---
VERSION := $(strip $(shell cat .version 2>/dev/null))
ifeq ($(VERSION),)
$(error .version missing in $(CURDIR) -- each worker owns its own image tag)
endif

FULL_TAG  := $(REGISTRY)/$(IMAGE):$(VERSION)
PLATFORMS := linux/amd64,linux/arm64
BUILD_CTX := ..

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
	docker build -t $(FULL_TAG) -f Dockerfile $(BUILD_CTX)

push: builder ## Build and push multi-arch images to registry
	docker buildx build \
	  --platform $(PLATFORMS) \
	  -f Dockerfile \
	  -t $(FULL_TAG) \
	  --output type=image,push=true,registry.insecure=true \
	  $(BUILD_CTX)

test: ## Run Go tests
	cd $(BUILD_CTX) && go test ./$(PKG)/... ./shared/...

lint: ## Run Go linter
	cd $(BUILD_CTX) && go vet ./$(PKG)/... ./shared/...

.PHONY: help builder build push test lint
.DEFAULT_GOAL := help
