# 构建阶段
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o xbot .

# 运行阶段
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

ENV TZ=Asia/Shanghai

WORKDIR /app

COPY --from=builder /build/xbot .

# data/ 目录通过 volume 挂载
VOLUME ["/app/data"]

ENV LOG_LEVEL=info

CMD ["./xbot"]
