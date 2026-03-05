# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown") && \
    BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags "-X xbot/version.Commit=${GIT_COMMIT} -X xbot/version.BuildTime=${BUILD_TIME}" \
    -o xbot .

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates git

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /build/xbot /app/xbot

# Create working directory
RUN mkdir -p /data && chmod 777 /data

WORKDIR /data

ENTRYPOINT ["/app/xbot"]
