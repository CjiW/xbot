---
title: "沙箱指南"
weight: 10
---

# 沙箱指南

## 概览

xbot 支持多种沙箱模式，控制 Agent 执行 Shell 命令时的隔离级别。

| 模式 | 说明 | 适合 |
|------|------|------|
| `none` | 无隔离，直接在本机执行 | 个人开发机、Docker 内部 |
| `docker` | 每个用户一个隔离 Docker 容器 | 多用户服务器 |

## 配置

在 `~/.xbot/config.json` 中设置：

```json
{
  "sandbox": {
    "mode": "none"
  }
}
```

### none 模式（默认推荐给个人用户）

命令直接在本机执行。Windows 下使用 PowerShell。

```json
{
  "sandbox": {
    "mode": "none"
  }
}
```

> ⚠️ 无隔离意味着 Agent 可以执行任何你当前用户有权限执行的命令。请确保你信任 Agent 的行为。

### docker 模式

每个用户获得一个独立的 Docker 容器，文件系统持久化。

**前置条件：**

```bash
# 安装 Docker
sudo apt-get update && sudo apt-get install -y docker.io
sudo systemctl start docker && sudo systemctl enable docker
sudo usermod -aG docker $USER  # 需要重新登录
```

**配置：**

```json
{
  "sandbox": {
    "mode": "docker",
    "docker_image": "ubuntu:22.04"
  }
}
```

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `mode` | `"docker"` | 沙箱模式 |
| `docker_image` | `"ubuntu:22.04"` | Docker 镜像 |
| `host_work_dir` | `""` | 宿主机工作目录（映射到容器内） |
| `idle_timeout` | `"30m"` | 空闲超时（0 = 不自动销毁） |

### 远程沙箱

用户可以连接自己的远程 Runner，在自己的机器上执行命令。通过 CLI 的 `/settings` 面板配置。
