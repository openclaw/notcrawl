.DEFAULT_GOAL := help

BINARY ?= bin/notcrawl
CLI = $(if $(filter /%,$(BINARY)),$(BINARY),./$(BINARY))
TAG ?=
ASSET_DIR ?= dist/release-assets

.PHONY: help build test test-release run fmt fmt-check deps lint smoke check release-notes release-check snapshot snapshot-release release-snapshot release release-artifacts release-macos verify-release verify-release-macos

help: ## Print available targets.
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the CLI into bin/notcrawl.
	go build -o $(BINARY) ./cmd/notcrawl

test: ## Run the full test suite.
	GOWORK=off go test -count=1 ./...

test-release: ## Test the macOS release scripts.
	./scripts/test-notcrawl-macos-release.sh

run: ## Run the CLI with optional ARGS.
	go run ./cmd/notcrawl $(ARGS)

fmt: ## Format Go sources.
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

fmt-check: ## Check that Go sources are formatted.
	@set -e; changed="$$(gofmt -l .)"; \
	if [ -n "$$changed" ]; then printf 'gofmt wants changes in:\n%s\n' "$$changed"; exit 1; fi

deps: ## Verify module metadata and known vulnerabilities.
	GOWORK=off go mod verify
	GOWORK=off go mod tidy
	git diff --exit-code -- go.mod go.sum
	GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@v1.4.0 ./...

lint: ## Run vet and dead-code analysis.
	GOWORK=off go vet ./...
	@output_file="$$(mktemp)"; trap 'rm -f "$$output_file"' EXIT; \
	if ! GOWORK=off go run golang.org/x/tools/cmd/deadcode@v0.46.0 -test ./... > "$$output_file"; then \
		cat "$$output_file"; exit 1; \
	fi; \
	if [ -s "$$output_file" ]; then cat "$$output_file"; exit 1; fi

smoke: ## Build and smoke-test the CLI control surface.
	GOWORK=off go build -ldflags "-X main.version=ci" -o $(BINARY) ./cmd/notcrawl
	@set -eu; tmp_dir="$$(mktemp -d)"; trap 'rm -rf "$$tmp_dir"' EXIT; \
	output="$$($(CLI) --help 2>&1)"; \
	printf '%s\n' "$$output"; \
	printf '%s' "$$output" | grep -q "Usage of notcrawl:"; \
	printf '%s' "$$output" | grep -q "metadata"; \
	printf '%s' "$$output" | grep -q "tui"; \
	test "$$($(CLI) --version)" = "ci"; \
	output="$$($(CLI) metadata --json)"; \
	printf '%s' "$$output" | grep -q '"schema_version"'; \
	$(CLI) --config "$$tmp_dir/notcrawl.toml" init; \
	output="$$($(CLI) --config "$$tmp_dir/notcrawl.toml" --db "$$tmp_dir/notcrawl.db" status --json)"; \
	printf '%s' "$$output" | grep -q '"databases"'; \
	output="$$($(CLI) --config "$$tmp_dir/notcrawl.toml" --db "$$tmp_dir/notcrawl.db" tui --json --limit 1)"; \
	printf '%s' "$$output" | grep -q '^\['

check: ## Run every local gate enforced by CI.
	$(MAKE) deps
	$(MAKE) fmt-check
	$(MAKE) lint
	$(MAKE) test
	$(MAKE) test-release
	$(MAKE) smoke
	$(MAKE) release-check
	$(MAKE) snapshot
	$(MAKE) snapshot-release

release-notes: ## Generate release notes for TAG=vX.Y.Z.
	@test -n "$(TAG)" || (echo "usage: make release-notes TAG=v0.1.0" >&2; exit 2)
	scripts/release-notes.sh "$(TAG)"

release-check: ## Validate both GoReleaser configurations.
	goreleaser check
	goreleaser check --config .goreleaser-release.yml

snapshot: ## Build credential-free snapshot artifacts without publishing.
	GOWORK=off goreleaser release --snapshot --clean --skip=publish

snapshot-release: ## Test the credential-free official Linux release configuration.
	GOWORK=off goreleaser release --snapshot --clean --skip=publish --config .goreleaser-release.yml

release-snapshot: snapshot ## Alias for snapshot.

release: ## Build and verify official release artifacts for TAG=vX.Y.Z.
	@test -n "$(TAG)" || (echo "usage: make release TAG=v0.1.0" >&2; exit 2)
	./scripts/prepare-notcrawl-release.sh "$(TAG)"

release-artifacts: release ## Alias for release.

release-macos: ## Build signed and notarized macOS artifacts for TAG=vX.Y.Z.
	@test -n "$(TAG)" || (echo "usage: make release-macos TAG=v0.1.0" >&2; exit 2)
	@helper="$${MAC_RELEASE_HELPER:-$$HOME/Projects/agent-scripts/skills/release-mac-app/scripts/mac-release}"; \
	"$$helper" codesign-run -- ./scripts/package-notcrawl-macos-release.sh "$(TAG)"

verify-release: ## Verify the complete official release artifact set for TAG=vX.Y.Z.
	@test -n "$(TAG)" || (echo "usage: make verify-release TAG=v0.1.0 [ASSET_DIR=path]" >&2; exit 2)
	./scripts/verify-notcrawl-release.sh "$(TAG)" "$(ASSET_DIR)"
	@version="$(TAG)"; version="$${version#v}"; \
	./scripts/verify-notcrawl-macos-release.sh "$(TAG)" \
	"$(ASSET_DIR)/notcrawl_$${version}_darwin_arm64.tar.gz" \
	"$(ASSET_DIR)/notcrawl_$${version}_darwin_amd64.tar.gz"

verify-release-macos: ## Verify macOS artifacts in dist for TAG=vX.Y.Z.
	@test -n "$(TAG)" || (echo "usage: make verify-release-macos TAG=v0.1.0" >&2; exit 2)
	@version="$(TAG)"; version="$${version#v}"; \
	./scripts/verify-notcrawl-macos-release.sh "$(TAG)" dist/notcrawl_$${version}_darwin_*.tar.gz
