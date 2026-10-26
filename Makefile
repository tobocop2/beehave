GO_TEST_FLAGS := -race -count=1 -p 2
GOLANGCI_LINT_VERSION := v2.15.0
BIN := bin/obee

.PHONY: setup lint-version deps build release-snapshot format format-check lint vet vuln test coverage smoke mutation \
	governance-generate governance-validate governance-verify clean check

setup:  ## Install the Go tools and generate the agent files
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	command -v ai-rulez >/dev/null 2>&1 || brew install goldziher/tap/ai-rulez
	command -v go-test-coverage >/dev/null 2>&1 || go install github.com/vladopajic/go-test-coverage/v2@v2.20.1
	command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
	$(MAKE) governance-generate

build:  ## Build bin/obee
	go build -trimpath -o $(BIN) ./cmd/obee

release-snapshot:  ## Build release archives without publishing
	goreleaser release --snapshot --clean --skip=publish

lint-version:  ## Fail unless golangci-lint is the version CI runs
	@golangci-lint version | grep -q "version $(patsubst v%,%,$(GOLANGCI_LINT_VERSION)) " || { echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required: run make setup"; exit 1; }

format: lint-version  ## Format the Go code (gofumpt and goimports)
	golangci-lint fmt ./...

format-check: lint-version  ## Fail if the Go code is not formatted
	golangci-lint fmt --diff ./...

lint: lint-version  ## Lint the Go code
	golangci-lint run ./...

vet:
	go vet ./...

# No package of the module may link DBOS Conductor, the enterprise module, viper, net/http or the shell interpreter. crypto/tls is allowed: DBOS links it through its Postgres driver, and TestRun_OpensNoNetworkSocket proves obee opens no connection.
deps:  ## Fail if the binary links a forbidden dependency
	@if go list -deps ./... | grep -E '^(go\.dbos\.dev|github\.com/spf13/viper|net/http$$|mvdan\.cc/sh/v3/interp)'; then echo "forbidden dependency in the module"; exit 1; fi

vuln:  ## Fail on a known vulnerability the code can reach
	GOTELEMETRY=off govulncheck ./...

test:  ## Run the Go tests with the race detector and write coverage.out
	go test $(GO_TEST_FLAGS) -covermode=atomic -coverprofile=coverage.out ./...

coverage:  ## Enforce the coverage gate on coverage.out (run make test first)
	go-test-coverage --config=.go-test-coverage.yml

smoke: build  ## Run the built binary: --version prints and exits 0
	$(BIN) --version | grep -q '^obee '

# Run on one package: make mutation PKG=./internal/cli/...
mutation:
	gremlins unleash $(or $(PKG),./internal/...)

governance-generate:  ## Generate the agent files from .ai-rulez/
	ai-rulez generate --no-self-mcp

governance-validate:
	ai-rulez validate

governance-verify: governance-validate  ## Validate the config and render it without writing files
	ai-rulez generate --dry-run --no-self-mcp --quiet

clean:
	go clean -cache -testcache

check: format-check lint vet deps vuln test coverage smoke governance-verify  ## The full gate (same as CI)
