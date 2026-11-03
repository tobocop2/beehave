# GO_TEST_P is the number of test packages that run at once. Empty keeps the Go default (one for each CPU).
# A small machine passes GO_TEST_P=2.
GO_TEST_P ?=
# The race detector turns on pointer checks (checkptr) for every package. The SQLite driver is C code that a translator turned into Go,
# and the workflow store runs it on every query. The pointer checks inside that driver cost a quarter to a third of the CPU of the test
# run. Upstream tests the driver with the pointer checks on; the main run skips them for speed. The race detector stays on for all packages.
# GO_TEST_DRIVER_PKGS are the fast packages that use the driver directly. The second run in "test" runs them with the pointer checks
# on for the driver, so a driver upgrade is still checked for pointer misuse on every gate.
GO_TEST_DRIVER_PKGS := ./internal/store/local
GO_TEST_FLAGS := -race -gcflags='modernc.org/...=-d=checkptr=0' -count=1 $(if $(GO_TEST_P),-p $(GO_TEST_P))
GOLANGCI_LINT_VERSION := v2.15.0
AI_RULEZ_VERSION := v5.9.2
BIN := bin/obee
REMEDY := bin/remedy

DEADCODE_VERSION := golang.org/x/tools/cmd/deadcode@v0.51.0

.PHONY: setup lint-version ai-rulez-version ai-rulez-install deps deadcode build release-snapshot format format-check lint vet vuln test coverage smoke smoke-agent smoke-hook smoke-sandbox mutation \
	governance-generate governance-validate governance-verify clean check check-darwin

setup:  ## Install the Go tools and generate the agent files
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(MAKE) ai-rulez-install
	command -v go-test-coverage >/dev/null 2>&1 || go install github.com/vladopajic/go-test-coverage/v2@v2.20.1
	command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
	command -v deadcode >/dev/null 2>&1 || go install $(DEADCODE_VERSION)
	$(MAKE) governance-generate

build:  ## Build bin/obee and bin/remedy
	go build -trimpath -o $(BIN) ./cmd/obee
	go build -trimpath -o $(REMEDY) ./examples/remediation/tools/cmd/remedy

release-snapshot:  ## Build release archives without publishing
	goreleaser release --snapshot --clean --skip=publish

lint-version:  ## Fail unless golangci-lint is the version CI runs
	@golangci-lint version | grep -q "version $(patsubst v%,%,$(GOLANGCI_LINT_VERSION)) " || { echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required: run make setup"; exit 1; }

ai-rulez-install:  ## Install ai-rulez $(AI_RULEZ_VERSION) into the Go bin folder
	sh scripts/install-ai-rulez.sh $(AI_RULEZ_VERSION) "$$(go env GOPATH)/bin"

ai-rulez-version:  ## Fail unless ai-rulez is the version the gate expects
	@ai-rulez --version | grep -qx "ai-rulez version $(patsubst v%,%,$(AI_RULEZ_VERSION))" || { echo "ai-rulez $(AI_RULEZ_VERSION) is required, found: $$(ai-rulez --version). Run make ai-rulez-install. A new version can add rules: change AI_RULEZ_VERSION in its own pull request."; exit 1; }

format: lint-version  ## Format the Go code (gofumpt and goimports)
	golangci-lint fmt ./...

format-check: lint-version  ## Fail if the Go code is not formatted
	golangci-lint fmt --diff ./...

lint: lint-version  ## Lint the Go code
	golangci-lint run ./...

vet:
	go vet ./...

# No package of the module may link DBOS Conductor, the enterprise module, viper or the shell interpreter. net/http and crypto/tls are allowed in dependencies: DBOS links both, and TestRun_OpensNoNetworkSocket proves obee opens no connection. The depguard rule no-http-client keeps net/http out of obee's own code.
deps:  ## Fail if the binary links a forbidden dependency
	@if go list -deps ./... | grep -E '^(go\.dbos\.dev|github\.com/spf13/viper|mvdan\.cc/sh/v3/interp)'; then echo "forbidden dependency in the module"; exit 1; fi

# deadcode -test counts the tests as entry points, so it reports a function that nothing calls, not even a test.
deadcode:  ## Fail if a function is unreachable from main and from the tests
	@out="$$(deadcode -test ./...)" || exit 1; if [ -n "$$out" ]; then echo "$$out"; echo "unreachable functions: delete them"; exit 1; fi

vuln:  ## Fail on a known vulnerability the code can reach
	GOTELEMETRY=off govulncheck ./...

test:  ## Run the Go tests with the race detector and write coverage.out
	go test $(GO_TEST_FLAGS) -covermode=atomic -coverprofile=coverage.out ./...
	go test -race -count=1 $(GO_TEST_DRIVER_PKGS)

coverage:  ## Enforce the coverage gate on coverage.out (run make test first)
	go-test-coverage --config=.go-test-coverage.yml

smoke: build  ## Run the built binary: --version prints and exits 0
	$(BIN) --version | grep -q '^obee '
	$(REMEDY) --help | grep -q '^usage: remedy '

smoke-agent: build  ## Run examples/local once with the real claude CLI (costs an agent run; not part of check)
	sh scripts/smoke-agent.sh

smoke-hook: build  ## Run one step with the real claude CLI and check that the command hook blocks git add -A (costs an agent run; not part of check)
	sh scripts/smoke-hook.sh

smoke-sandbox: build  ## Run one step with the real claude CLI and check what the sandbox denies and allows (costs an agent run, at most 0.30 USD; not part of check)
	sh scripts/smoke-sandbox.sh

check-darwin:  ## On a Mac: run the Seatbelt enforcer tests and the contract suite under the real sandbox-exec (not part of check)
	@test "$$(uname -s)" = Darwin || { echo "check-darwin needs a Mac: the Seatbelt code builds on darwin only"; exit 1; }
	go test -count=1 -v -run 'TestEnforcer_PassesTheContractSuite' ./internal/sandbox/seatbelt | grep -v '^=== ' | grep -q -e '--- PASS: TestEnforcer_PassesTheContractSuite' || { echo "the contract suite did not pass under the real enforcer (skipped or failed)"; exit 1; }
	go test -count=1 ./internal/sandbox/...

# Run on one package: make mutation PKG=./internal/cli/...
mutation:
	gremlins unleash $(or $(PKG),./internal/...)

governance-generate: ai-rulez-version  ## Generate the agent files from .ai-rulez/
	ai-rulez generate --no-self-mcp

governance-validate: ai-rulez-version
	ai-rulez validate

governance-verify: governance-validate  ## Validate the config and render it without writing files
	ai-rulez generate --dry-run --no-self-mcp --quiet

clean:
	go clean -cache -testcache

check: format-check lint vet deps deadcode vuln test coverage smoke governance-verify  ## The full gate (same as CI)
