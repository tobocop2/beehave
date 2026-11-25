# GO_TEST_P is the number of test packages that run at once. Empty keeps the Go default (one for each CPU).
# A small machine passes GO_TEST_P=2.
GO_TEST_P ?=
# The race detector turns on pointer checks (checkptr) for every package. The SQLite driver is C code that a translator turned into Go,
# and the workflow store runs it on every query. The pointer checks inside that driver cost a quarter to a third of the CPU of the test
# run. Upstream tests the driver with the pointer checks on; the main run skips them for speed. The race detector stays on for all packages.
# GO_TEST_DRIVER_PKGS are the fast packages that use the driver directly. The second run in "test" runs them with the pointer checks
# on for the driver, so a driver upgrade is still checked for pointer misuse on every gate.
GO_TEST_DRIVER_PKGS := ./internal/store/local
# "test" starts one go test run for each flow shard and one for the other packages, all at the same time. The CPU work of the runs adds up
# to more than one 4-core CI runner can do in the CI job limit, so CI runs "make test" in several jobs, and GO_TEST_SLICE=<job>/<jobs> picks
# the runs of one job: job 1 runs the other packages, and jobs 2 and up share the flow shards (shard S goes to job 2 + (S - 1) mod (jobs - 1)).
# The default 1/1 runs everything. Each flow run gets GO_TEST_PROCS cores as GOMAXPROCS, which also sets the -p and -parallel defaults, so
# the runs of one job share the cores of the host. The run of the other packages gets the cores that the flow runs leave (at least two).
GO_CORES := $(shell nproc 2>/dev/null || sysctl -n hw.ncpu)
# The CPU work of a run does not change with its share, so a smaller share takes longer. Each run gets GO_TEST_TIMEOUT. All the runs of a job
# start together, so a run ends at the timeout at the latest: a CI job needs a limit above the build time plus GO_TEST_TIMEOUT.
GO_TEST_TIMEOUT ?= 18m
GO_TEST_FLAGS := -race -gcflags='modernc.org/...=-d=checkptr=0' -count=1 -timeout $(GO_TEST_TIMEOUT) $(if $(GO_TEST_P),-p $(GO_TEST_P))
# The flow package has about 1500 tests, and under the race detector the run of all of them takes far longer than one run can. "test" lists the tests
# of the package (FLOW_LIST) and gives the test on line N of the list to shard N mod FLOW_SHARD_COUNT, so every listed test is in exactly one
# shard and no test is left out of every shard. The tests of one family sit on neighbour lines, so each shard gets a mix of the cheap
# and the costly tests, and a new test needs no change here. Each shard writes its own coverage profile, and "coverage" merges them
# with the profile of the other packages (CI jobs hand their profiles on as artifacts), so the gate still measures the whole package.
# Each run writes its own test-*.log file and prints it when it ends. "test" fails if any run fails.
# A flow package that does not build lists no test. "test" then writes the build failure to junit-flow-build.xml and still runs the other
# packages, so a tree with test files that do not build (the revert run of the gate) gives a report of every package. The shards then find no
# list and fail, so "test" fails as before.
comma := ,
space := $() $()
FLOW_PKG := ./internal/flow
FLOW_LIST := test-flow-list.log
FLOW_SHARD_COUNT := 12
FLOW_SHARDS := $(shell seq 1 $(FLOW_SHARD_COUNT))
FLOW_PROFILES := $(subst $(space),,coverage.out$(foreach s,$(FLOW_SHARDS),$(comma)coverage-flow-$(s).out))
GO_TEST_SLICE ?= 1/1
ifneq ($(shell printf '%s' '$(GO_TEST_SLICE)' | grep -Ec '^[1-9][0-9]*/[1-9][0-9]*$$'),1)
$(error GO_TEST_SLICE must be <job>/<jobs>, such as 2/4, not "$(GO_TEST_SLICE)")
endif
SLICE_JOB := $(word 1,$(subst /, ,$(GO_TEST_SLICE)))
SLICE_JOBS := $(word 2,$(subst /, ,$(GO_TEST_SLICE)))
ifneq ($(shell [ $(SLICE_JOB) -le $(SLICE_JOBS) ] && [ $(SLICE_JOBS) -le $(FLOW_SHARD_COUNT) ] && echo ok),ok)
$(error GO_TEST_SLICE must name a job up to the number of jobs, and at most $(FLOW_SHARD_COUNT) jobs, not "$(GO_TEST_SLICE)")
endif
RUN_SHARDS := $(if $(filter 1,$(SLICE_JOBS)),$(FLOW_SHARDS),$(if $(filter 1,$(SLICE_JOB)),,$(shell seq $$(( $(SLICE_JOB) - 1 )) $$(( $(SLICE_JOBS) - 1 )) $(FLOW_SHARD_COUNT))))
RUN_OTHER := $(if $(filter 1,$(SLICE_JOBS) $(SLICE_JOB)),yes)
RUN_TARGETS := $(if $(RUN_OTHER),test-run-other) $(addprefix test-run-flow-,$(RUN_SHARDS))
FLOW_RUN_TARGETS := $(addprefix test-run-flow-,$(FLOW_SHARDS))
GO_TEST_UNITS = $(shell echo $$(( $(words $(RUN_SHARDS)) + $(if $(RUN_OTHER),2,0) )))
GO_TEST_PROCS ?= $(shell u=$(GO_TEST_UNITS); [ $$u -ge 1 ] || u=1; n=$$(( ($(GO_CORES) + u / 2) / u )); [ $$n -ge 1 ] || n=1; echo $$n)
GO_TEST_PROCS_HEAVY = $(shell m=$$(( 2 * $(GO_TEST_PROCS) )); n=$$(( $(GO_CORES) - $(words $(RUN_SHARDS)) * $(GO_TEST_PROCS) )); [ $$n -ge $$m ] || n=$$m; echo $$n)
ifneq ($(shell printf '%s' '$(GO_TEST_PROCS)' | grep -Ec '^[1-9][0-9]*$$'),1)
$(error GO_TEST_PROCS must be a whole number of at least 1, not "$(GO_TEST_PROCS)")
endif
GOLANGCI_LINT_VERSION := v2.15.0
GOTESTSUM_VERSION := v1.13.0
GO_TEST_COVERAGE_VERSION := v2.20.1
GOVULNCHECK_VERSION := v1.9.0
# GO_INSTALL_DIR is the folder where go install writes a binary: GOBIN when set, else the bin folder of the first GOPATH entry.
GO_INSTALL_DIR = $(or $(shell go env GOBIN),$(firstword $(subst :, ,$(shell go env GOPATH)))/bin)
# GO_TOOL_AT succeeds when the binary that go install writes for $(1) was built from module $(2) at version $(3). make setup reinstalls a tool
# when it fails. It reads the file in GO_INSTALL_DIR, not the first copy on PATH, so an older copy earlier on PATH does not force a reinstall.
GO_TOOL_AT = go version -m "$(GO_INSTALL_DIR)/$(1)" 2>/dev/null | grep -Eq '^[[:space:]]+mod[[:space:]]+$(subst .,\.,$(2))[[:space:]]+$(subst .,\.,$(3))[[:space:]]'
# Each run of "test" goes through gotestsum, which writes one JUnit file in REPORT_DIR (the glob reports/*.xml matches them all).
REPORT_DIR := reports
GOTESTSUM = gotestsum --format standard-quiet --junitfile $(REPORT_DIR)
AI_RULEZ_VERSION := v5.9.2
# The secret scan of the candidate gate job runs gitleaks. The "git" subcommand exists from 8.19 on.
GITLEAKS_VERSION := v8.30.1
# make mutation and make mutation-pr run gremlins. scripts/mutation-pr.sh reads the JSON report of this version.
GREMLINS_VERSION := v0.6.0
# make goreleaser-install installs this goreleaser. make setup, the CI snapshot job and the tag workflow in .github/workflows/release.yaml all run it.
GORELEASER_VERSION := v2.20.2
BIN := bin/beehave
REMEDY := bin/remedy

DEADCODE_VERSION := golang.org/x/tools/cmd/deadcode@v0.51.0

.PHONY: setup go-test-coverage-install govulncheck-install gotestsum-install gitleaks-install golangci-lint-version lint-version ai-rulez-version ai-rulez-install contract-tools-install deps deadcode build remedy goreleaser-install release-snapshot format format-check lint lint-darwin vet vuln test test-examples coverage smoke smoke-agent smoke-hook smoke-sandbox mutation mutation-pr \
	governance-generate governance-validate governance-verify clean check check-darwin

setup:  ## Install the Go tools and generate the agent files
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(MAKE) ai-rulez-install
	$(MAKE) go-test-coverage-install govulncheck-install gotestsum-install gitleaks-install
	$(call GO_TOOL_AT,deadcode,golang.org/x/tools,$(word 2,$(subst @, ,$(DEADCODE_VERSION)))) || go install $(DEADCODE_VERSION)
	go install github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION)
	$(MAKE) goreleaser-install
	$(MAKE) governance-generate

# The CI jobs run these targets, so the version of each tool is set once, in the variables at the top of this file.
go-test-coverage-install:  ## Install go-test-coverage $(GO_TEST_COVERAGE_VERSION) into the Go bin folder
	$(call GO_TOOL_AT,go-test-coverage,github.com/vladopajic/go-test-coverage/v2,$(GO_TEST_COVERAGE_VERSION)) || go install github.com/vladopajic/go-test-coverage/v2@$(GO_TEST_COVERAGE_VERSION)

govulncheck-install:  ## Install govulncheck $(GOVULNCHECK_VERSION) into the Go bin folder
	$(call GO_TOOL_AT,govulncheck,golang.org/x/vuln,$(GOVULNCHECK_VERSION)) || go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

gotestsum-install:  ## Install gotestsum $(GOTESTSUM_VERSION) into the Go bin folder
	$(call GO_TOOL_AT,gotestsum,gotest.tools/gotestsum,$(GOTESTSUM_VERSION)) || go install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

gitleaks-install:  ## Install gitleaks $(GITLEAKS_VERSION) into the Go bin folder
	$(call GO_TOOL_AT,gitleaks,github.com/zricethezav/gitleaks/v8,$(GITLEAKS_VERSION)) || go install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)

golangci-lint-version:  ## Print the golangci-lint version that CI and make setup use
	@echo $(GOLANGCI_LINT_VERSION)

build:  ## Build bin/beehave
	go build -trimpath -o $(BIN) ./cmd/beehave

remedy:  ## Build bin/remedy, the tool of the remediation example (not part of the release)
	go build -trimpath -o $(REMEDY) ./examples/remediation/tools/cmd/remedy

goreleaser-install:  ## Install goreleaser $(GORELEASER_VERSION) into the Go bin folder
	goreleaser --version 2>/dev/null | grep -Eq '^GitVersion:[[:space:]]+v?$(subst .,\.,$(patsubst v%,%,$(GORELEASER_VERSION)))[[:space:]]*$$' || go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

release-snapshot:  ## Build release archives without publishing
	goreleaser release --snapshot --clean --skip=publish

lint-version:  ## Fail unless golangci-lint is the version CI runs
	@golangci-lint version | grep -q "version $(patsubst v%,%,$(GOLANGCI_LINT_VERSION)) " || { echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required: run make setup"; exit 1; }

ai-rulez-install:  ## Install ai-rulez $(AI_RULEZ_VERSION) into the Go bin folder
	sh scripts/install-ai-rulez.sh $(AI_RULEZ_VERSION) "$$(go env GOPATH)/bin"

CONTRACT_TOOLS_DIR ?= $(shell go env GOPATH)/bin

contract-tools-install:  ## Install dolt and bd 1.3.2 (linux amd64) into CONTRACT_TOOLS_DIR for the contract tests against a real bd
	sh scripts/install-contract-tools.sh "$(CONTRACT_TOOLS_DIR)"

ai-rulez-version:  ## Fail unless ai-rulez is the version the gate expects
	@ai-rulez --version | grep -qx "ai-rulez version $(patsubst v%,%,$(AI_RULEZ_VERSION))" || { echo "ai-rulez $(AI_RULEZ_VERSION) is required, found: $$(ai-rulez --version). Run make ai-rulez-install. A new version can add rules: change AI_RULEZ_VERSION in its own pull request."; exit 1; }

format: lint-version  ## Format the Go code (gofumpt and goimports)
	golangci-lint fmt ./...

format-check: lint-version  ## Fail if the Go code is not formatted
	golangci-lint fmt --diff ./...

lint: lint-version  ## Lint the Go code
	golangci-lint run ./...

# GOOS=darwin makes golangci-lint and go vet load the files that build on macOS only, which a Linux run never reads.
lint-darwin: lint-version  ## Lint and vet the Go code as GOOS=darwin
	GOOS=darwin golangci-lint run ./...
	GOOS=darwin go vet ./...

vet:
	go vet ./...

# No package of the module may link DBOS Conductor, the enterprise module, viper or the shell interpreter. net/http and crypto/tls are allowed in dependencies: DBOS links both, and TestRun_OpensNoNetworkSocket proves beehave opens no connection. The depguard rule no-http-client keeps net/http out of beehave's own code.
deps:  ## Fail if the binary links a forbidden dependency
	@if go list -deps ./... | grep -E '^(go\.dbos\.dev|github\.com/spf13/viper|mvdan\.cc/sh/v3/interp)'; then echo "forbidden dependency in the module"; exit 1; fi

# deadcode -test counts the tests as entry points, so it reports a function that nothing calls, not even a test.
deadcode:  ## Fail if a function is unreachable from main and from the tests
	@out="$$(deadcode -test ./...)" || exit 1; if [ -n "$$out" ]; then echo "$$out"; echo "unreachable functions: delete them"; exit 1; fi

vuln:  ## Fail on a known vulnerability the code can reach
	GOTELEMETRY=off govulncheck ./...

test:  ## Run the Go tests with the race detector and write coverage.out, the flow profiles and the JUnit reports in reports/
	@mkdir -p $(REPORT_DIR); rm -f $(REPORT_DIR)/junit-*.xml coverage.out coverage-flow-*.out test-*.log
	$(if $(RUN_SHARDS),@go test $(GO_TEST_FLAGS) -list '.*' $(FLOW_PKG) | grep -E '^(Test|Example|Fuzz)' > $(FLOW_LIST); [ -s $(FLOW_LIST) ] || { echo "make test: go test listed no flow tests" >&2; $(GOTESTSUM)/junit-flow-build.xml -- $(GO_TEST_FLAGS) -run '^$$' $(FLOW_PKG) > test-flow-build.log 2>&1; echo "== test-flow-build.log"; cat test-flow-build.log; })
	@$(MAKE) --no-print-directory -k -j $(if $(RUN_OTHER),test-run-other) $(RUN_TARGETS)
	$(if $(RUN_OTHER),go test -race -count=1 $(GO_TEST_DRIVER_PKGS))

# One run of go test each: the other packages, and the flow tests that FLOW_LIST assigns to the shard. "test" starts the runs of the slice
# together, and each run prints its own log when it ends. The recipe of "test" has no $(MAKE) in a line that does work, so make -n runs nothing.
.PHONY: test-run-other $(FLOW_RUN_TARGETS)
test-run-other:
	GOMAXPROCS=$(GO_TEST_PROCS_HEAVY) $(GOTESTSUM)/junit-other.xml -- $(GO_TEST_FLAGS) -covermode=atomic -coverprofile=coverage.out $$(go list ./... | grep -v '/internal/flow$$') > test-other.log 2>&1; \
	rc=$$?; echo "== test-other.log"; cat test-other.log; exit $$rc

$(FLOW_RUN_TARGETS): test-run-flow-%:
	[ -s $(FLOW_LIST) ] || { echo "make test: no list of the flow tests in $(FLOW_LIST)" >&2; exit 1; }; \
	names=$$(awk -v n=$(FLOW_SHARD_COUNT) -v i=$* 'NR % n == i % n { printf "%s%s", sep, $$0; sep = "|" }' $(FLOW_LIST)); \
	GOMAXPROCS=$(GO_TEST_PROCS) $(GOTESTSUM)/junit-flow-$*.xml -- $(GO_TEST_FLAGS) -covermode=atomic -coverprofile=coverage-flow-$*.out -run "^($$names)\$$" $(FLOW_PKG) > test-flow-$*.log 2>&1; \
	rc=$$?; echo "== test-flow-$*.log"; cat test-flow-$*.log; exit $$rc

test-examples:  ## Run the tests of the examples folder alone (a local loop; make test runs them too)
	go test -race -count=1 -timeout 120s ./examples/...

coverage:  ## Enforce the coverage gate on coverage.out and the flow profiles (run make test first)
	go-test-coverage --config=.go-test-coverage.yml --profile=$(FLOW_PROFILES)

smoke: build remedy  ## Run the built binary: --version prints and exits 0, and the upload-pack helper of the push answers
	$(BIN) --version | grep -q '^beehave '
	$(BIN) --beehave-upload-pack nope / / 2>&1 | grep -q '^beehave upload-pack: '
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

# Both mutation targets start 4 gremlins workers, set in .gremlins.yaml. To change the count, edit that file.
# Run on one package: make mutation PKG=./internal/cli
# PKG is a folder, never a "..." pattern: gremlins reads "./internal/..." as a folder name, finds no file and passes. The target refuses such a PKG.
mutation:
	@case "$(PKG)" in *...*) echo 'PKG=$(PKG) holds "...": gremlins reads it as a folder name and mutates nothing. Name one folder, such as PKG=./internal/cli'; exit 2;; esac
	gremlins unleash $(or $(PKG),.)

# Per-PR run: mutate only the Go lines that differ from BASE, the branch the pull request targets. BASE is required: make mutation-pr BASE=origin/integration/r1
# Needs git, go, gremlins, jq and timeout (or gtimeout) on the path; MUTATION_TIMEOUT sets the limit, a positive whole number of seconds (default 3600 when the diff changes a non-test Go file in internal/flow or below it, otherwise 1200).
# Starts gremlins once for each changed folder (a folder below another changed folder is covered by the upper one), so the coverage run reads those packages only. MUTATION_TIMEOUT is one deadline for all starts and also the -timeout of the coverage run.
# Fails when no mutant applies or one lives. A diff with no Go source change is not applicable and passes. A diff whose changed lines hold no operator that gremlins mutates is "no candidate" and passes.
mutation-pr:
	sh scripts/mutation-pr.sh "$(BASE)"

governance-generate: ai-rulez-version  ## Generate the agent files from .ai-rulez/
	ai-rulez generate --no-self-mcp

governance-validate: ai-rulez-version
	ai-rulez validate

governance-verify: governance-validate  ## Validate the config and render it without writing files
	ai-rulez generate --dry-run --no-self-mcp --quiet

clean:
	go clean -cache -testcache

check: format-check lint lint-darwin vet deps deadcode vuln test coverage smoke governance-verify  ## The full gate (same as CI)
