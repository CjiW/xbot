# Plan: TUI Header Context Status Bar

## Summary
Add a context usage progress bar to the TUI header bar that shows:
1. **Context fill ratio** — how much of the prompt budget is used (visual bar + percentage)
2. **Compression trigger distance** — gap between current usage and 75% threshold
3. **Output token reservation** — proportion of context window reserved for output

The bar replaces the existing `📊 12.5K/128K (10%)` text in the ready-status area with a richer inline visualization.

## Current State Analysis

### Data Available on CLI Side
- `m.lastTokenUsage.PromptTokens` — last known prompt tokens from API response
- `m.cachedMaxContextTokens` — max context window (from `max_context_tokens` setting)
- `m.lastTokenUsage.CompletionTokens` — last completion token count
- **Missing**: `max_output_tokens` is NOT currently exposed to the CLI model for this purpose

### Compression Logic (engine_run.go)
- `promptBudget = maxContextTokens - maxOutputTokens` (line 745)
- Compression triggers at 75% of promptBudget: `shouldCompact(total, promptBudget)` (trigger.go)
- Snipping triggers at 65% of promptBudget (line 836)

### Current Rendering
- `renderContextUsage()` (cli_view.go:872) — text only: `📊 12.5K/128K (10%)`
- Shown in ready-status bar (cli_view.go:263): `readyParts` when `!typing && progress == nil`
- Title bar is a single line: `titleLeft + padding + titleRight`

## Design

### Visual Layout

**Ready-status bar** (between viewport and input, where context usage currently shows):

```
 ████░░░░░░░░░░░░░░░░ ctx 42% │ out ██░░░░ 6% │ budget 8.4K/20K │ compress at 75%
```

Breakdown:
- **Context bar** (green/yellow/red gradient): `promptTokens / maxContextTokens` — shows raw context fill
- **Output reservation** (dim segment): `maxOutputTokens / maxContextTokens` — shows the reserved slice
- **Budget label**: `promptTokens / promptBudget` — actual usable budget minus output reservation
- **Compress trigger**: the 75% threshold as a `│` marker on the context bar

### Compact variant (narrow terminals < 80 cols):

```
 ctx 42% ▓▓▓▓░░░░░░ [compress 75%]
```

### Approach

1. **Extend `CLITokenUsage`** with `MaxOutputTokens int64` field — populated from agent's `MaxOutputTokens` config
2. **Update `TokenUsageSnapshot`** in `agent/progress.go` with `MaxOutputTokens`
3. **Populate `MaxOutputTokens`** in `updateTokenUsage()` from `s.cfg.MaxOutputTokens`
4. **Update wire translation** in `engine_wire.go` to pass through `MaxOutputTokens`
5. **Cache `maxOutputTokens`** in `cliModel` alongside `cachedMaxContextTokens`
6. **Rewrite `renderContextUsage()`** to render a multi-segment progress bar with:
   - Filled portion (prompt tokens used)
   - Output reservation portion (dim)
   - Compress threshold marker (75% of promptBudget)
   - Numeric labels
7. **Show during progress too** — currently context usage only shows in ready state; add it to `renderProgressStatus` as well

## Changes

### `agent/progress.go`
- Add `MaxOutputTokens int64` to `TokenUsageSnapshot`

### `agent/engine_run.go`
- In `updateTokenUsage()`: set `MaxOutputTokens` from `s.cfg.MaxOutputTokens`

### `agent/engine_wire.go`
- In both CLI and remote CLI progress paths: pass through `MaxOutputTokens` when creating `CLITokenUsage`

### `channel/cli_types.go`
- Add `MaxOutputTokens int64` to `CLITokenUsage`

### `channel/cli_model.go`
- Add `cachedMaxOutputTokens int64` field to `cliModel`

### `channel/cli_helpers.go`
- In progress handler where `lastTokenUsage` is cached: also cache `MaxOutputTokens`
- Add `resolveMaxOutputTokens()` helper (reads from settings)

### `channel/cli_view.go`
- Rewrite `renderContextUsage()` to render a segmented progress bar
- Add `renderContextBar()` helper for the actual bar rendering
- Show in both ready-status AND progress-status bars

### `channel/cli_theme.go`
- Add styles for bar segments: `CtxBarFilled`, `CtxBarOutput`, `CtxBarEmpty`, `CtxBarThreshold`

## Risks
- **Bar width overflow on narrow terminals**: mitigate with compact fallback and truncation
- **Missing maxOutputTokens data**: fallback to 8192 default (same as engine_run.go)
- **Performance**: View() is called on every frame — keep bar rendering pure string ops, no allocations in hot path

## Definition of Done
- [ ] Header shows a segmented progress bar with context fill, output reservation, and compress threshold
- [ ] Bar adapts to terminal width (full bar on wide, compact on narrow)
- [ ] Color gradient matches existing renderContextUsage (green < 50%, yellow 50-80%, red > 80%)
- [ ] Works in both ready-status and during progress (thinking/tool execution)
- [ ] Works in both local CLI and remote CLI mode
- [ ] Build passes: `go build ./...`
- [ ] Test passes: `go test ./channel/... ./agent/...`

## Open Questions
- Should we show this as part of the title bar (top) or keep it in the status bar (bottom, where it is now)? The user said "header" but the current implementation is in the status area. The title bar is already crowded with hints.
