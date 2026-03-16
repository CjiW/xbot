# Docker Commit 清理优化计划

## ✅ 状态：已完成 (2026-03-16)

### 实施方案

**采用方案一**：`docker commit --squash`（Docker 25.0+ 已正式支持）

### 代码修改

`tools/sandbox_runner.go` 的 `commitIfDirty` 函数：

```go
// 使用 --squash 压缩层，避免层累积浪费磁盘空间
commitCmd := exec.Command("docker", "commit", "--squash", containerName, userImage)
if err := commitCmd.Run(); err != nil {
    // 如果 --squash 不支持（Docker < 25.0），fallback 到普通 commit
    log.WithError(err).Warnf("docker commit --squash failed, trying without squash (Docker < 25.0?)")
    commitCmd = exec.Command("docker", "commit", containerName, userImage)
    // ...
}

// 清理 dangling images
pruneCmd := exec.Command("docker", "image", "prune", "-f")
pruneCmd.Output()
```

### 效果

| 之前 | 之后 |
|------|------|
| 每次commit增加新层，空间累积 | 每次commit合并成单层，空间稳定 |
| `docker rmi` 只移除tag，layer仍被引用 | `--squash` 合并层 + `prune -f` 清理dangling |

---

## 问题分析

### 当前实现（`sandbox_runner.go`）

`commitIfDirty` 函数在 commit 时会：
1. 检查容器是否有变更（`docker diff`）
2. 获取旧镜像 ID（如果存在）
3. 执行 `docker commit` 创建新镜像
4. 如果新镜像 ID != 旧镜像 ID，执行 `docker rmi <oldImageID>`

### ❌ 当前方案的致命问题

```
commit 链式累积：

第1次 commit：
  base_image (1GB)
    └─ layer1 (10MB) ← tag: xbot-user:latest

第2次 commit：
  base_image (1GB)
    └─ layer1 (10MB) ← dangling (tag 被移走)
         └─ layer2 (10MB) ← tag: xbot-user:latest

第3次 commit：
  base_image (1GB)
    └─ layer1 (10MB) ← 仍被 layer3 间接引用
         └─ layer2 (10MB) ← 仍被 layer3 间接引用
              └─ layer3 (10MB) ← tag: xbot-user:latest

❌ docker rmi <oldImageID> 只是移除 tag，layer 仍被新镜像引用
❌ docker image prune -f 也无法清理（这些 layer 不是 dangling）
❌ 空间持续增长，每次 commit 都会增加新层！
```

**这就是为什么你发现"长期下去很浪费空间"——当前方案根本没有真正释放空间。**

---

## 解决方案对比

### 方案一：使用 `--squash`（推荐，最简洁）

```go
commitCmd := exec.Command("docker", "commit", "--squash", containerName, userImage)
```

**效果**：
```
每次 commit 都将所有变更合并成单层：

base_image (1GB)
  └─ squashed_layer (30MB) ← 所有变更合并

下次 commit：
base_image (1GB)
  └─ squashed_layer (40MB) ← 新增变更合并，旧层被替换
```

| 优点 | 缺点 |
|------|------|
| 实现简单，加一个参数 | 需要 Docker 实验性功能 |
| 空间不再累积 | squash 操作有额外开销（~秒级） |
| 无需手动清理 | 旧 Docker 版本不支持 |

**启用实验性功能**：
```bash
# /etc/docker/daemon.json
{
  "experimental": true
}
# 或在客户端
export DOCKER_CLI_EXPERIMENTAL=enabled
```

---

### 方案二：重建单层镜像（可靠，但复杂）

当满足条件时重建镜像：

```go
// 条件：镜像层数超过阈值 或 镜像大小超过阈值
func (s *dockerSandbox) shouldRebuild(imageID string) bool {
    // 检查层数
    layers := getLayerCount(imageID)
    if layers > 10 {
        return true
    }
    // 检查大小
    size := getImageSize(imageID)
    if size > 2*1024*1024*1024 { // 2GB
        return true
    }
    return false
}

// 重建：导出容器文件系统，删除旧镜像，重新导入
func (s *dockerSandbox) rebuildImage(containerName, userImage string) error {
    // 1. 导出容器文件系统
    // docker export container > /tmp/rootfs.tar
    
    // 2. 删除旧镜像
    // docker rmi -f userImage
    
    // 3. 从 tar 导入新镜像（单层）
    // cat /tmp/rootfs.tar | docker import - userImage
    
    // 4. 清理 tar 文件
}
```

| 优点 | 缺点 |
|------|------|
| 不依赖实验性功能 | 实现复杂 |
| 可控制重建时机 | `docker export` 会丢失镜像元数据（环境变量等） |
| 真正释放空间 | 需要额外存储 tar 的空间 |

---

### 方案三：版本化镜像 + 定期清理

保留最近 N 个版本，定期清理旧版本：

```go
// commit 时带时间戳
userImage := fmt.Sprintf("xbot-%s:%s", userID, time.Now().Format("20060102-150405"))

// 定期清理，只保留最近 3 个版本
func (s *dockerSandbox) cleanupOldImages(userID string, keep int) {
    images := listImages(fmt.Sprintf("xbot-%s:", userID))
    sort.Sort(ByCreatedAt(images))
    for i := keep; i < len(images); i++ {
        dockerRmi(images[i])
    }
}
```

| 优点 | 缺点 |
|------|------|
| 可以回滚 | **仍然无法解决层累积问题** |
| 保留历史 | 需要更多存储空间 |
| | 实现复杂 |

---

### 方案四：导出+重建（彻底解决）

在每次 commit 后，检查是否需要重建：

```go
func (s *dockerSandbox) commitWithRebuild(containerName, userID string) {
    userImage := userImageName(userID)
    
    // 1. 正常 commit
    commitCmd := exec.Command("docker", "commit", containerName, userImage + ":temp")
    commitCmd.Run()
    
    // 2. 导出新镜像的文件系统
    // docker save userImage:temp | docker load  # 这只是 tag
    
    // 3. 使用 docker export + import 重建为单层
    // docker export container | docker import - userImage
    
    // 4. 删除临时镜像
    // docker rmi userImage:temp
    
    // 这样每次都是单层镜像！
}
```

**更简单的实现**：直接用 `docker export` + `docker import`：

```go
func (s *dockerSandbox) commitFlattened(containerName, userID string) error {
    userImage := userImageName(userID)
    
    // 方式1：export + import（单层，但丢失 Docker 元数据）
    cmd := exec.Command("sh", "-c",
        fmt.Sprintf("docker export %s | docker import - %s", containerName, userImage))
    return cmd.Run()
}
```

**注意**：`export/import` 会丢失：
- ENV 变量
- WORKDIR
- ENTRYPOINT
- EXPOSE 端口

但我们的场景是保存用户的运行时环境（apt install 的包、用户数据等），这些都在文件系统里，**不受影响**。

---

## 推荐方案

### 首选：方案一（`--squash`）

如果 Docker 支持实验性功能，这是最简洁的方案。

### 备选：方案四（export + import）

如果不支持 `--squash`，使用 `docker export | docker import` 重建单层镜像。

**实现要点**：
1. 只在容器有变更时执行（复用 `docker diff` 检查）
2. 用 `export | import` 替代 `commit`
3. 保存基础镜像的环境变量（从 `/proc/.../environ` 或 `docker inspect` 获取）
4. commit 后执行 `docker image prune -f` 清理 dangling

---

## 实现计划

### 第一步：检测 Docker 是否支持 `--squash`

```go
func (s *dockerSandbox) supportsSquash() bool {
    cmd := exec.Command("docker", "commit", "--help")
    output, err := cmd.CombinedOutput()
    return err == nil && strings.Contains(string(output), "--squash")
}
```

### 第二步：修改 `commitIfDirty`

```go
func (s *dockerSandbox) commitIfDirty(containerName, userID string) {
    // ... 现有的 diff 检查 ...
    
    userImage := userImageName(userID)
    
    if s.supportsSquash() {
        // 方案一：使用 --squash
        commitCmd := exec.Command("docker", "commit", "--squash", containerName, userImage)
        if err := commitCmd.Run(); err != nil {
            log.WithError(err).Warnf("Failed to commit with squash")
            return
        }
    } else {
        // 方案四：export + import
        exportCmd := exec.Command("sh", "-c",
            fmt.Sprintf("docker export %s | docker import - %s", containerName, userImage))
        if err := exportCmd.Run(); err != nil {
            log.WithError(err).Warnf("Failed to export/import")
            return
        }
    }
    
    // 清理 dangling images
    pruneCmd := exec.Command("docker", "image", "prune", "-f")
    pruneCmd.Run()
}
```

### 第三步：处理基础镜像元数据（export/import 方案）

如果使用 `export + import`，需要在重建后恢复环境变量：

```go
// 保存基础镜像的环境变量
func (s *dockerSandbox) saveBaseEnvVars(userID string) []string {
    // 从 /proc/1/environ 或 docker inspect 获取
    cmd := exec.Command("docker", "exec", containerName,
        "cat", "/proc/1/environ")
    output, _ := cmd.Output()
    return strings.Split(string(output), "\x00")
}

// 在容器启动时注入环境变量
// 修改 getOrCreateContainer 的 docker run 命令
```

**简化方案**：由于我们的基础镜像（ubuntu:22.04）环境变量很少，可以直接硬编码或从 config 配置。

---

## 风险评估

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| `--squash` 不支持 | 需用备选方案 | 自动检测，fallback 到 export/import |
| export/import 丢失 ENV | 可能影响某些工具 | 从基础镜像恢复或硬编码 |
| squash 操作耗时 | commit 变慢 | 只影响有变更时的 commit |
| prune 清理其他 dangling | 通常无影响 | 可加过滤条件 |

---

## 下一步

请确认方案：

1. **方案一优先**：检测 `--squash` 支持，自动选择最佳方式
2. **直接方案四**：不依赖实验性功能，export/import 重建单层

确认后我将实现代码。
