SHELL := /bin/bash
GO ?= go
BINARY := bin/platform
PKG := ./...

.PHONY: build test lint generate migrate run docker-build clean tidy

build:
	$(GO) build -o $(BINARY) ./cmd/platform

test:
	$(GO) test -race -count=1 $(PKG)

lint:
	$(GO) vet $(PKG)

generate:
	$(GO) generate $(PKG)

migrate:
	$(GO) run ./cmd/platform migrate

run: build
	./$(BINARY)

docker-build:
	docker compose build

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin/