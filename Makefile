GOVULNCHECK_VERSION := v1.9.0
BUF_VERSION := v1.75.0
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
# The commit buf breaking compares with. CI sets this to the commit before the change.
BUF_BASE ?= main

.DEFAULT_GOAL := help

.PHONY: help check build vet gamevet test web lint buflint bufbreaking vuln gen fmt tidy

help: ## Show the available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

check: build vet gamevet test web lint buflint bufbreaking vuln ## Run every check

build: ## Build all packages
	go build ./...

vet: ## Run go vet
	go vet ./...

gamevet: ## Run playhold's determinism checks, on the playhold version in go.mod
	go vet -vettool=$$(go tool -n playholdvet) ./...

test: ## Run the tests with the race detector, without cached results
	go test -count=1 -race ./...

web: web/node_modules ## Type-check, test and build the browser page
	npm --prefix web run check

web/node_modules: web/package-lock.json
	npm --prefix web ci
	touch web/node_modules

lint: ## Run golangci-lint with the repository's config
	golangci-lint cache clean
	golangci-lint run ./...

buflint: ## Lint the .proto files
	$(BUF) lint

bufbreaking: ## Check the .proto files only changed in ways older pages can still read, since BUF_BASE
	@if git cat-file -e '$(BUF_BASE):proto' 2>/dev/null; then \
		$(BUF) breaking --against '.git#ref=$(BUF_BASE)'; \
	else \
		echo "$(BUF_BASE) has no proto/ yet; nothing to compare with"; \
	fi

vuln: ## Check dependencies and the standard library for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

gen: web/node_modules ## Generate Go and TypeScript code from the .proto files
	$(BUF) generate

fmt: ## Format the code with golangci-lint's formatters
	golangci-lint fmt ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy