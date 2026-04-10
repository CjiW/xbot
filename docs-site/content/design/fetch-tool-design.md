---
title: "Fetch Tool Design"
weight: 40
---


## 1. 需求概述

为 LLM Agent 设计一个 fetch 工具，功能包括：
- 通过 URL 获取网页内容
- 将网页转换为 LLM 友好的格式（Markdown）
- 支持最大输出 token 限制，让 LLM 可以选择输出量
- 符合业界最佳实践
- **不依赖外部 API（自实现方案）**

## 2. 技术选型

### 方案对比

| 方案 | 优点 | 缺点 |
|------|------|------|
| **A. Jina AI Reader API** | 效果好，支持 JS 渲染 | 需要 API Key，依赖外部服务 |
| **B. go-readability 自实现** | 无需 API Key，完全离线，可定制 | 无法处理 JS 渲染的页面 |

**采用方案 B（自实现）**，原因：
1. 无需外部 API Key，部署更简单
2. 完全离线，不依赖外部服务
3. go-readability 是 Mozilla Readability 的 Go 移植，效果有保障

### 依赖库

| 库 | 用途 |
|---|------|
| `github.com/go-shiori/go-readability` | 提取网页正文（ Mozilla Readability Go 移植） |
| `github.com/tiktoken-go/tokenizer` | Token 计数（纯 Go 实现，无 CGO 依赖） |

## 3. 技术设计

### 3.1 工具接口

```go
type FetchTool struct {
    httpClient *http.Client
    maxSize    int64 // 最大响应大小 (10MB)
}
```

### 3.2 参数设计

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| url | string | 是 | - | 要获取的 URL |
| max_tokens | number | 否 | 4096 | 最大输出 token 数（max: 30000） |

### 3.3 实现流程

1. **URL 验证** → 拒绝非法/危险 URL
2. **发起 HTTP 请求** → 支持 context 取消，设置合理 User-Agent
3. **检查 Content-Type** → 仅处理 text/html
4. **读取响应** → 限制最大 10MB
5. **提取正文** → 使用 go-readability 转为 Markdown
6. **Token 截断** → 使用 tiktoken 计数，达到限制则截断

### 3.4 Token 截断策略

1. 全文提取后使用 tiktoken 计算 token 数
2. 如果超过 max_tokens，从后向前截断
3. 截断处添加 `...（内容已截断，已截取 {实际} / {限制} tokens）`

### 3.5 RegisterCore 集成

```go
// tools/interface.go DefaultRegistry()
r.RegisterCore(NewFetchTool())
```

## 4. 安全考虑

### 4.1 URL 验证（必须）

- **协议检查**：仅允许 `http://` 和 `https://`
- **内网IP检查**：拒绝以下 IP 范围：
  - `127.0.0.0/8` (loopback)
  - `10.0.0.0/8`
  - `172.16.0.0/12`
  - `192.168.0.0/16`
  - `169.254.0.0/16` (link-local)
  - `0.0.0.0/8`
- **域名检查**：拒绝 `localhost`、`localhost.localdomain`

### 4.2 请求限制

- 超时：30 秒
- 支持 context 取消
- 最大响应大小：10MB（超过则截断）

### 4.3 敏感信息

- 请求时不发送 Authorization header
- 响应中自动移除密码类 meta 标签（可选）

## 5. 输出格式

```markdown
# {页面标题}

**URL:** {页面URL}

---

{正文内容}

---

*已截取 {实际token数} / {限制token数} tokens*
```

如果被截断：
```markdown
# {页面标题}

**URL:** {页面URL}

---

{前文内容}...

---

*⚠️ 内容已截断（已截取 {实际} / {限制} tokens）*
```

## 6. 验证方法

- URL 验证：合法 URL、非法协议、内网 IP、localhost
- Token 截断：不同 max_tokens 值的截断行为
- 普通网页 → Markdown 格式标题+正文
- 长页面 → 截断在 max_tokens 处
- 非法 URL → 友好错误提示
