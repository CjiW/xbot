# Docker 沙箱模式使用指南

## 概述

xbot 支持三种沙箱模式：
- `none` - 无沙箱，直接执行命令
- `bwrap` - 使用 bwrap 隔离（需要安装 bwrap）
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
   # 沙箱模式：none / bwrap / docker
   SANDBOX_MODE=docker

   # Docker 镜像（可选，默认 ubuntu:22.04）
   SANDBOX_DOCKER_IMAGE=ubuntu:22.04

   # 持久化卷目录（可选）
   SANDBOX_DOCKER_VOLUME_DIR=.xbot/sandbox
   ```

## 工作原理

### 容器隔离

每个用户拥有独立的 Docker 容器：
- 容器命名格式：`xbot-{user_id}`
- 用户之间完全隔离
- 容器按需创建，启动后保持运行

### 环境持久化原理

```
┌─────────────────────────────────────────────────────────────┐
│                      宿主机                                  │
│                                                              │
│   ┌─────────────────────────────────────────────────────┐   │
│   │              Docker Volume: xbot-{user_id}          │   │
│   │   ┌────────────┐  ┌────────────┐  ┌───────────┐   │   │
│   │   │   /root    │  │ /usr/local │  │   /opt    │   │   │
│   │   │  (用户数据)  │  │  (系统级)  │  │ (系统级)  │   │   │
│   │   └────────────┘  └────────────┘  └───────────┘   │   │
│   └─────────────────────────────────────────────────────┘   │
│                              ▲                              │
│                              │ 挂载                          │
│   ┌──────────────────────────┴──────────────────────────┐   │
│   │              容器内文件系统                          │   │
│   │   /root ──────────► Docker Volume                  │   │
│   │   /usr/local ──────► 符号链接 ──► /root/.local/usr_local │   │
│   │   /opt ────────────► 符号链接 ──► /root/.local/opt     │   │
│   │   /workspace ──────► 挂载宿主机工作区               │   │
│   └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

### 为什么安装的软件不会消失？

xbot 使用**符号链接**技术解决系统级安装的持久化问题：

1. **自动符号链接**
   - 容器首次启动时，自动创建以下符号链接：
     - `/usr/local` -> `/root/.local/usr_local`
     - `/opt` -> `/root/.local/opt`

2. **持久化原理**
   - 所有安装到 `/usr/local`（如 `./configure && make install`）的文件
   - 所有安装到 `/opt` 的软件
   - 都会通过符号链接写入 Docker Volume
   - 容器重启后，Volume 重新挂载，数据完好

3. **支持的安装方式**
   ```bash
   # 方式一：apt-get 安装（会自动使用 /usr/local）
   apt-get install -y golang-go

   # 方式二：编译安装（默认安装到 /usr/local）
   ./configure && make && make install

   # 方式三：手动移动到 /opt
   mv myapp /opt/

   # 方式四：用户级工具（推荐）
   pip install --user numpy
   conda install python
   ```

## 使用示例

### 首次使用：安装开发环境

```bash
# 安装 Golang
apt-get update
apt-get install -y wget
wget https://go.dev/dl/go1.21.0.linux-amd64.tar.gz
tar -C /root -xzf go1.21.0.linux-amd64.tar.gz
export PATH=$PATH:/root/go/bin

# 安装 Python
apt-get install -y python3 python3-pip
pip install numpy pandas
```

### 后续使用：环境已就绪

再次启动时，已安装的工具可以直接使用：
```bash
go version    # 直接可用
python3 -c "import numpy; print(numpy.__version__)"
```

## 容器生命周期

- **按需创建**：首次执行命令时自动创建容器
- **持续运行**：容器保持运行状态（`tail -f /dev/null`）
- **自动恢复**：容器停止后自动启动（通过 `docker start`）
- **手动清理**：如需重置环境，删除容器和卷：
  ```bash
  docker rm -f xbot-{user_id}
  docker volume rm xbot-{user_id}
  ```

**注意**：不要手动删除容器，否则所有已安装的软件会丢失。正确做法是让容器保持运行，xbot 会在需要时自动启动已停止的容器。

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

### 清理所有用户的容器

```bash
# 列出所有 xbot 容器
docker ps -a --filter "name=xbot-"

# 删除所有 xbot 容器
docker rm -f $(docker ps -aq --filter "name=xbot-")

# 删除所有 xbot 卷
docker volume rm $(docker volume ls -q --filter "name=xbot-")
```

## 性能考量

- **冷启动**：首次执行需要拉取镜像和创建容器（约 10-30 秒）
- **热执行**：容器运行后，命令执行与直接执行无明显差异
- **资源占用**：每个活跃用户占用一个容器，建议监控 Docker 资源使用
