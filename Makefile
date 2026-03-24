.PHONY: all build build-go build-web build-worker test test-go test-web test-worker \
	lint lint-go lint-web lint-worker proto proto-clean docker-build docker-up docker-down \
	dev dev-web dev-go clean migrate help integration-test enterprise-test smoke-test phase-gate ci

GO_BIN = bin/sourcebridge
GO_MIGRATE_BIN = bin/migrate
PROTO_DIR = proto
GEN_DIR = gen

all: build

# Build
build: build-go build-web

build-go:
	go build -o $(GO_BIN) ./cmd/sourcebridge

build-web:
	cd web && npm ci && npm run build

build-worker:
	cd workers && uv sync

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
lint: lint-go lint-web lint-worker

lint-go:
	golangci-lint run ./...

lint-web:
	cd web && npm run lint

lint-worker:
	cd workers && uv run ruff check .

# Proto
PROTO_SOURCES = $(PROTO_DIR)/common/v1/types.proto \
	$(PROTO_DIR)/reasoning/v1/reasoning.proto \
	$(PROTO_DIR)/linking/v1/linking.proto \
	$(PROTO_DIR)/requirements/v1/requirements.proto \
	$(PROTO_DIR)/indexer/v1/indexer.proto \
	$(PROTO_DIR)/knowledge/v1/knowledge.proto

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

# Enterprise integration tests
enterprise-test:
	cd sourcebridge-enterprise && go test ./tests/integration/... -v -count=1 -timeout 120s

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
	@echo "Checking Phase 11: Enterprise & Operations..."
	@echo "  Running enterprise integration tests..."
	cd sourcebridge-enterprise && go test ./tests/integration/... -v -count=1 -timeout 120s
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
	@echo "  Phase 11: Enterprise & Operations PASS"
endif
	@echo "=== Phase $(PHASE) Gate PASSED ==="

# Pre-push check: mirrors CI pipeline locally (lint + test)
ci: lint test
	@echo "=== All CI checks passed ==="

# Help
help:
	@echo "Available targets:"
	@echo "  build        - Build Go binary and web app"
	@echo "  test         - Run all tests"
	@echo "  lint         - Run all linters"
	@echo "  proto        - Generate protobuf code"
	@echo "  docker-build - Build Docker images"
	@echo "  docker-up    - Start Docker Compose"
	@echo "  docker-down  - Stop Docker Compose"
	@echo "  dev          - Run Go server in dev mode"
	@echo "  dev-web      - Run Next.js dev server"
	@echo "  clean        - Remove build artifacts"
	@echo "  migrate      - Run database migrations"
	@echo "  help         - Show this help"
