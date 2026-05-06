SHELL := /bin/bash
GO ?= go
BINARY := bin/platform
PKG := ./...
ATLAS := atlas

.PHONY: build test lint generate migrate migrate-diff migrate-lint migrate-status run docker-build clean tidy proto-lint proto-generate proto-breaking integration

build:
	$(GO) build -o $(BINARY) ./cmd/platform

test:
	$(GO) test -race -count=1 $(PKG)

lint:
	$(GO) vet $(PKG)

generate:
	$(GO) generate $(PKG)

migrate:
	atlas migrate apply --env $${ATLAS_ENV:-local}

migrate-diff:
	atlas migrate diff $${NAME:-change} --env local

migrate-lint:
	atlas migrate lint --env local --latest 1

migrate-status:
	atlas migrate status --env local

run: build
	./$(BINARY)

docker-build:
	docker compose build

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin/

proto-lint:
	cd internal/connector/proto && buf lint

proto-generate:
	cd internal/connector/proto && buf generate

proto-breaking:
	cd internal/connector/proto && buf breaking --against ".git#branch=master,subdir=internal/connector/proto"

integration:
	docker compose up -d postgres
	@until docker compose exec -T postgres pg_isready -U platform >/dev/null 2>&1; do sleep 1; done
	atlas migrate apply --env local
	JWT_SIGNING_KEY="$${JWT_SIGNING_KEY:-$$(openssl rand -base64 48)}" \
	DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/platform?sslmode=disable \
	$(GO) run ./cmd/platform start &
	@for i in $$(seq 1 30); do curl -fsS http://localhost:8080/health >/dev/null && break || sleep 1; done
	JWT_SIGNING_KEY="$${JWT_SIGNING_KEY:-$$(openssl rand -base64 48)}" \
	INTEGRATION_DATABASE_URL=postgres://platform_app:platform_app@localhost:5432/platform?sslmode=disable \
	PLATFORM_URL=http://localhost:8080 \
	$(GO) test -tags=integration -race -count=1 ./test/integration/...
	-pkill -f "platform start" || true
	docker compose down -v