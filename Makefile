.PHONY: fmt lint test build run dev clean ci

BINARY_NAME := xbot

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

test:
	go test -v -race -coverprofile=coverage.out ./...

build:
	go build -o $(BINARY_NAME) .

run: build
	./$(BINARY_NAME)

dev:
	go run .

clean:
	rm -f $(BINARY_NAME) coverage.out
	go clean

ci: lint build test
	@echo "CI checks passed!"
