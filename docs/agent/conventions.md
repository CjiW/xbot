# Coding Conventions

## Error Handling

- Wrap with context: `fmt.Errorf("context: %w", err)`
- Never use `pkg/errors` — stdlib `fmt.Errorf` with `%w` only
- User-facing validation errors can be Chinese
- Error message prefix: lowercase English with colon separator
- For sentinel errors: define in package, use `errors.Is()`/`errors.As()`

## Logging

- Import: `log "xbot/logger"` (custom wrapper, not stdlib)
- Single field: `log.WithField("key", val).Info("msg")`
- Multiple fields: `log.WithFields(log.Fields{...}).Info("msg")`
- Errors: `log.WithError(err).Error("msg")` — never `WithField("error", err)`
- Save to variable for reuse: `l := log.WithField("request_id", id)`
- Levels: Fatal (unrecoverable startup) → Error (runtime) → Warn (degraded) → Info (state change) → Debug (verbose)

## Testing

- Files: `*_test.go` alongside source, one test file per source file
- 102 test files across the project
- Pre-commit hook runs: gofmt → golangci-lint → go build → go test
- Run specific: `go test ./agent/ -run TestName -count=1`
- Run all: `go test ./...`

## Interfaces

- Define at point of use, not in separate `interfaces.go`
- Small, focused interfaces (1-3 methods typical)
- Key interfaces: see `docs/agent/architecture.md#key-interfaces`

## Concurrency

- Goroutines for: agent loops, channel handlers, background tasks, streaming
- ~70 goroutine launch points across the codebase
- Always use `context.WithCancel` for cancellable work
- Non-blocking channel sends with `select { case ch <- msg:; case <-ctx.Done(): }`
- Use `sync.WaitGroup` for background task drain on shutdown
- Never defer semaphore release inside loops (causes slot accumulation)

## Naming

- Packages: short, lowercase, no underscores (`agent`, `llm`, `tools`)
- Files: snake_case (`engine_run.go`, `middleware_builtin.go`)
- Test helpers: `setupXxx()`, `newMockXxx()`
- Constants: CamelCase in Go, UPPER_SNAKE for pre-commit env vars

## Build

```bash
go build ./...                  # compile all
go test ./...                   # run all tests
golangci-lint run ./...         # lint
```
Makefile targets: `make build`, `make run`, `make test`
Binary: `xbot-cli` from `cmd/xbot-cli/`
