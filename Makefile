.PHONY: fmt test build run clean web-install web-dev web-build all

BINARY_NAME := xbot
PORT ?= 8080

# Go 相关
fmt:
	go fmt ./...

test:
	go test -v ./...

build:
	go build -o $(BINARY_NAME) .

run: build
	./$(BINARY_NAME)

dev:
	go run .

clean-db:
	rm -rf .xbot MEMORY.md HISTORY.md cron.json

clean:
	rm -f $(BINARY_NAME)
	go clean
	rm -rf web/dist web/node_modules

# 前端相关
web-install:
	cd web && npm install --legacy-peer-deps

web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build

# 完整构建
all: web-build build
	@echo "Build complete! Run ./$(BINARY_NAME) to start."

all-dev:
	$(MAKE) web-dev &
	$(MAKE) dev

# API 测试命令
.PHONY: create-session list-sessions health

create-session:
	@curl -s -X POST http://localhost:$(PORT)/api/sessions | jq .

list-sessions:
	@curl -s http://localhost:$(PORT)/api/sessions | jq .

health:
	@curl -s http://localhost:$(PORT)/health | jq .


