# SubAgent Role Loader Reference

## File Location Resolution

```
Search order (first match wins):
1. User-private: {WorkDir}/.xbot/users/{senderID}/workspace/agents/{name}.md
2. Synced global: {WorkspaceRoot}/.agents/{name}.md  (copied from global by skill_sync)
3. Host global:  {AgentsDir}/{name}.md               (configured at startup)
```

## Frontmatter Parser Details

The parser (`tools/subagent_loader.go`) handles:

- **BOM-safe**: strips `\xef\xbb\xbf` prefix
- **Must start with** `---` on the first line
- **Closes with** `\n---` (newline before closing delimiter)
- **Key-value**: `key: value` at column 0
- **Lists**: `- item` at column 0 (indented `- item` under a parent key also works)
- **Capabilities sub-fields**: indented under `capabilities:` key
- **String quoting**: strips surrounding `"` or `'`

### Supported capabilities keys

| Key | Maps to | Values |
|-----|---------|--------|
| `memory` | `SubAgentCapabilities.Memory` | true/yes/1 |
| `send_message` | `SubAgentCapabilities.SendMessage` | true/yes/1 |
| `spawn_agent` | `SubAgentCapabilities.SpawnAgent` | true/yes/1 |

### Fallback behavior

- If `name` is empty in frontmatter, falls back to filename without `.md`
- If `tools` list is empty, the agent receives ALL available tools (no restriction)
- If `capabilities` section is missing, all capabilities default to `false`

## SubAgentRole struct

```go
type SubAgentRole struct {
    Name         string
    Description  string
    SystemPrompt string
    AllowedTools []string
    Capabilities SubAgentCapabilities
}
```

## How Roles Are Used

1. **System prompt injection**: `AgentStore.GetAgentsCatalog(senderID)` scans all directories and generates `<available_agents>` XML for the system prompt
2. **Tool execution**: `SubAgentTool.Execute()` calls `GetSubAgentRole(name, dirs...)` to load the role, then passes `role.SystemPrompt` and `role.AllowedTools` to the agent engine
3. **Hot reload**: roles are loaded from disk on every call (no restart needed to pick up changes)
