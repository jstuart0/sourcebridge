.PHONY: all build build-go build-web build-worker build-vscode test test-go test-web test-worker test-vscode \
	lint lint-go lint-web lint-worker lint-vscode package-vscode install-vscode \
	proto proto-clean docker-build docker-up docker-down \
	dev dev-web dev-go clean migrate help integration-test smoke-test phase-gate ci \
	test-livingwiki-integration test-livingwiki-smoke \
	benchmark-comprehension-fake benchmark-comprehension-local benchmark-comprehension-report \
	benchmark-report-quality-live

GO_BIN = bin/sourcebridge
GO_MIGRATE_BIN = bin/migrate
PROTO_DIR = proto
GEN_DIR = gen

all: build

# Build
build: build-go build-web build-vscode

build-go:
	go build -o $(GO_BIN) ./cmd/sourcebridge

build-web:
	cd web && npm ci && npm run build

build-worker:
	cd workers && uv sync

build-vscode:
	cd plugins/vscode && npm ci && npm run compile

# Test
test: test-go test-web test-worker test-vscode

test-go:
	go test ./... -v -race

test-web:
	cd web && npm test

test-worker:
	cd workers && uv run python -m pytest tests/ -v

test-vscode:
	cd plugins/vscode && npm test

# Lint
lint: lint-go lint-web lint-worker lint-vscode

lint-go:
	golangci-lint run ./...

lint-web:
	cd web && npm run lint

lint-worker:
	cd workers && uv run ruff check .

lint-vscode:
	cd plugins/vscode && npx eslint src --ext ts

# Package the VS Code extension as a VSIX. The output file lands in
# plugins/vscode/ and is gitignored. Use `install-vscode` to drop it
# into your local VS Code afterward.
package-vscode:
	cd plugins/vscode && npm run compile && npm run package

# Install the most recently packaged VSIX into the VS Code on the
# current machine. Requires the `code` CLI to be on PATH (macOS: the
# full path lives at "/Applications/Visual Studio Code.app/Contents/
# Resources/app/bin/code" — symlink it or use `make` from a shell
# that has it).
install-vscode: package-vscode
	code --install-extension $(shell ls -t plugins/vscode/*.vsix | head -1) --force

# Proto
PROTO_SOURCES = $(PROTO_DIR)/common/v1/types.proto \
	$(PROTO_DIR)/reasoning/v1/reasoning.proto \
	$(PROTO_DIR)/linking/v1/linking.proto \
	$(PROTO_DIR)/requirements/v1/requirements.proto \
	$(PROTO_DIR)/indexer/v1/indexer.proto \
	$(PROTO_DIR)/enterprise/v1/report.proto \
	$(PROTO_DIR)/knowledge/v1/knowledge.proto \
	$(PROTO_DIR)/contracts/v1/contracts.proto

proto:
	cd $(PROTO_DIR) && buf generate
	rm -rf $(GEN_DIR)/python
	mkdir -p $(GEN_DIR)/python
	workers/.venv/bin/python3 -m grpc_tools.protoc \
		-I$(PROTO_DIR) \
		--python_out=$(GEN_DIR)/python \
		--grpc_python_out=$(GEN_DIR)/python \
		--pyi_out=$(GEN_DIR)/python \
		$(PROTO_SOURCES)
	find $(GEN_DIR)/python -type d -exec touch {}/__init__.py \;

proto-clean:
	rm -rf $(GEN_DIR)

# Docker
docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

# Dev
dev: dev-go

dev-go: build-go
	./$(GO_BIN) serve

dev-web:
	cd web && npm run dev

# Clean
clean:
	rm -rf bin/ gen/ web/.next web/node_modules/.cache

# Migration
migrate:
	go build -o $(GO_MIGRATE_BIN) ./cmd/migrate
	./$(GO_MIGRATE_BIN)

# Integration tests
integration-test:
	go test ./tests/integration/... -v -count=1 -timeout 120s

# Living-wiki Tier-1 unit-integration test (in-memory fakes, no external services)
test-livingwiki-integration:
	go test -tags integration -race -count=1 -timeout 120s \
		./internal/livingwiki/... -v -run ^TestLivingWikiE2E

# Living-wiki Tier-2 real-Confluence smoke (requires env vars, runs against live cluster)
# SOURCEBRIDGE_URL, SOURCEBRIDGE_ADMIN_TOKEN, and SMOKE_REPO_ID must be set.
test-livingwiki-smoke:
	go run ./cmd/livingwiki-smoke

# Smoke tests
smoke-test:
	bash tests/smoke/phase1.sh

# Phase gate
phase-gate:
ifndef PHASE
	$(error PHASE is required, e.g. make phase-gate PHASE=1)
endif
	@echo "=== Phase $(PHASE) Gate ==="
	$(MAKE) build
	$(MAKE) test
	$(MAKE) lint-go
	cd workers && uv run ruff check .
ifeq ($(PHASE),1)
	$(MAKE) smoke-test
endif
ifeq ($(PHASE),8)
	@echo "Checking repository completeness..."
	@test -f LICENSE && echo "  LICENSE exists" || (echo "  MISSING: LICENSE" && exit 1)
	@grep -q "GNU AFFERO GENERAL PUBLIC LICENSE" LICENSE && echo "  LICENSE is AGPL" || (echo "  LICENSE is not AGPL" && exit 1)
	@test -f README.md && echo "  README.md exists" || (echo "  MISSING: README.md" && exit 1)
	@grep -q "docker compose" README.md && echo "  README mentions docker compose" || (echo "  README missing docker compose" && exit 1)
	@grep -q "brew install" README.md && echo "  README mentions brew install" || (echo "  README missing brew install" && exit 1)
	@test -f CONTRIBUTING.md && echo "  CONTRIBUTING.md exists" || (echo "  MISSING: CONTRIBUTING.md" && exit 1)
	@test -d .github/ISSUE_TEMPLATE && echo "  Issue templates exist" || (echo "  MISSING: issue templates" && exit 1)
	@ls .github/ISSUE_TEMPLATE/*.md >/dev/null 2>&1 && echo "  At least 1 issue template" || (echo "  No issue templates" && exit 1)
	@echo "  Repository completeness: PASS"
endif
ifeq ($(PHASE),11)
	@echo "Checking Phase 11: Operations..."
	@echo "  Checking Helm chart..."
	helm lint deploy/helm/sourcebridge/
	helm template sourcebridge deploy/helm/sourcebridge/ > /dev/null
	@echo "  Helm chart: PASS"
	@echo "  Checking documentation..."
	@test -f docs/admin/deployment.md && echo "  docs/admin/deployment.md exists" || (echo "  MISSING: docs/admin/deployment.md" && exit 1)
	@test -f docs/admin/backup-restore.md && echo "  docs/admin/backup-restore.md exists" || (echo "  MISSING: docs/admin/backup-restore.md" && exit 1)
	@test -f docs/self-hosted/helm-guide.md && echo "  docs/self-hosted/helm-guide.md exists" || (echo "  MISSING: docs/self-hosted/helm-guide.md" && exit 1)
	@test -d docs/user && echo "  docs/user/ exists" || (echo "  MISSING: docs/user/" && exit 1)
	@echo "  Documentation: PASS"
	@echo "  Phase 11: Operations PASS"
endif
	@echo "=== Phase $(PHASE) Gate PASSED ==="

# Pre-push check: mirrors CI pipeline locally (lint + test)
ci: lint test
	@echo "=== All CI checks passed ==="

# Benchmarks
BENCHMARK_RESULTS_DIR ?= benchmarks/results/local
REPORT_RESULTS_DIR ?= benchmarks/results/report-quality-live
REPORT_BASE_URL ?= https://sourcebridge-enterprise.xmojo.net
REPORT_REPO_NAME ?= MACU Residence

benchmark-comprehension-fake:
	uv run --project workers python -m workers.benchmarks.run_comprehension_bench --output-dir $(BENCHMARK_RESULTS_DIR)

benchmark-comprehension-local:
	uv run --project workers python -m workers.benchmarks.run_comprehension_bench --provider-mode live --output-dir $(BENCHMARK_RESULTS_DIR)

benchmark-comprehension-report:
	@test -f $(BENCHMARK_RESULTS_DIR)/report.md && cat $(BENCHMARK_RESULTS_DIR)/report.md || (echo "No benchmark report found at $(BENCHMARK_RESULTS_DIR)/report.md" && exit 1)

benchmark-report-quality-live:
	SOURCEBRIDGE_SECURITY_JWT_SECRET="$$(kubectl -n sourcebridge get secret sourcebridge-secrets -o jsonpath='{.data.SOURCEBRIDGE_SECURITY_JWT_SECRET}' | base64 -d)" \
	python3 benchmarks/report_quality/run_live_report_eval.py \
		--base-url $(REPORT_BASE_URL) \
		--repo-name "$(REPORT_REPO_NAME)" \
		--results-dir $(REPORT_RESULTS_DIR)

# Help
help:
	@echo "Available targets:"
	@echo "  build                        - Build Go binary and web app"
	@echo "  test                         - Run all tests"
	@echo "  lint                         - Run all linters"
	@echo "  proto                        - Generate protobuf code"
	@echo "  docker-build                 - Build Docker images"
	@echo "  docker-up                    - Start Docker Compose"
	@echo "  docker-down                  - Stop Docker Compose"
	@echo "  dev                          - Run Go server in dev mode"
	@echo "  dev-web                      - Run Next.js dev server"
	@echo "  clean                        - Remove build artifacts"
	@echo "  migrate                      - Run database migrations"
	@echo "  test-livingwiki-integration  - Run living-wiki Tier-1 e2e tests (in-memory fakes)"
	@echo "  test-livingwiki-smoke        - Run living-wiki Tier-2 smoke against live cluster"
	@echo "  help                         - Show this help"
