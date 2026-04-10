---
title: "Permission Control"
weight: 40
---

# Permission Control

OS user-based permission control for tool execution. Restricts which OS users the agent can execute commands as, with optional approval workflows for privileged operations.

## Overview

When permission control is enabled, the agent can execute tools as different OS users via the `run_as` parameter. Sensitive operations (e.g., running as root) require user approval before execution.

## Setup

### 1. Configure Users

Set the permission users via per-user settings:

```
/settings set default_user user
/settings set privileged_user root
```

| Setting | Description |
|---------|-------------|
| `default_user` | Non-privileged user. Tool execution proceeds without approval. |
| `privileged_user` | Privileged user (e.g., `root`). Requires user approval before execution. |

### 2. Configure Sudoers

Run the setup script to configure NOPASSWD sudo entries:

```bash
sudo bash scripts/setup-perm-control.sh --default-user user --privileged-user root
```

### 3. Enable

Permission control activates automatically when `default_user` or `privileged_user` is set. When enabled:

- All raw `sudo` commands are blocked (use `run_as` instead)
- `run_as` and `reason` must be provided together
- Executing as `privileged_user` triggers an approval workflow

## Behavior

### sudo Blocking

When permission control is enabled, any raw `sudo` in Shell commands is denied:

```
error: sudo is not allowed when permission control is enabled (use run_as instead)
```

### Pair Validation

`run_as` and `reason` must always be provided together:

```
error: run_as and reason must be provided together
```

### Approval Workflow

When the agent wants to execute as the `privileged_user`:

1. An approval request is sent to the user (CLI panel or Feishu card)
2. User approves or denies
3. If approved, the command executes as the specified user
4. If denied, the tool returns an error with the optional deny reason

### Timeout

If the user doesn't respond within the LLM context timeout, the approval card closes automatically and the tool returns a timeout error.

## Affected Tools

| Tool | Additional Parameters |
|------|----------------------|
| `Shell` | `run_as`, `reason` |
| `FileCreate` | `run_as`, `reason` |
| `FileReplace` | `run_as`, `reason` |

## Channel-specific Approval

### CLI

TUI approval panel with:
- Approve button
- Deny button (opens text input for optional deny reason)
- Deny reason propagates into tool error

### Feishu

Interactive card-based approval:
- Approve button on the initial card
- Deny button opens a second card with optional deny reason form
- Card auto-closes on timeout with "Timed Out" status
