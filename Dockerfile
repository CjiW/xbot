# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Install build dependencies (including gcc for CGO)
RUN apk add --no-cache git gcc musl-dev

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with CGO enabled
RUN CGO_ENABLED=1 GOOS=linux go build -o xbot .

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates git

WORKDIR /app

# Copy the binary and prompt from builder
COPY --from=builder /build/xbot /app/xbot
COPY --from=builder /build/prompt.md /app/prompt.md

# Create working directory
RUN mkdir -p /data && chmod 777 /data

WORKDIR /data

ENTRYPOINT ["/app/xbot"]
