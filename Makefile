.PHONY: fmt lint test build run dev clean ci clean-memory web-build web-lint web-dev

BINARY_NAME := xbot

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

test:
	go test -v -race -coverprofile=coverage.out ./...

VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -X xbot/version.Version=$(VERSION) -X xbot/version.Commit=$(shell git rev-parse --short HEAD) -X xbot/version.BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY_NAME) .

run: build
	./$(BINARY_NAME)

dev:
	go run -ldflags "$(LDFLAGS)" .

clean:
	rm -f $(BINARY_NAME) coverage.out
	go clean

ci: lint build test web-lint web-build
	@echo "CI checks passed!"

clean-memory:
	rm -rf .xbot/
	@echo "Memory cleaned!"

web-build:
	cd web && yarn build

web-lint:
	cd web && yarn lint

web-dev:
	cd web && yarn dev

