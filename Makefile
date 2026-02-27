GOCMD := go
GOFMT := ${GOCMD} fmt
GOMOD := ${GOCMD} mod
BINARY := vim-markdown-preview
GOLANGCILINT_CACHE := ${CURDIR}/.golangci-lint/build/cache

.PHONY: help
help: ## print this help message
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-z0-9A-Z_-]+:.*?##/ { printf "  \033[36m%-30s\033[0m%s\n", $$1, $$2 }' $(MAKEFILE_LIST)

tidy: ## tidy modules
	${GOMOD} tidy

fmt: ## apply go code style formatter
	${GOFMT} -x ./...

lint: ## run linters
	golangci-lint run -v

binary: fmt tidy lint test ## build a binary
	goreleaser build --clean --single-target --snapshot --output .

build: binary ## alias for `binary`

build-all: fmt tidy lint ## test release process with goreleaser, does not publish/upload
	goreleaser release --snapshot --clean

test: fmt tidy ## run tests
	go test -race -v ./...

web-vendor: ## download vendored browser JS/CSS/font dependencies
	./scripts/vendor-web-deps.sh

web-check: ## verify all vendored web dependencies are present (CI guard)
	./scripts/vendor-web-deps.sh --check
