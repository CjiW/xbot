package serverapp

import (
	"encoding/json"
	"fmt"
)

// rpcHandler is a function that handles a single RPC method.
type rpcHandler func(params json.RawMessage) (json.RawMessage, error)

// rpcTable maps method names to their handler functions.
// Built once at startup, used for every incoming RPC request.
type rpcTable map[string]rpcHandler

// --- Generic adapters that eliminate JSON boilerplate ---

// rpc0 wraps a zero-arg function that returns a marshalable result.
// Used for simple getters like get_context_mode, list_models.
func rpc0[R any](fn func() R) rpcHandler {
	return func(_ json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(fn())
	}
}

// rpc0err wraps a zero-arg function that returns (R, error).
func rpc0err[R any](fn func() (R, error)) rpcHandler {
	return func(_ json.RawMessage) (json.RawMessage, error) {
		result, err := fn()
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

// rpc1 wraps a single-arg function that returns (R, error).
// Unmarshals params JSON into P, calls fn, marshals result.
func rpc1[P any, R any](fn func(P) (R, error)) rpcHandler {
	return func(raw json.RawMessage) (json.RawMessage, error) {
		var p P
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		result, err := fn(p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

// rpc1void wraps a single-arg function that returns only error.
// Unmarshals params JSON into P, calls fn, returns nil body on success.
func rpc1void[P any](fn func(P) error) rpcHandler {
	return func(raw json.RawMessage) (json.RawMessage, error) {
		var p P
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return nil, fn(p)
	}
}

// rpc0void wraps a zero-arg void function.
func rpc0void(fn func() error) rpcHandler {
	return func(_ json.RawMessage) (json.RawMessage, error) {
		return nil, fn()
	}
}

// dispatch looks up a method in the table and calls it.
func (t rpcTable) dispatch(method string, params json.RawMessage) (json.RawMessage, error) {
	h, ok := t[method]
	if !ok {
		return nil, fmt.Errorf("unknown RPC method: %s", method)
	}
	return h(params)
}
