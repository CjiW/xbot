package tools

import (
	"context"
	"os"
	"strconv"
	"strings"

	"xbot/config"
	log "xbot/logger"
)

// BuiltinDockerRunnerName is the special name for the built-in docker sandbox.
// Used in user_settings.active_runner to indicate "use server-side docker sandbox".
const BuiltinDockerRunnerName = "__docker__"

// SandboxRouter implements Sandbox interface and routes per-user to either
// DockerSandbox, RemoteSandbox, or NoneSandbox based on user state.
//
// Routing rules (per-user, determined by user_settings.active_runner):
//   - active_runner == BuiltinDockerRunnerName → docker (if enabled)
//   - active_runner == specific remote name → remote (if connected)
//   - Fallback: remote if connected, then docker, then none
type SandboxRouter struct {
	docker     *DockerSandbox
	remote     *RemoteSandbox
	none       *NoneSandbox
	denied     *DeniedSandbox
	tokenStore *RunnerTokenStore

	// defaultMode is used when SandboxForUser can't determine per-user routing.
	// "docker" if docker is enabled, "remote" if remote is enabled, "none" otherwise.
	defaultMode string

	// webServerRunner controls whether web users can use the server's docker sandbox.
	webServerRunner bool
}

// NewSandboxRouter creates a router that holds both docker and remote sandbox instances.
// Either (or both) may be nil — the router falls back gracefully.
func NewSandboxRouter(sandboxCfg config.SandboxConfig, webServerRunner bool, workDir string) *SandboxRouter {
	r := &SandboxRouter{
		none:            &NoneSandbox{},
		denied:          &DeniedSandbox{},
		webServerRunner: webServerRunner,
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

// HasDocker reports whether the built-in docker sandbox is available.
func (r *SandboxRouter) HasDocker() bool {
	return r.docker != nil
}

// DockerImage returns the configured docker image name (e.g. "ubuntu:22.04").
func (r *SandboxRouter) DockerImage() string {
	if r.docker == nil {
		return ""
	}
	return r.docker.Image()
}

// IsRunnerOnline reports whether a specific named runner is connected for the user.
func (r *SandboxRouter) IsRunnerOnline(userID, runnerName string) bool {
	if r.remote == nil {
		return false
	}
	return r.remote.IsRunnerOnline(userID, runnerName)
}

// SetTokenStore stores the runner token store for reading user active_runner preferences.
func (r *SandboxRouter) SetTokenStore(store *RunnerTokenStore) {
	r.tokenStore = store
}

// SandboxForUser returns the user-specific sandbox instance.
// This is the key method for per-user routing — buildToolContext uses it
// to inject the correct sandbox into ToolContext.Sandbox.
//
// Routing priority:
//  1. If user has set active_runner to BuiltinDockerRunnerName → docker
//  2. If user has a connected remote runner matching active_runner name → remote
//  3. If user has any connected remote runner → remote
//  4. Fallback → docker (if enabled), then none
func (r *SandboxRouter) SandboxForUser(userID string) Sandbox {
	// 1. Check explicit active_runner preference
	if userID != "" && r.tokenStore != nil {
		if activeName, err := r.tokenStore.GetActiveRunner(userID); err == nil {
			if activeName == BuiltinDockerRunnerName {
				if r.docker != nil {
					return r.docker
				}
			}
		}
	}

	// 2. Check remote runner (explicit active name or any connection)
	if userID != "" && r.remote != nil {
		if r.remote.HasUser(userID) {
			return r.remote
		}
	}

	// 3. Pure web user without remote runner — denied by default
	if strings.HasPrefix(userID, "web-") {
		if !r.webServerRunner {
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

// resolve returns the per-user sandbox instance (delegates to SandboxForUser).
func (r *SandboxRouter) resolve(userID string) Sandbox {
	return r.SandboxForUser(userID)
}
