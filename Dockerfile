# 构建阶段
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o xbot .

# 运行阶段
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

ENV TZ=Asia/Shanghai

# 二进制目录
RUN mkdir -p /app /data /work

COPY --from=builder /build/xbot /app/xbot

# 数据持久化卷
VOLUME ["/data"]

# 工具执行的工作目录
WORKDIR /work

# 配置路径环境变量
ENV DATA_DIR=/data
ENV WORK_DIR=/work
ENV LOG_LEVEL=info

CMD ["/app/xbot"]
