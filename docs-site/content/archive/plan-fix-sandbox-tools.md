---
title: "plan-fix-sandbox-tools"
weight: 170
---

# 修复沙箱内置工具（Glob / Grep / Read / Edit / Skill）文件查找问题

> **版本**：v2（门下省审核修订版）
> **修订内容**：补充 edit.go / skill.go 修复；安全加固升级为必做；补充边界测试

## 1. 问题概述

在沙箱模式下，内置工具无法正确查找文件，核心原因是路径硬编码和 glob-to-find 翻译错误。

| 工具 | 现象 | 严重程度 |
|------|------|----------|
| Glob | `**/*.go`、`src/*.go` 等含路径的 pattern 永远匹配不到文件 | **致命** |
| Grep | `--include='*.{go,ts}'` 不生效，grep 不支持 brace expansion | **高** |
| Read | 路径硬编码 `/workspace`，未使用 `ctx.SandboxWorkDir` | **高** |
| Edit | 路径硬编码 `/workspace`，未使用 `ctx.SandboxWorkDir` | **高** |
| Skill | 沙箱 skill 目录硬编码 `/workspace/skills`、`/workspace/.skills` | **高** |

## 2. 根因分析

### 2.1 Glob：`find -name` 无法匹配路径中的 `/`

**文件**：`/workspace/xbot/tools/glob.go`，`executeInSandbox` 方法

```go
findCmd := fmt.Sprintf(
    "find %s -type f -name '%s' -not -path '*/.*' -not -path '*/node_modules/*' 2>/dev/null | head -200",
    searchDir, pattern)
```

`find -name` 只匹配文件 basename，**完全不支持路径分隔符 `/`**。

| Glob Pattern | 当前生成的 find 命令 | 结果 |
|---|---|---|
| `*.go` | `find /workspace -name '*.go'` | ❌ 递归搜索（用户期望仅当前目录） |
| `**/*.go` | `find /workspace -name '**/*.go'` | ❌ 永远不匹配（basename 不含 `/`） |
| `src/*.go` | `find /workspace -name 'src/*.go'` | ❌ 永远不匹配 |
| `**/test/*.go` | `find /workspace -name '**/test/*.go'` | ❌ 永远不匹配 |

### 2.2 Grep：brace 展开缺失

**文件**：`/workspace/xbot/tools/grep.go`，`executeInSandbox` 方法

```go
grepCmd += fmt.Sprintf(" --include='%s'", include)
```

当 `include="*.{go,ts}"` 时，生成 `--include='*.{go,ts}'`。grep 本身不支持 brace expansion。

注意：`expandBracePattern` 函数已在 `grep.go` 中实现，但仅在 `executeLocal` 中使用，`executeInSandbox` 未调用。

### 2.3 路径硬编码 `/workspace`（影响 4 个工具）

`ToolContext.SandboxWorkDir`（`tools/interface.go`）专门存储沙箱内工作目录，非沙箱时与 `WorkspaceRoot` 相同。以下文件均硬编码 `"/workspace"` 而未使用该字段：

| 文件 | 硬编码位置 | 正确参考 |
|------|------------|----------|
| `glob.go` | `executeInSandbox` 中 `searchDir := "/workspace"` | `cd.go` 使用 `ctx.SandboxWorkDir` |
| `grep.go` | `executeInSandbox` 中 `searchDir := "/workspace"` | 同上 |
| `read.go` | `executeInSandbox` 中多处 `"/workspace"` 拼接 | 同上 |
| `edit.go` | `executeInSandbox` 中 `"/workspace/" + params.Path` 等 | 同上 |

### 2.4 Skill 沙箱路径硬编码

**文件**：`/workspace/xbot/tools/skill.go`，约第 84-85 行

```go
{filepath.Join(ctx.WorkspaceRoot, "skills"), "/workspace/skills"},
{filepath.Join(ctx.WorkspaceRoot, ".skills"), "/workspace/.skills"},
```

当 `SandboxWorkDir` 不是 `/workspace` 时，skill 在沙箱内找不到。

### 2.5 Shell 注入风险（安全漏洞）

所有沙箱工具通过 `fmt.Sprintf` 将用户输入（pattern、searchDir、include）直接拼入 shell 命令，未做任何转义。攻击者可通过精心构造的 pattern 注入任意 shell 命令。

## 3. 修复方案

### 3.1 新增公共辅助函数（path_guard.go）

**位置**：`/workspace/xbot/tools/path_guard.go`

选择此文件是因为它已包含 `SandboxToHostPath` / `HostToSandboxPath` 等沙箱路径转换函数，语义内聚。

```go
// sandboxBaseDir 返回沙箱内的工作目录前缀。
// 优先使用 ctx.SandboxWorkDir，兜底为 "/workspace"。
func sandboxBaseDir(ctx *ToolContext) string {
    if ctx != nil && ctx.SandboxWorkDir != "" {
        return ctx.SandboxWorkDir
    }
    return "/workspace"
}

// shellEscape 对字符串进行 shell 单引号转义，防止命令注入。
// 将字符串中的单引号替换为 '\''
func shellEscape(s string) string {
    return strings.ReplaceAll(s, "'", "'\\''")
}
```

### 3.2 新增函数：`globToFindArgs`（glob.go）

**位置**：`/workspace/xbot/tools/glob.go`，新增函数

```go
// globToFindArgs 将 glob pattern 翻译为 find 命令的参数。
// 返回值：(find 搜索子目录, find 过滤参数片段)
//
// 翻译规则：
//   - *.go            → ("", "-maxdepth 1 -name '*.go'")
//   - **/*.go         → ("", "-name '*.go'")               // 递归
//   - src/*.go        → ("src", "-maxdepth 1 -name '*.go'")
//   - src/**/*.go     → ("src", "-name '*.go'")            // 递归
//   - **/test/*.go    → ("", "-path '*/test/*.go'")        // 递归
//   - src/**/test/*.go→ ("src", "-path '*/test/*.go'")     // 递归
func globToFindArgs(pattern string) (searchBase string, args string)
```

**算法**：

```go
func globToFindArgs(pattern string) (searchBase string, args string) {
    pattern = strings.Trim(pattern, "/")
    pattern = filepath.ToSlash(pattern)
    if pattern == "" {
        return "", ""
    }

    segments := strings.Split(pattern, "/")

    // 定位第一个 ** 的位置
    doubleStarIdx := -1
    for i, seg := range segments {
        if seg == "**" {
            doubleStarIdx = i
            break
        }
    }

    if doubleStarIdx == -1 {
        // 无 **：简单匹配，-maxdepth 1 限定不递归
        if len(segments) == 1 {
            return "", fmt.Sprintf("-maxdepth 1 -name '%s'", segments[0])
        }
        base := strings.Join(segments[:len(segments)-1], "/")
        name := segments[len(segments)-1]
        return base, fmt.Sprintf("-maxdepth 1 -name '%s'", name)
    }

    // 有 **：
    prefix := strings.Join(segments[:doubleStarIdx], "/")
    suffixSegments := segments[doubleStarIdx+1:]

    if len(suffixSegments) == 0 {
        return prefix, ""
    }

    if len(suffixSegments) == 1 {
        return prefix, fmt.Sprintf("-name '%s'", suffixSegments[0])
    }

    // 多个后缀 segment：用 -path
    pathPattern := "*/" + strings.Join(suffixSegments, "/")
    return prefix, fmt.Sprintf("-path '%s'", pathPattern)
}
```

### 3.3 修改 `GlobTool.executeInSandbox`（glob.go）

**修改要点**：
1. 使用 `sandboxBaseDir(ctx)` 替代硬编码
2. 调用 `globToFindArgs` 翻译 pattern
3. 对用户输入做 `shellEscape`

```go
func (t *GlobTool) executeInSandbox(ctx *ToolContext, pattern, path string) (*ToolResult, error) {
    sandboxBase := sandboxBaseDir(ctx)

    // 翻译 glob pattern → find 参数
    searchBase, findArgs := globToFindArgs(pattern)

    // 确定 find 搜索目录
    searchDir := sandboxBase
    if path != "" {
        if strings.HasPrefix(path, sandboxBase+"/") {
            searchDir = path
        } else {
            searchDir = sandboxBase + "/" + path
        }
    } else if ctx != nil && ctx.CurrentDir != "" {
        if strings.HasPrefix(ctx.CurrentDir, ctx.WorkspaceRoot) {
            rel, _ := filepath.Rel(ctx.WorkspaceRoot, ctx.CurrentDir)
            searchDir = sandboxBase + "/" + rel
        }
    }

    // 合并 globToFindArgs 的子目录前缀
    if searchBase != "" {
        searchDir = searchDir + "/" + searchBase
    }

    // 构建 find 命令（对 searchDir 做 shellEscape 防注入）
    escapedDir := shellEscape(searchDir)
    findCmd := fmt.Sprintf(
        "find %s -type f %s -not -path '*/.*' -not -path '*/node_modules/*' 2>/dev/null | head -200",
        escapedDir, findArgs)

    output, err := RunInSandboxWithShell(ctx, findCmd)
    // ... 其余不变
}
```

### 3.4 修改 `GrepTool.executeInSandbox`（grep.go）

**修改要点**：
1. 使用 `sandboxBaseDir(ctx)` 替代硬编码
2. 对 `include` 调用 `expandBracePattern` 展开 brace
3. 对用户输入 `pattern`、`searchDir` 做 `shellEscape`

```go
// include brace 展开（复用已有函数）
if include != "" {
    patterns := expandBracePattern(include)
    for _, p := range patterns {
        grepCmd += fmt.Sprintf(" --include='%s'", shellEscape(p))
    }
}

// pattern 和 searchDir 也需要 shellEscape
grepCmd += fmt.Sprintf(" %s %s", shellEscape(pattern), shellEscape(searchDir))
```

### 3.5 修改 `ReadTool.executeInSandbox`（read.go）

**修改要点**：将所有 `"/workspace"` 替换为 `sandboxBaseDir(ctx)`，对 `sandboxPath` 做 `shellEscape`。

```go
sandboxBase := sandboxBaseDir(ctx)
// ... 路径解析逻辑中使用 sandboxBase 替代 "/workspace" ...
cmd := fmt.Sprintf("cat '%s'", shellEscape(sandboxPath))
```

### 3.6 修改 `EditTool.executeInSandbox`（edit.go）

**文件**：`/workspace/xbot/tools/edit.go`

**修改要点**：将 `"/workspace"` 硬编码替换为 `sandboxBaseDir(ctx)`。

当前代码（约第 135-142 行）：
```go
if !strings.HasPrefix(params.Path, "/workspace/") && !strings.HasPrefix(params.Path, "/") {
    sandboxPath = "/workspace/" + params.Path
} else if strings.HasPrefix(params.Path, "/workspace/") {
    sandboxPath = params.Path
} else if strings.HasPrefix(params.Path, "/") {
    rel, _ := filepath.Rel(ctx.WorkspaceRoot, params.Path)
    sandboxPath = "/workspace/" + rel
}
```

修改为：
```go
sandboxBase := sandboxBaseDir(ctx)
if !strings.HasPrefix(params.Path, sandboxBase+"/") && !strings.HasPrefix(params.Path, "/") {
    sandboxPath = sandboxBase + "/" + params.Path
} else if strings.HasPrefix(params.Path, sandboxBase+"/") {
    sandboxPath = params.Path
} else if strings.HasPrefix(params.Path, "/") {
    rel, _ := filepath.Rel(ctx.WorkspaceRoot, params.Path)
    sandboxPath = sandboxBase + "/" + rel
}
```

### 3.7 修改 `SkillTool` 沙箱路径映射（skill.go）

**文件**：`/workspace/xbot/tools/skill.go`，约第 84-85 行

当前代码：
```go
candidates = []candidate{
    {filepath.Join(ctx.WorkspaceRoot, "skills"), "/workspace/skills"},
    {filepath.Join(ctx.WorkspaceRoot, ".skills"), "/workspace/.skills"},
}
```

修改为：
```go
sandboxBase := sandboxBaseDir(ctx)
candidates = []candidate{
    {filepath.Join(ctx.WorkspaceRoot, "skills"), filepath.Join(sandboxBase, "skills")},
    {filepath.Join(ctx.WorkspaceRoot, ".skills"), filepath.Join(sandboxBase, ".skills")},
}
```

## 4. 修改文件清单

| 文件 | 修改类型 | 具体变更 |
|------|----------|----------|
| `tools/path_guard.go` | 新增函数 | `sandboxBaseDir(ctx)` + `shellEscape(s)` |
| `tools/glob.go` | 新增函数 | `globToFindArgs(pattern) (string, string)` |
| `tools/glob.go` | 修改方法 | `executeInSandbox`：`globToFindArgs` + `sandboxBaseDir` + `shellEscape` |
| `tools/grep.go` | 修改方法 | `executeInSandbox`：brace 展开 + `sandboxBaseDir` + `shellEscape` |
| `tools/read.go` | 修改方法 | `executeInSandbox`：`sandboxBaseDir` + `shellEscape` |
| `tools/edit.go` | 修改方法 | `executeInSandbox`：`sandboxBaseDir` 替换硬编码 |
| `tools/skill.go` | 修改方法 | 沙箱路径映射使用 `sandboxBaseDir` |
| `tools/path_guard_test.go` | 新增测试 | `TestSandboxBaseDir` + `TestShellEscape` |
| `tools/glob_test.go` | 新增测试 | `TestGlobToFindArgs`（含边界 case） |

## 5. 测试策略

### 5.1 `globToFindArgs` 单元测试

| Pattern | searchBase | args |
|---------|------------|------|
| `*.go` | `""` | `-maxdepth 1 -name '*.go'` |
| `*.txt` | `""` | `-maxdepth 1 -name '*.txt'` |
| `*` | `""` | `-maxdepth 1 -name '*'` |
| `src/*.go` | `"src"` | `-maxdepth 1 -name '*.go'` |
| `pkg/utils/*.go` | `"pkg/utils"` | `-maxdepth 1 -name '*.go'` |
| `a/b/c/*.go` | `"a/b/c"` | `-maxdepth 1 -name '*.go'` |
| `**/*.go` | `""` | `-name '*.go'` |
| `**/*.ts` | `""` | `-name '*.ts'` |
| `src/**/*.go` | `"src"` | `-name '*.go'` |
| `**/test/*.go` | `""` | `-path '*/test/*.go'` |
| `src/**/test/*.go` | `"src"` | `-path '*/test/*.go'` |
| `**` | `""` | `""` |
| `src/**` | `"src"` | `""` |
| `**/*` | `""` | `-name '*'` |
| `/**/*.go` | `""` | `-name '*.go'` |
| `**/*.go/` | `""` | `-name '*.go'` |
| `""` | `""` | `""` |

### 5.2 `shellEscape` 单元测试

| 输入 | 期望输出 |
|------|----------|
| `hello` | `hello` |
| `hello world` | `hello world` |
| `it's` | `it'\\''s` |
| `"` | `"` |
| `\` | `\` |
| `$HOME` | `$HOME` |
| `""` | `""` |

### 5.3 `sandboxBaseDir` 单元测试

| ctx | 期望输出 |
|-----|----------|
| `nil` | `/workspace` |
| `SandboxWorkDir: "/data/ws"` | `/data/ws` |
| `SandboxWorkDir: ""` | `/workspace` |

### 5.4 现有测试

运行 `go test ./tools/...` 确保现有测试不受影响。

## 6. 影响范围评估

| 维度 | 评估 |
|------|------|
| **影响文件数** | 6 个（path_guard.go、glob.go、grep.go、read.go、edit.go、skill.go） |
| **影响工具数** | 5 个（Glob、Grep、Read、Edit、Skill） |
| **新增公共 API** | 2 个内部函数（`globToFindArgs`、`sandboxBaseDir`），不导出 |
| **对非沙箱模式的影响** | **无**。所有修改仅在沙箱相关代码路径内 |
| **对 SubAgent 的影响** | **无**。SubAgent 通过 `RunInSandbox` 接口执行 |
| **向后兼容性** | **完全兼容**。仅修复错误行为，不改变接口签名 |
| **回滚方案** | Git revert 即可。所有修改限定在沙箱路径内 |

## 7. 实施顺序

```
步骤 1: path_guard.go    → 新增 sandboxBaseDir()、shellEscape()          [无依赖]
步骤 2: glob.go           → 新增 globToFindArgs() + 重构 executeInSandbox  [依赖步骤 1]
步骤 3: grep.go           → 重构 executeInSandbox()（brace + escape）     [依赖步骤 1]
步骤 4: read.go           → 重构 executeInSandbox()（sandboxBaseDir）     [依赖步骤 1]
步骤 5: edit.go           → 重构 executeInSandbox()（sandboxBaseDir）     [依赖步骤 1]
步骤 6: skill.go          → 修复沙箱路径映射                               [依赖步骤 1]
步骤 7: 测试文件          → 新增单元测试                                   [依赖步骤 1-2]
步骤 8: 运行测试          → go test ./tools/... 验证通过                  [最后执行]
```

步骤 2-6 可并行实施。
