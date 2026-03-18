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

push-backup: ## Build and push backup-worker image
	cd backup && $(MAKE) push

push-trivy: ## Build and push trivy-scan-worker image
	cd trivyscan && $(MAKE) push

push-cleanup: ## Build and push cleanup-worker image
	cd nodecleanup && $(MAKE) push

push-all: push-backup push-trivy push-cleanup ## Build and push all images

changelog: ## Generate CHANGELOG.md from git history
	git cliff -o CHANGELOG.md

release: ## Tag and push to trigger a GitHub Release
	git tag $$(cat .version)
	git push origin $$(cat .version)

# -------------------------------------------------------------------------
# WEBSITE
# -------------------------------------------------------------------------

REGISTRY   ?= registry.munchbox.cc
WEB_IMAGE  := $(REGISTRY)/temporal-workers-web
WEB_TAG    ?= $(shell cat .version)
PLATFORMS  ?= linux/amd64,linux/arm64

GODOC_PKGS := shared:./shared \
              backup-activities:./backup/activities \
              backup-workflows:./backup/workflows \
              trivyscan-activities:./trivyscan/activities \
              trivyscan-workflows:./trivyscan/workflows \
              nodecleanup-activities:./nodecleanup/activities \
              nodecleanup-workflows:./nodecleanup/workflows

web-tools: ## Install Hugo and gomarkdoc for local website development
	go install github.com/gohugoio/hugo@latest
	go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest

web-godoc: ## Generate Go API reference markdown for the website
	@mkdir -p web/content/godoc
	@for entry in $(GODOC_PKGS); do \
		name=$${entry%%:*}; pkg=$${entry#*:}; \
		echo "  godoc: $$pkg"; \
		printf -- '---\ntitle: "%s"\n---\n\n' "$$name" > web/content/godoc/$$name.md; \
		gomarkdoc $$pkg >> web/content/godoc/$$name.md; \
		sed -i '0,/^# /{/^# /d}' web/content/godoc/$$name.md; \
	done

web-serve: web-godoc ## Serve the project website locally
	cd web && hugo serve

web-build: web-godoc ## Build the project website
	cd web && hugo --minify

web-docker: ## Build website Docker image for local architecture
	docker build --pull -f web/Dockerfile -t $(WEB_IMAGE):$(WEB_TAG) .

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
	  --output type=image,push=true \
	  .

.PHONY: help builder build test vet lint govulncheck push-backup push-trivy push-cleanup push-all changelog release web-tools web-godoc web-serve web-build web-docker web-push
.DEFAULT_GOAL := help
