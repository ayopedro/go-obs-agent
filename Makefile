# Targets that run the app load .env first (copy .env.example to .env).
SHELL := /bin/bash
LOAD_ENV := set -a; [ -f .env ] && . ./.env; set +a;
IMAGE ?= go-obs-agent
CONTAINER ?= docker

.PHONY: help run dev demo api test cover build image container-run
help: ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

run: ## Run the server with .env loaded
	@$(LOAD_ENV) go run ./cmd/server

dev: ## Run the server with live reload (needs air: go install github.com/air-verse/air@latest)
	@$(LOAD_ENV) air

demo: ## Serve synthetic telemetry on 127.0.0.1:4319
	go run ./cmd/demo

api: ## One-off CLI question: make api PROMPT='Investigate trace ...'
	@$(LOAD_ENV) go run ./cmd/api "$(PROMPT)"

test: ## Run tests
	go test ./...

cover: ## Run tests with a per-function coverage summary
	go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

build: ## Build binaries into ./bin
	go build -o bin/ ./cmd/server ./cmd/api ./cmd/demo

image: ## Build the container image (CONTAINER=podman to use podman)
	$(CONTAINER) build -t $(IMAGE) .

container-run: ## Run the image with .env on 127.0.0.1:8080
	$(CONTAINER) run --rm --env-file .env -e HTTP_ADDR=0.0.0.0:8080 -p 127.0.0.1:8080:8080 $(IMAGE)
