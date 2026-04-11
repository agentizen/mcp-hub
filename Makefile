# mcp-hub — Development & CI Makefile
#
# Usage:
#   make              Run full validation pipeline (tidy + fmt + vet + lint + build + test)
#   make ci-setup     Install CI tools (golangci-lint, gosec, goimports)
#   make ci           Full CI pipeline (ci-setup + all + docker)
#   make docker       Build the Docker image locally
#   make validate-config  Validate config.yaml without starting the server

COVERAGE_FILE := coverage.out
DISPATCHER    := dispatcher

# ---------- Aggregate targets ----------

.PHONY: all
all: tidy fmt vet lint build test

.PHONY: ci
ci: ci-setup all docker

.PHONY: quality
quality: lint test-coverage

# ---------- Dependencies ----------

.PHONY: tidy
tidy:
	@echo "--- go mod tidy ---"
	cd $(DISPATCHER) && go mod tidy

# ---------- Formatting ----------

.PHONY: fmt
fmt:
	@echo "--- gofmt check ---"
	@BADFILES=$$(find $(DISPATCHER) -type f -name '*.go' | xargs gofmt -l); \
	if [ -n "$$BADFILES" ]; then \
		echo "Files need gofmt:"; echo "$$BADFILES"; exit 1; \
	fi
	@echo "ok"

.PHONY: fmt-fix
fmt-fix:
	find $(DISPATCHER) -type f -name '*.go' | xargs gofmt -s -w

# ---------- Imports ----------

.PHONY: imports
imports:
	@echo "--- goimports check ---"
	@which goimports > /dev/null 2>&1 || { echo "goimports not installed (run: make ci-setup)"; exit 1; }
	@BADFILES=$$(find $(DISPATCHER) -type f -name '*.go' | xargs goimports -l); \
	if [ -n "$$BADFILES" ]; then \
		echo "Files need goimports:"; echo "$$BADFILES"; exit 1; \
	fi
	@echo "ok"

.PHONY: imports-fix
imports-fix:
	find $(DISPATCHER) -type f -name '*.go' | xargs goimports -w

# ---------- Vet ----------

.PHONY: vet
vet:
	@echo "--- go vet ---"
	cd $(DISPATCHER) && go vet ./...

# ---------- Lint ----------

.PHONY: lint
lint:
	@echo "--- golangci-lint ---"
	cd $(DISPATCHER) && golangci-lint run --timeout=5m

# ---------- Security ----------

.PHONY: security
security:
	@echo "--- gosec ---"
	cd $(DISPATCHER) && gosec -quiet ./...

# ---------- Build ----------

.PHONY: build
build:
	@echo "--- go build ---"
	cd $(DISPATCHER) && go build -v ./...

# ---------- Test ----------

.PHONY: test
test:
	@echo "--- go test ---"
	cd $(DISPATCHER) && go test --count=1 ./...

.PHONY: test-verbose
test-verbose:
	cd $(DISPATCHER) && go test -v --count=1 ./...

.PHONY: test-race
test-race:
	@echo "--- go test -race ---"
	cd $(DISPATCHER) && go test -race --count=1 ./...

.PHONY: test-coverage
test-coverage:
	@echo "--- go test -race -cover ---"
	cd $(DISPATCHER) && go test -race -coverprofile=$(COVERAGE_FILE) -covermode=atomic ./...
	cd $(DISPATCHER) && go tool cover -func=$(COVERAGE_FILE)

.PHONY: test-coverage-html
test-coverage-html: test-coverage
	cd $(DISPATCHER) && go tool cover -html=$(COVERAGE_FILE)

# ---------- Config validation ----------

.PHONY: validate-config
validate-config:
	cd $(DISPATCHER) && go run . --config ../config.yaml --validate

# ---------- Docker ----------

.PHONY: docker
docker:
	@echo "--- docker build ---"
	docker build -t mcp-hub:local .

# ---------- CI Setup ----------

.PHONY: ci-setup
ci-setup:
	@echo "--- Installing CI tools ---"
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.3
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install golang.org/x/tools/cmd/goimports@latest

# ---------- Clean ----------

.PHONY: clean
clean:
	rm -f $(DISPATCHER)/$(COVERAGE_FILE)
