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

.PHONY: help build test vet lint govulncheck push-backup push-trivy push-cleanup push-all
.DEFAULT_GOAL := help
