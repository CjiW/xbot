---
title: "MCP Protocol"
weight: 50
---


xbot supports the [Model Context Protocol](https://modelcontextprotocol.io/) for integrating external tools. MCP servers expose tools that the agent can discover and use at runtime.

## Overview

- **Global servers** — Always-on, configured in `.xbot/mcp.json`
- **Session servers** — Dynamically added/removed at runtime via the `ManageTools` tool
- **Transports** — stdio and HTTP (SSE)
- **Cleanup** — Inactivity-based lazy cleanup for session servers

## Configuration

### Global MCP Servers

Create `~/.xbot/mcp.json`:

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"],
      "description": "File system access"
    },
    "web-search": {
      "url": "http://localhost:3001/sse",
      "description": "Web search service"
    }
  }
}
```

### Runtime Management

The agent manages MCP servers dynamically:

```
ManageTools(action="add_mcp", name="my-server", mcp_config="{...}")
ManageTools(action="remove_mcp", name="my-server")
ManageTools(action="list_mcp")
ManageTools(action="reload")
```

## Tool Lifecycle

1. **Discovery** — MCP server tools appear in the tool catalog with stub schemas
2. **Activation** — `load_tools` loads the full parameter schema for a specific tool
3. **Execution** — The agent calls the tool; xbot proxies the request to the MCP server
4. **Cleanup** — Session servers with no activity are cleaned up after the inactivity timeout

## Tool Naming

MCP tools follow the pattern: `mcp_<server_name>_<tool_name>`

For example, a tool `read_file` from server `filesystem` becomes `mcp_filesystem_read_file`.

## Feishu MCP Tools

xbot includes a built-in MCP server group for Feishu API integration (20+ tools for wiki, bitable, docx, drive, and file operations). These are channel-restricted to Feishu only and require user OAuth authorization.

## Security Considerations

- MCP servers run as separate processes (stdio) or connect to external services (HTTP)
- Tool execution is sandboxed according to the configured sandbox mode
- File path guards prevent access outside the workspace
- HTTP-based servers should use authentication tokens
