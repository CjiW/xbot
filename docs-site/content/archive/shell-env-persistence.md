---
title: "shell-env-persistence"
weight: 260
---


## 问题背景

当用户在沙箱容器内执行安装程序（如安装 Go、Node.js）时，安装脚本会修改 `~/.bashrc`、`/etc/profile` 等配置文件，但这些环境变量在后续命令中不生效。

### 核心问题

1. **当前方案**：使用 `bash -l -c` 执行命令
   - 问题：假设容器内有 bash，但某些镜像（如 Alpine）只有 `sh`
   
2. **错误方案**：使用 `sh -c ". ~/.bashrc"`
   - 问题：`~/.bashrc` 可能包含 `source` 等 bash 专用语法，`sh` 无法解析
   
3. **环境变量获取错误**：使用 `$SHELL`
   - 问题：`$SHELL` 是宿主机的环境变量，不是容器内的

## 正确方案：检测容器内用户的默认 Shell

### 核心思路

从容器内的 `/etc/passwd` 获取用户的默认 shell，然后用该 shell 的 login mode 执行命令。

```bash
# 示例命令
docker exec -it <container> $(docker exec <container> grep "^root:" /etc/passwd | cut -d: -f7)
```

### 详细设计

#### 1. 数据结构变更

```go
type dockerContainer struct {
    name    string
    started bool
    shell   string  // 新增：缓存的用户默认 shell
}
```

#### 2. Shell 检测逻辑

```go
// 在容器创建后，获取用户的默认 shell
func (s *dockerSandbox) detectShell(containerName string) string {
    cmd := exec.Command("docker", "exec", containerName, 
        "sh", "-c", "grep '^root:' /etc/passwd | cut -d: -f7")
    output, err := cmd.Output()
    if err != nil || len(strings.TrimSpace(string(output))) == 0 {
        return "/bin/sh"  // fallback
    }
    return strings.TrimSpace(string(output))
}
```

#### 3. Wrap 方法变更

```go
func (s *dockerSandbox) Wrap(...) (string, []string, error) {
    // ... 获取容器 ...
    
    // 获取缓存的 shell
    shell := "/bin/sh"
    if c, ok := s.containers[userID]; ok && c.shell != "" {
        shell = c.shell
    }
    
    dockerArgs := []string{
        "exec",
        "-i",
        "-w", "/workspace",
        containerName,
        shell, "-l", "-c",  // login shell，自动加载所有配置
        command,
    }
    // ...
}
```

#### 4. 文件执行方式变更

由于使用 login shell，命令格式需要调整：

```go
// 之前
dockerArgs = append(dockerArgs, containerName, command)
dockerArgs = append(dockerArgs, args...)

// 之后（使用 shell -l -c）
shellCmd := command
if len(args) > 0 {
    shellCmd = command + " " + strings.Join(args, " ")
}
dockerArgs = append(dockerArgs, containerName, shell, "-l", "-c", shellCmd)
```

### Shell 兼容性矩阵

| Shell | Login Mode 行为 |
|-------|----------------|
| **bash** | `-l` 加载 `/etc/profile`, `~/.bash_profile`, `~/.bashrc` |
| **zsh** | `-l` 加载 `/etc/zprofile`, `~/.zprofile`, `~/.zshrc` |
| **sh** | `-l` 大多忽略，需要手动 source（但 POSIX shell 通常不依赖 rc 文件） |
| **ash/dash** | `-l` 行为类似 sh |

### 环境变量持久化

`export VAR=value` 持久化到 `~/.xbot_env` 的逻辑保持不变：
1. 解析命令中的 `export` 语句
2. 写入 `~/.xbot_env`
3. 确保 `~/.bashrc` 或 `~/.zshrc` source `~/.xbot_env`

### 边界情况处理

1. **容器无 shell 字段**：fallback 到 `/bin/sh`
2. **login shell 不支持**：某些最小化镜像可能不支持 `-l`，可尝试不用 `-l`
3. **配置文件语法错误**：用户配置文件可能有错误，需要静默忽略

## 实现步骤

1. 修改 `dockerContainer` 结构体，添加 `shell` 字段
2. 在 `getOrCreateContainer` 中，容器启动后检测 shell
3. 修改 `Wrap` 方法，使用缓存的 shell 执行命令
4. 修改 `shell.go`，移除硬编码的 `bash -l`，改用沙箱提供的 shell
5. 更新测试用例

## 测试用例

```bash
# Alpine 镜像（只有 sh）
docker run --rm alpine sh -c "grep '^root:' /etc/passwd | cut -d: -f7"
# 输出: /bin/sh

# Ubuntu 镜像
docker run --rm ubuntu grep '^root:' /etc/passwd | cut -d: -f7
# 输出: /bin/bash

# 安装后环境变量生效测试
# 1. 执行安装脚本（修改 ~/.bashrc）
# 2. 后续命令中环境变量应该生效
```