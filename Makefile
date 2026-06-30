# -------------------------------------------------------------------------------
# Nomad Temporal Jobs - Root Build Targets
#
# Author: Alex Freidah
#
# Aggregates build, test, and lint targets across all workflow domains. Each
# domain also has its own Makefile for independent image builds.
# -------------------------------------------------------------------------------

help: ## Display available Make targets
	@echo ""
	@echo "Available targets:"
	@echo ""
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' Makefile | \
		awk 'BEGIN {FS = ":.*?## "} {printf "  %-20s %s\n", $$1, $$2}'
	@echo ""

build: ## Build all packages
	go build ./...

test: ## Run all tests
	go test -race ./...

vet: ## Run Go vet
	go vet ./...

lint: ## Run golangci-lint
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.1 run ./...

govulncheck: ## Scan for known vulnerabilities
	govulncheck ./...

# -------------------------------------------------------------------------
# Every image (workers + web) tags from git describe via _common.mk -- there
# are no .version files to bump. Release tags are computed from conventional
# commits by svu (see the release target).
# -------------------------------------------------------------------------

push-backup: ## Build and push backup-worker image
	cd backup && $(MAKE) push

push-trivy: ## Build and push trivy-scan-worker image
	cd trivyscan && $(MAKE) push

push-cleanup: ## Build and push cleanup-worker image
	cd maintenance && $(MAKE) push

push-cert: ## Build and push cert-acquirer-worker image
	cd certacquirer && $(MAKE) push

push-ghtokens: ## Build and push github-token-renewer image
	cd ghtokenrenewer && $(MAKE) push

push-runnerscaler: ## Build and push ci-runner-scaler image
	cd runnerscaler && $(MAKE) push

push-all: push-backup push-trivy push-cleanup push-cert push-ghtokens push-runnerscaler ## Build and push all images

# Recurses with -j so the image builds run concurrently. They share one
# buildx builder + BuildKit cache mounts, so the Go-compile phases partly
# serialize (cache-mount locks); the registry pushes, runtime stages, and the
# emulated arm64 legs overlap. Net speedup, not a clean linear scale.
push-all-parallel: ## Build and push all images concurrently (make -j)
	$(MAKE) -j6 push-backup push-trivy push-cleanup push-cert push-ghtokens push-runnerscaler

changelog: ## Generate CHANGELOG.md from git history
	git cliff -o CHANGELOG.md

SVU := go run github.com/caarlos0/svu/v3@latest

release: ## Compute the next version from commits, tag, and push to trigger a Release
	@next=$$($(SVU) next) && \
		echo "Releasing $$next (current: $$($(SVU) current))" && \
		git tag "$$next" && \
		git push origin "$$next"

# -------------------------------------------------------------------------
# WEBSITE
# -------------------------------------------------------------------------

REGISTRY   ?= registry.munchbox.cc
WEB_IMAGE  := $(REGISTRY)/temporal-workers-web
WEB_TAG    ?= $(shell git describe --tags --always --dirty)
PLATFORMS  ?= linux/amd64,linux/arm64

web-tools: ## Install Hugo and gomarkdoc for local website development
	go install github.com/gohugoio/hugo@latest
	go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest

web-godoc: ## Generate Go API reference markdown for the website
	@sh web/gen-godoc.sh web/content/godoc

web-serve: web-godoc ## Serve the project website locally
	cd web && hugo serve

web-build: web-godoc ## Build the project website
	cd web && hugo --minify

web-docker: ## Build website Docker image for local architecture
	docker build --pull -f web/Dockerfile -t $(WEB_IMAGE):$(WEB_TAG) -t $(WEB_IMAGE):latest .

builder: ## Ensure the Buildx builder exists
	@docker buildx inspect munchbox-builder >/dev/null 2>&1 || \
		docker buildx create --name munchbox-builder --driver-opt network=host --use
	@docker buildx inspect --bootstrap

web-push: builder ## Build and push multi-arch website image to registry
	docker buildx build \
	  --pull \
	  --platform $(PLATFORMS) \
	  -f web/Dockerfile \
	  -t $(WEB_IMAGE):$(WEB_TAG) \
	  -t $(WEB_IMAGE):latest \
	  --output type=image,push=true \
	  .

.PHONY: help builder build test vet lint govulncheck push-backup push-trivy push-cleanup push-cert push-ghtokens push-runnerscaler push-all push-all-parallel changelog release web-tools web-godoc web-serve web-build web-docker web-push
.DEFAULT_GOAL := help
