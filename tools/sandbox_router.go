package tools

import (
	"context"
	"os"
	"strconv"
	"strings"

	"xbot/config"
	log "xbot/logger"
)

// SandboxRouter implements Sandbox interface and routes per-user to either
// DockerSandbox, RemoteSandbox, or NoneSandbox based on user state.
//
// Routing rules:
//   - If the user has an active RemoteSandbox connection → remote
//   - Otherwise → docker (if enabled)
//   - Fallback → none
//
// The same user always routes to the same sandbox type within a session.
// Cross-mode failover (docker ↔ remote) is intentionally NOT supported
// because the two have completely different filesystems.
type SandboxRouter struct {
	docker *DockerSandbox
	remote *RemoteSandbox
	none   *NoneSandbox
	denied *DeniedSandbox

	// defaultMode is used when SandboxForUser can't determine per-user routing.
	// "docker" if docker is enabled, "remote" if remote is enabled, "none" otherwise.
	defaultMode string
}

// NewSandboxRouter creates a router that holds both docker and remote sandbox instances.
// Either (or both) may be nil — the router falls back gracefully.
func NewSandboxRouter(sandboxCfg config.SandboxConfig, workDir string) *SandboxRouter {
	r := &SandboxRouter{
		none:   &NoneSandbox{},
		denied: &DeniedSandbox{},
	}

	// Initialize docker sandbox if configured
	// Docker is enabled when Mode=="docker", or when RemoteMode is set and Mode is not "none".
	// Mode=="none" + RemoteMode=="remote" means remote-only, no docker.
	if sandboxCfg.Mode == "docker" || (sandboxCfg.RemoteMode != "" && sandboxCfg.Mode != "none") {
		cleanupStaleTmpFiles()
		pruneDockerResources()
		r.docker = NewDockerSandbox(sandboxCfg, workDir)
	}

	// Initialize remote sandbox if configured
	if sandboxCfg.RemoteMode != "" || sandboxCfg.Mode == "remote" {
		wsPort := sandboxCfg.WSPort
		if wsPort == 0 {
			wsPort = 8080
		}
		xbotDir := workDir + "/.xbot"
		syncCfg := RemoteSandboxSyncConfig{
			GlobalSkillDirs: []string{xbotDir + "/skills"},
			AgentsDir:       xbotDir + "/agents",
		}
		rs, err := NewRemoteSandbox(RemoteSandboxConfig{
			Addr:      "0.0.0.0:" + strconv.Itoa(wsPort),
			AuthToken: sandboxCfg.AuthToken,
		}, syncCfg)
		if err != nil {
			log.WithError(err).Error("Failed to start remote sandbox, falling back")
		} else {
			r.remote = rs
		}
	}

	// Determine default mode for Name() and fallback routing
	switch {
	case r.remote != nil:
		r.defaultMode = "remote"
	case r.docker != nil:
		r.defaultMode = "docker"
	default:
		r.defaultMode = "none"
	}

	log.Infof("SandboxRouter initialized: default=%s, docker=%v, remote=%v",
		r.defaultMode, r.docker != nil, r.remote != nil)

	return r
}

// Name returns the default sandbox mode name.
// For per-user resolution, use SandboxForUser(userID).Name().
func (r *SandboxRouter) Name() string {
	return r.defaultMode
}

// SandboxForUser returns the user-specific sandbox instance.
// This is the key method for per-user routing — buildToolContext uses it
// to inject the correct sandbox into ToolContext.Sandbox.
//
// Routing:
//   - If user has a connected remote runner → use it
//   - If user is a pure web user (senderID starts with "web-"):
//     — WEB_USER_SERVER_RUNNER=true (default): fallback to docker
//     — WEB_USER_SERVER_RUNNER=false: no fallback (remote only)
//   - Otherwise → docker fallback
func (r *SandboxRouter) SandboxForUser(userID string) Sandbox {
	// Check remote runner first
	if userID != "" && r.remote != nil {
		if r.remote.HasUser(userID) {
			return r.remote
		}
	}

	// Pure web user without remote runner — denied by default
	if strings.HasPrefix(userID, "web-") {
		webServerRunner := false
		if v := os.Getenv("WEB_USER_SERVER_RUNNER"); v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				webServerRunner = b
			}
		}
		if !webServerRunner {
			// User must have their own remote runner — return DeniedSandbox to block ALL access
			return r.denied
		}
		// Explicitly enabled: allow fallback to server sandbox (docker)
	}

	if r.docker != nil {
		return r.docker
	}
	return r.none
}

// Ensure SandboxRouter implements SandboxResolver
var _ SandboxResolver = (*SandboxRouter)(nil)

// --- Sandbox interface delegation ---

func (r *SandboxRouter) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	return r.resolve(spec.UserID).Exec(ctx, spec)
}

func (r *SandboxRouter) ReadFile(ctx context.Context, path string, userID string) ([]byte, error) {
	return r.resolve(userID).ReadFile(ctx, path, userID)
}

func (r *SandboxRouter) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	return r.resolve(userID).WriteFile(ctx, path, data, perm, userID)
}

func (r *SandboxRouter) Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error) {
	return r.resolve(userID).Stat(ctx, path, userID)
}

func (r *SandboxRouter) ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error) {
	return r.resolve(userID).ReadDir(ctx, path, userID)
}

func (r *SandboxRouter) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	return r.resolve(userID).MkdirAll(ctx, path, perm, userID)
}

func (r *SandboxRouter) Remove(ctx context.Context, path string, userID string) error {
	return r.resolve(userID).Remove(ctx, path, userID)
}

func (r *SandboxRouter) RemoveAll(ctx context.Context, path string, userID string) error {
	return r.resolve(userID).RemoveAll(ctx, path, userID)
}

func (r *SandboxRouter) DownloadFile(ctx context.Context, url, outputPath, userID string) error {
	return r.resolve(userID).DownloadFile(ctx, url, outputPath, userID)
}

func (r *SandboxRouter) GetShell(userID string, workspace string) (string, error) {
	return r.resolve(userID).GetShell(userID, workspace)
}

func (r *SandboxRouter) Workspace(userID string) string {
	return r.resolve(userID).Workspace(userID)
}

// Close closes all sandbox instances (docker containers, remote connections).
func (r *SandboxRouter) Close() error {
	var errs []error
	if r.docker != nil {
		if err := r.docker.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.remote != nil {
		if err := r.remote.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0] // Return first error
	}
	return nil
}

// CloseForUser closes sandbox resources for a specific user across all backends.
// Remote sandbox connections are not closed — runners should be persistent.
func (r *SandboxRouter) CloseForUser(userID string) error {
	if r.docker != nil {
		return r.docker.CloseForUser(userID)
	}
	return nil
}

// IsExporting checks if docker sandbox is exporting for this user.
func (r *SandboxRouter) IsExporting(userID string) bool {
	if r.docker != nil {
		return r.docker.IsExporting(userID)
	}
	return false
}

// ExportAndImport triggers export+import on the docker sandbox.
func (r *SandboxRouter) ExportAndImport(userID string) error {
	if r.docker != nil {
		return r.docker.ExportAndImport(userID)
	}
	return nil
}

// resolve returns the per-user sandbox instance.
func (r *SandboxRouter) resolve(userID string) Sandbox {
	if userID != "" && r.remote != nil && r.remote.HasUser(userID) {
		return r.remote
	}
	// Pure web user without remote runner — denied by default (same logic as SandboxForUser)
	if strings.HasPrefix(userID, "web-") {
		webServerRunner := false
		if v := os.Getenv("WEB_USER_SERVER_RUNNER"); v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				webServerRunner = b
			}
		}
		if !webServerRunner {
			return r.denied
		}
	}
	if r.docker != nil {
		return r.docker
	}
	return r.none
}
