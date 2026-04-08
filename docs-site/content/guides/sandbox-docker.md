---
title: "Sandbox Docker"
weight: 10
---

# Docker 沙箱模式使用指南

## 概述

xbot 支持两种沙箱模式：
- `none` - 无沙箱，直接执行命令
- `docker` - Docker 容器隔离（默认）

本文档介绍 Docker 模式的使用方法。

## 环境要求

1. **安装 Docker**
   ```bash
   # Ubuntu/Debian
   sudo apt-get update
   sudo apt-get install -y docker.io

   # 启动 Docker 服务
   sudo systemctl start docker
   sudo systemctl enable docker

   # 将当前用户加入 docker 组（需要重新登录生效）
   sudo usermod -aG docker $USER
   ```

2. **配置环境变量**

   在 `.env` 文件中添加：
   ```bash
   # 沙箱模式：none / docker
   SANDBOX_MODE=docker

   # Docker 镜像（可选，默认 ubuntu:22.04）
   SANDBOX_DOCKER_IMAGE=ubuntu:22.04
   ```

## 工作原理

### 容器隔离

每个用户拥有独立的 Docker 容器：
- 容器命名格式：`xbot-{user_id}`
- 用户之间完全隔离
- 容器按需创建，启动后保持运行

### 环境持久化原理（docker commit）

```
┌─────────────────────────────────────────────────────────────┐
│                        生命周期                               │
│                                                              │
│   1. 首次使用                                                │
│      基础镜像 (ubuntu:22.04)                                  │
│            │                                                  │
│            ▼                                                  │
│      创建容器 xbot-{user_id}                                  │
│            │                                                  │
│            ▼                                                  │
│      用户操作（apt install, pip install 等）                   │
│                                                              │
│   2. 关闭时                                                  │
│      docker commit xbot-{user_id} xbot-{user_id}:latest     │
│            │                                                  │
│            ▼                                                  │
│      stop + rm 容器                                           │
│                                                              │
│   3. 再次使用                                                │
│      检测到用户镜像 xbot-{user_id}:latest                     │
│            │                                                  │
│            ▼                                                  │
│      用该镜像创建新容器 ── 所有环境完整恢复                     │
└─────────────────────────────────────────────────────────────┘
```

### 为什么安装的软件不会消失？

xbot 使用 **docker commit** 将容器的完整文件系统保存为用户专属镜像：

1. **完整持久化** — 所有文件系统变更都会被保存，包括：
   - `apt-get install` 安装的系统级包
   - `pip install` / `npm install -g` 等语言级包
   - 编译安装的软件（`make install`）
   - 配置文件修改（`.bashrc`、`/etc` 下的配置等）

2. **透明恢复** — 下次创建容器时自动使用已提交的镜像，用户无感知

3. **支持的安装方式**
   ```bash
   # 系统包
   apt-get update && apt-get install -y golang-go python3 git

   # Python 包
   pip install numpy pandas

   # Node.js 全局包
   npm install -g typescript

   # 编译安装
   ./configure && make && make install
   ```

## 使用示例

### 首次使用：安装开发环境

```bash
apt-get update
apt-get install -y wget python3 python3-pip

pip install numpy pandas
```

### 后续使用：环境已就绪

再次启动时，已安装的工具可以直接使用：
```bash
python3 -c "import numpy; print(numpy.__version__)"
```

## 容器生命周期

- **按需创建**：首次执行命令时自动创建容器
- **持续运行**：容器保持运行状态（`tail -f /dev/null`）
- **自动恢复**：容器停止后自动启动（通过 `docker start`）
- **自动提交**：xbot 关闭时自动 `docker commit` 保存环境
- **手动清理**：如需重置环境，删除容器和用户镜像：
  ```bash
  docker rm -f xbot-{user_id}
  docker rmi xbot-{user_id}:latest
  ```

## 故障排查

### Docker 命令不可用

```bash
# 检查 Docker 服务状态
sudo systemctl status docker

# 检查当前用户是否有 Docker 权限
docker ps
```

### 容器创建失败

```bash
# 查看容器日志
docker logs xbot-{user_id}

# 检查镜像是否存在
docker images
```

### 清理所有用户的容器和镜像

```bash
# 列出所有 xbot 容器
docker ps -a --filter "name=xbot-"

# 删除所有 xbot 容器
docker rm -f $(docker ps -aq --filter "name=xbot-")

# 删除所有 xbot 用户镜像
docker rmi $(docker images --format '{{.Repository}}:{{.Tag}}' | grep '^xbot-')
```

## 性能考量

- **冷启动**：首次执行需要拉取镜像和创建容器（约 10-30 秒）
- **热执行**：容器运行后，命令执行与直接执行无明显差异
- **docker commit**：通常在 1-2 秒内完成（仅保存文件系统差异层）
- **资源占用**：每个活跃用户占用一个容器，用户镜像占用磁盘空间（增量存储）
