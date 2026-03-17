# -------------------------------------------------------------------------------
# Trivy Scan Worker - Build and Push
#
# Project: Nomad Temporal Jobs / Author: Alex Freidah
#
# Builds and pushes the trivy-scan-worker container image. The build context
# is the parent temporal-workers directory so shared packages are available.
# -------------------------------------------------------------------------------

REGISTRY   ?= registry.munchbox.cc
IMAGE      := trivy-scan-worker
VERSION    ?= $(shell cat .version 2>/dev/null || echo latest)

FULL_TAG   := $(REGISTRY)/$(IMAGE):$(VERSION)
CACHE_TAG  := $(REGISTRY)/$(IMAGE):cache
PLATFORMS  := linux/amd64,linux/arm64
BUILD_CTX  := ..

help: ## Display available Make targets
	@echo ""
	@echo "Available targets:"
	@echo ""
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' Makefile | \
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
	  --cache-from type=registry,ref=$(CACHE_TAG) \
	  --cache-to type=registry,ref=$(CACHE_TAG),mode=max \
	  --output type=image,push=true,registry.insecure=true \
	  $(BUILD_CTX)

test: ## Run Go tests
	cd $(BUILD_CTX) && go test ./trivyscan/... ./shared/...

lint: ## Run Go linter
	cd $(BUILD_CTX) && go vet ./trivyscan/... ./shared/...

.PHONY: help builder build push test lint
.DEFAULT_GOAL := help
