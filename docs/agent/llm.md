# llm/ — LLM Client Abstraction

## Files

| File | Purpose |
|------|---------|
| `interface.go` | LLM, StreamingLLM interfaces |
| `openai.go` | OpenAI-compatible client (OpenAI, DeepSeek, Qwen, etc.) (~901 lines) |
| `anthropic.go` | Anthropic/Claude client (~741 lines) |
| `retry.go` | Exponential backoff retry wrapper (~295 lines) |
| `stream.go` | CollectStream: assembles StreamEvents into LLMResponse |
| `types.go` | ChatMessage, ToolDefinition, LLMResponse, think block extraction |
| `proxy.go` | ProxyLLM: forwards via sandbox protocol to remote runner |
| `semaphore.go` | Per-tenant concurrency limiter |
| `think_extract.go` | Extracts <think/>, <reasoning> blocks |
| `tokenizer.go` | Token counting via tiktoken (~380 lines) |

## Streaming Pitfalls

- DeepSeek duplicates `reasoning_content` in Content — deduplicate with TrimSpace (`openai.go:584`)
- Empty stream deltas (all nil) cause panic if not skipped (`openai.go:763`)
- `finish_reason` in intermediate chunks causes premature termination — check only after loop ends (`openai.go:788`)
- Must send Usage before Done event (`openai.go:836`)
- Provider without `finish_reason` but with tool_calls: infer reason as tool_calls (`openai.go:844`)

## Retry Behavior

- Creates fresh context per attempt (`context.Background()`), not inheriting parent deadline (`retry.go:230-257`)
- Parent cancel bridged via separate goroutine
- `GenerateStream` does NOT use perAttemptCtx — defer cancel() would kill async stream goroutine (`retry.go:278`)

## Async Model Loading

`NewOpenAILLM` loads model list in a goroutine (non-blocking). `ListModels()` returns fallback model immediately, full list updates when API responds.

## Key Interfaces

```go
type LLM interface {
    Generate(ctx, model, messages, tools, thinkingMode) (*LLMResponse, error)
    ListModels() []string
}
type StreamingLLM interface {
    Stream(ctx, model, messages, tools, thinkingMode) (<-chan StreamEvent, error)
}
type ModelLoader interface {
    LoadModelsFromAPI(ctx context.Context) error
}
```

`ModelLoader` is implemented by `*OpenAILLM` only — used by `GetLLMForModel` via type assertion for sync model loading on cache miss.

## OnModelsLoaded Callback

`UserLLMConfig.OnModelsLoaded` is called by `NewOpenAILLM`'s async goroutine after fetching model list from API. Used to persist models to DB via `UpdateCachedModels`. Must handle case where sub ID doesn't exist in DB (config-only subs).
