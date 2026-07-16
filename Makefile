BINARY ?= bin/notcrawl

.PHONY: build test run fmt release-notes release-snapshot release-check release-artifacts release-macos verify-release-macos

build:
	go build -o $(BINARY) ./cmd/notcrawl

test:
	go test ./...

run:
	go run ./cmd/notcrawl $(ARGS)

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

release-notes:
	@test -n "$(TAG)" || (echo "usage: make release-notes TAG=v0.1.0" >&2; exit 2)
	scripts/release-notes.sh "$(TAG)"

release-check:
	goreleaser check
	goreleaser check --config .goreleaser-release.yml

release-snapshot:
	goreleaser release --snapshot --clean

release-artifacts:
	@test -n "$(TAG)" || (echo "usage: make release-artifacts TAG=v0.1.0" >&2; exit 2)
	./scripts/prepare-notcrawl-release.sh "$(TAG)"

release-macos:
	@test -n "$(TAG)" || (echo "usage: make release-macos TAG=v0.1.0" >&2; exit 2)
	@helper="$${MAC_RELEASE_HELPER:-$$HOME/Projects/agent-scripts/skills/release-mac-app/scripts/mac-release}"; \
	"$$helper" codesign-run -- ./scripts/package-notcrawl-macos-release.sh "$(TAG)"

verify-release-macos:
	@test -n "$(TAG)" || (echo "usage: make verify-release-macos TAG=v0.1.0" >&2; exit 2)
	@version="$(TAG)"; version="$${version#v}"; \
	./scripts/verify-notcrawl-macos-release.sh "$(TAG)" dist/notcrawl_$${version}_darwin_*.tar.gz
