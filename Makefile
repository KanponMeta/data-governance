SHELL := /bin/bash
GO ?= go
BINARY := bin/platform
PKG := ./...
ATLAS := atlas

.PHONY: build test lint generate migrate migrate-diff migrate-lint migrate-status run docker-build clean tidy proto-lint proto-generate proto-breaking

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