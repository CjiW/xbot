# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Install build dependencies (no gcc needed - using pure Go SQLite)
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary (no CGO needed - using pure Go SQLite)
RUN GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown") && \
    BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags "-X xbot/version.Commit=${GIT_COMMIT} -X xbot/version.BuildTime=${BUILD_TIME}" \
    -o xbot .

# Final stage
FROM node:22-alpine

RUN apk --no-cache add ca-certificates git tzdata docker-cli && \
    ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

ENV TZ=Asia/Shanghai

WORKDIR /app

# Copy the binary and prompt from builder
COPY --from=builder /build/xbot /app/xbot
COPY --from=builder /build/prompt.md /app/prompt.md

# Bundle default skills and agents so they're available out-of-the-box
COPY --from=builder /build/.xbot.example/skills/ /app/.xbot/skills/
COPY --from=builder /build/.xbot.example/agents/ /app/.xbot/agents/

ENTRYPOINT ["/app/xbot"]
