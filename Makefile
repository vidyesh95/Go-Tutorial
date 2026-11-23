# Common tasks. Run `make help` to list them.
GO        ?= go
PKG       ?= ./...
LESSON    ?= ./01-fundamentals/01hello

.PHONY: help run build fmt vet test test-race cover lint tidy sqlc migrate-up migrate-down clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-14s %s\n",$$1,$$2}'

run: ## Run a lesson: make run LESSON=./01-fundamentals/13range
	$(GO) run $(LESSON)

build: ## Compile every package
	$(GO) build $(PKG)

fmt: ## Format all Go code
	gofmt -s -w .

vet: ## Static checks from the Go toolchain
	$(GO) vet $(PKG)

test: ## Run all tests
	$(GO) test $(PKG)

test-race: ## Run all tests with the race detector
	$(GO) test -race $(PKG)

cover: ## Coverage report -> coverage.html
	$(GO) test -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -html=coverage.out -o coverage.html

lint: ## golangci-lint (install: https://golangci-lint.run/welcome/install/)
	golangci-lint run

tidy: ## Sync go.mod/go.sum
	$(GO) mod tidy

sqlc: ## Regenerate type-safe SQL code
	$(GO) tool sqlc generate -f 03-sql-sqlite/09sqlc/sqlc.yaml
	$(GO) tool sqlc generate -f 05-projects/taskapi/sqlc.yaml

clean: ## Remove build output and local databases
	rm -rf bin tmp coverage.out coverage.html
	find . -name '*.db' -delete
