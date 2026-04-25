package hooks

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// HTTPExecutor
// ---------------------------------------------------------------------------

// HTTPExecutor sends an HTTP POST request to the URL specified in HookDef.URL
// and returns the parsed response as a Result.
type HTTPExecutor struct {
	client      *http.Client
	allowedNets []*net.IPNet // SSRF whitelist (CIDR format)
	mu          sync.RWMutex // protects allowedNets
	envLookupFn func(string) (string, bool)
}

// maxHTTPResponseSize limits HTTP hook response body to 1 MB to prevent OOM.
const maxHTTPResponseSize = 1 << 20

// NewHTTPExecutor creates a new HTTPExecutor with a default HTTP client and
// SSRF protection that blocks RFC 1918 / loopback / link-local addresses.
// The SSRF check runs at dial time (inside the Transport) to prevent DNS rebinding.
func NewHTTPExecutor() *HTTPExecutor {
	e := &HTTPExecutor{
		envLookupFn: os.LookupEnv,
	}
	e.client = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: e.dialWithSSRFCheck,
			// Match default Transport TLS settings
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	return e
}

// Type returns "http".
func (e *HTTPExecutor) Type() string { return "http" }

// Execute sends an HTTP POST to def.URL with the event payload as JSON body.
//
// Execution flow:
//  1. Determine timeout: def.Timeout > 0, otherwise default 10s.
//  2. SSRF check: resolve URL host, block private IPs unless whitelisted.
//  3. Build POST request with JSON body and custom headers.
//  4. Send request.
//  5. 2xx → parse response body as JSON Result.
//     Non-2xx → non-blocking error (Decision="allow").
//     Connection failure → non-blocking error (Decision="allow").
func (e *HTTPExecutor) Execute(ctx context.Context, def *HookDef, event Event) (*Result, error) {
	// 1. Determine timeout.
	timeout := 10 * time.Second
	if def.Timeout > 0 {
		timeout = time.Duration(def.Timeout) * time.Second
	}

	// 2. SSRF check: validate URL scheme (only http/https allowed).
	parsedURL, err := url.Parse(def.URL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme: %s", parsedURL.Scheme)
	}

	// 3. Build request body.
	bodyBytes, err := json.Marshal(event.Payload())
	if err != nil {
		return nil, fmt.Errorf("marshal event payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, def.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set body after creation so we can control content type header.
	req.Body = io.NopCloser(newBytesReader(bodyBytes))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(newBytesReader(bodyBytes)), nil
	}
	req.ContentLength = int64(len(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	// 4. Set custom headers with env var substitution.
	for k, v := range def.Headers {
		req.Header.Set(k, e.resolveEnvVars(v))
	}

	// 5. Inject allowed env vars into the request context (as headers).
	// The env vars are attached as X-Hook-Env-<NAME> headers.
	for _, envName := range def.AllowedEnvVars {
		if val, ok := e.envLookupFn(envName); ok {
			req.Header.Set("X-Hook-Env-"+envName, val)
		}
	}

	// 6. Send request.
	resp, err := e.client.Do(req)
	if err != nil {
		// SSRF errors are blocking — return as hard error.
		if strings.Contains(err.Error(), "SSRF:") {
			return nil, err
		}
		// Other connection failures → non-blocking error.
		return &Result{
			Decision: "allow",
			Reason:   fmt.Sprintf("HTTP request failed: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseSize))
	if err != nil {
		return &Result{
			Decision: "allow",
			Reason:   fmt.Sprintf("read response body: %v", err),
		}, nil
	}

	// 7. Interpret response.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return parseHTTPSuccessResult(respBody), nil
	}

	// Non-2xx → non-blocking error.
	return &Result{
		Decision: "allow",
		Reason:   fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)),
	}, nil
}

// ---------------------------------------------------------------------------
// SSRF protection (dial-time check to prevent DNS rebinding)
// ---------------------------------------------------------------------------

// dialWithSSRFCheck is a custom DialContext that resolves the target hostname
// and blocks connections to private/reserved IPs unless they are in allowedNets.
// This runs at actual TCP dial time, eliminating DNS rebinding TOCTOU attacks.
func (e *HTTPExecutor) dialWithSSRFCheck(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("SSRF: invalid address %q: %w", addr, err)
	}

	// Resolve hostname to IP addresses at dial time.
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("SSRF: DNS lookup failed for %q: %w", host, err)
	}

	for _, ip := range ips {
		if !e.isAllowedIP(ip.IP) && isPrivateIP(ip.IP) {
			return nil, fmt.Errorf("SSRF: %s resolves to private/reserved IP %s (blocked)", host, ip.IP)
		}
	}

	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
}

// isAllowedIP checks whether the given IP falls within any of the allowed CIDR ranges.
func (e *HTTPExecutor) isAllowedIP(ip net.IP) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, cidr := range e.allowedNets {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// SetAllowedNets configures the CIDR whitelist for SSRF protection.
// Addresses within these ranges will be allowed even if they are private IPs.
func (e *HTTPExecutor) SetAllowedNets(cidrs []string) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, ipNet, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		nets = append(nets, ipNet)
	}
	e.mu.Lock()
	e.allowedNets = nets
	e.mu.Unlock()
}

// isPrivateIP checks whether the given IP is in a private, loopback, link-local,
// or other reserved address range that should not be accessible via HTTP hooks.
func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		network string
	}{
		// Loopback
		{"127.0.0.0/8"},
		{"::1/128"},
		// RFC 1918 private
		{"10.0.0.0/8"},
		{"172.16.0.0/12"},
		{"192.168.0.0/16"},
		// Link-local
		{"169.254.0.0/16"},
		{"fe80::/10"},
		// IPv6 unique local
		{"fd00::/8"},
		// 0.0.0.0/8 — on Linux/macOS, connecting to 0.0.0.0:PORT = 127.0.0.1:PORT
		{"0.0.0.0/8"},
		// CGNAT / shared address space
		{"100.64.0.0/10"},
		// Benchmarking (used by some cloud internal metadata)
		{"198.18.0.0/15"},
		// Multicast
		{"224.0.0.0/4"},
		// Reserved
		{"240.0.0.0/4"},
	}

	for _, r := range privateRanges {
		_, ipNet, err := net.ParseCIDR(r.network)
		if err != nil {
			continue
		}
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// envVarPattern matches $ENV_VAR or ${ENV_VAR} in strings.
var envVarPattern = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

// resolveEnvVars replaces $ENV_VAR and ${ENV_VAR} patterns in s with the
// corresponding environment variable values. Variables that are not found are
// replaced with an empty string.
func (e *HTTPExecutor) resolveEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract variable name from $VAR or ${VAR}.
		name := envVarPattern.FindStringSubmatch(match)[1]
		if val, ok := e.envLookupFn(name); ok {
			return val
		}
		return ""
	})
}

// parseHTTPSuccessResult decodes the HTTP response body as a JSON Result.
// If the body is not valid JSON, it is returned as the Context field.
func parseHTTPSuccessResult(body []byte) *Result {
	result := &Result{
		Decision: "allow",
	}

	trimmed := trimSpace(string(body))
	if trimmed == "" {
		return result
	}

	// Try to parse as JSON.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		result.Context = trimmed
		return result
	}

	if v, ok := raw["decision"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			result.Decision = s
		}
	}
	if v, ok := raw["reason"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			result.Reason = s
		}
	}
	if v, ok := raw["context"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			result.Context = s
		}
	}
	if v, ok := raw["updatedInput"]; ok {
		var m map[string]any
		if json.Unmarshal(v, &m) == nil {
			result.UpdatedInput = m
		}
	}

	return result
}

// bytesReader wraps a byte slice to implement io.Reader.
type bytesReader struct {
	data []byte
	pos  int
}

func newBytesReader(data []byte) *bytesReader {
	return &bytesReader{data: data}
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
