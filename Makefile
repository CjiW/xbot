.PHONY: fmt test build run clean web-install web-dev web-build all docker-dev docker-build docker-stop

BINARY_NAME := xbot
IMAGE_NAME := xbot
CONTAINER_NAME := xbot-dev
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

# Docker 相关
docker-build:
	docker build -t $(IMAGE_NAME) .

docker-dev: docker-build docker-stop
	docker run -d \
		--name $(CONTAINER_NAME) \
		--env-file .env \
		-v $(CURDIR)/data:/work \
		--restart unless-stopped \
		$(IMAGE_NAME)
	@echo "Container $(CONTAINER_NAME) started. Logs: make docker-logs"

docker-logs:
	docker logs -f $(CONTAINER_NAME)

docker-stop:
	@docker rm -f $(CONTAINER_NAME) 2>/dev/null || true
