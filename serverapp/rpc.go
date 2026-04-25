package serverapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// rpcHandler is a function that handles a single RPC method.
type rpcHandler func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

// rpcTable maps method names to their handler functions.
// Built once at server startup, reused for every incoming RPC request.
type rpcTable map[string]rpcHandler

// --- Per-request context values ---

type rpcCtxKeyType struct{}

var rpcCtxKey = rpcCtxKeyType{}

// rpcCtxData holds per-request identity fields, stored in context.
type rpcCtxData struct {
	authSenderID string
	bizID        string
}

func withRPCCtx(ctx context.Context, authSenderID, bizID string) context.Context {
	return context.WithValue(ctx, rpcCtxKey, &rpcCtxData{authSenderID: authSenderID, bizID: bizID})
}

func rpcAuthID(ctx context.Context) string {
	if v, ok := ctx.Value(rpcCtxKey).(*rpcCtxData); ok {
		return v.authSenderID
	}
	return ""
}

func rpcBizID(ctx context.Context) string {
	if v, ok := ctx.Value(rpcCtxKey).(*rpcCtxData); ok {
		return v.bizID
	}
	return ""
}

// --- Generic adapters that eliminate JSON boilerplate ---

func rpc0[R any](fn func(ctx context.Context) R) rpcHandler {
	return func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(fn(ctx))
	}
}

func rpc0err[R any](fn func(ctx context.Context) (R, error)) rpcHandler {
	return func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		result, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

func rpc1[P any, R any](fn func(ctx context.Context, p P) (R, error)) rpcHandler {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p P
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		result, err := fn(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

func rpc1void[P any](fn func(ctx context.Context, p P) error) rpcHandler {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p P
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return nil, fn(ctx, p)
	}
}

func rpc0void(fn func(ctx context.Context) error) rpcHandler {
	return func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return nil, fn(ctx)
	}
}

func (t rpcTable) dispatch(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	h, ok := t[method]
	if !ok {
		return nil, fmt.Errorf("unknown RPC method: %s", method)
	}
	return h(ctx, params)
}

var errSettingsUnavailable = errors.New("settings service not available")
