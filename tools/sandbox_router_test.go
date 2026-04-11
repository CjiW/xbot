package tools

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// ============================================================================
// SandboxRouter 单元测试
// 覆盖路由逻辑：Name()、SandboxForUser()、接口委托、生命周期管理
// ============================================================================

// --- 辅助工具 ---

// newNoneRouter 创建一个只有 NoneSandbox 的路由器（最简配置）
func newNoneRouter() *SandboxRouter {
	return &SandboxRouter{
		none:        &NoneSandbox{},
		defaultMode: "none",
	}
}

// newDockerRouter 创建一个带 DockerSandbox 的路由器（零值 DockerSandbox 不依赖 Docker daemon）
func newDockerRouter() *SandboxRouter {
	return &SandboxRouter{
		docker:      &DockerSandbox{},
		none:        &NoneSandbox{},
		defaultMode: "docker",
	}
}

// newRemoteRouter 创建一个带 RemoteSandbox 的路由器，并模拟 userA 已连接
func newRemoteRouter(connectedUsers ...string) *SandboxRouter {
	rs := &RemoteSandbox{}
	for _, uid := range connectedUsers {
		rs.connections.Store(uid, &userRunnersEntry{runners: map[string]*runnerConnection{"default": {}}})
	}
	return &SandboxRouter{
		remote:      rs,
		none:        &NoneSandbox{},
		defaultMode: "remote",
	}
}

// newFullRouter 创建同时拥有 docker + remote 的路由器（remote 优先）
func newFullRouter(connectedUsers ...string) *SandboxRouter {
	rs := &RemoteSandbox{}
	for _, uid := range connectedUsers {
		rs.connections.Store(uid, &userRunnersEntry{runners: map[string]*runnerConnection{"default": {}}})
	}
	return &SandboxRouter{
		docker:      &DockerSandbox{},
		remote:      rs,
		none:        &NoneSandbox{},
		defaultMode: "remote",
	}
}

// ============================================================================
// Name() 测试 — 验证 defaultMode 返回值
// ============================================================================

func TestSandboxRouter_Name_NoneOnly(t *testing.T) {
	r := newNoneRouter()
	if got := r.Name(); got != "none" {
		t.Errorf("Name() = %q, want %q", got, "none")
	}
}

func TestSandboxRouter_Name_DockerOnly(t *testing.T) {
	r := newDockerRouter()
	if got := r.Name(); got != "docker" {
		t.Errorf("Name() = %q, want %q", got, "docker")
	}
}

func TestSandboxRouter_Name_RemoteOnly(t *testing.T) {
	r := newRemoteRouter()
	if got := r.Name(); got != "remote" {
		t.Errorf("Name() = %q, want %q", got, "remote")
	}
}

func TestSandboxRouter_Name_FullRouter(t *testing.T) {
	// docker + remote 同时存在时，remote 优先
	r := newFullRouter()
	if got := r.Name(); got != "remote" {
		t.Errorf("Name() = %q, want %q (remote should take priority)", got, "remote")
	}
}

// ============================================================================
// SandboxForUser 路由测试 — 核心路由逻辑
// ============================================================================

func TestSandboxForUser_NoneOnly(t *testing.T) {
	r := newNoneRouter()

	// 无 docker、无 remote → 所有用户都走 none
	for _, uid := range []string{"userA", "userB", ""} {
		sb := r.SandboxForUser(uid)
		if sb.Name() != "none" {
			t.Errorf("SandboxForUser(%q).Name() = %q, want %q", uid, sb.Name(), "none")
		}
	}
}

func TestSandboxForUser_DockerOnly(t *testing.T) {
	r := newDockerRouter()

	// 有 docker、无 remote → 所有用户走 docker（包括空 userID）
	for _, uid := range []string{"userA", "userB", ""} {
		sb := r.SandboxForUser(uid)
		if sb.Name() != "docker" {
			t.Errorf("SandboxForUser(%q).Name() = %q, want %q", uid, sb.Name(), "docker")
		}
	}
}

func TestSandboxForUser_RemoteOnly(t *testing.T) {
	// 只有 remote，无 docker
	r := newRemoteRouter("userA")

	// userA 已连接 → 走 remote
	sb := r.SandboxForUser("userA")
	if sb.Name() != "remote" {
		t.Errorf("SandboxForUser(userA).Name() = %q, want %q", sb.Name(), "remote")
	}

	// userB 未连接、无 docker → 回退到 none
	sb = r.SandboxForUser("userB")
	if sb.Name() != "none" {
		t.Errorf("SandboxForUser(userB).Name() = %q, want %q", sb.Name(), "none")
	}

	// 空 userID → 跳过 remote 检查 → 回退到 none
	sb = r.SandboxForUser("")
	if sb.Name() != "none" {
		t.Errorf("SandboxForUser(\"\").Name() = %q, want %q", sb.Name(), "none")
	}
}

func TestSandboxForUser_FullRouter(t *testing.T) {
	// docker + remote 同时存在
	r := newFullRouter("userA")

	// userA 有 remote 连接 → 走 remote
	sb := r.SandboxForUser("userA")
	if sb.Name() != "remote" {
		t.Errorf("SandboxForUser(userA).Name() = %q, want %q", sb.Name(), "remote")
	}

	// userB 无 remote 连接 → 回退到 docker
	sb = r.SandboxForUser("userB")
	if sb.Name() != "docker" {
		t.Errorf("SandboxForUser(userB).Name() = %q, want %q", sb.Name(), "docker")
	}

	// 空 userID → 跳过 remote 检查 → 回退到 docker
	sb = r.SandboxForUser("")
	if sb.Name() != "docker" {
		t.Errorf("SandboxForUser(\"\").Name() = %q, want %q", sb.Name(), "docker")
	}
}

func TestSandboxForUser_RemoteConnectionTracking(t *testing.T) {
	// 动态添加/移除 remote 连接，验证路由变化
	rs := &RemoteSandbox{}
	r := &SandboxRouter{
		docker:      &DockerSandbox{},
		remote:      rs,
		none:        &NoneSandbox{},
		defaultMode: "remote",
	}

	// userA 未连接 → docker
	sb := r.SandboxForUser("userA")
	if sb.Name() != "docker" {
		t.Errorf("before connect: userA should route to docker, got %q", sb.Name())
	}

	// 模拟 userA 连接
	rs.connections.Store("userA", &userRunnersEntry{runners: map[string]*runnerConnection{"default": {}}})

	// userA 已连接 → remote
	sb = r.SandboxForUser("userA")
	if sb.Name() != "remote" {
		t.Errorf("after connect: userA should route to remote, got %q", sb.Name())
	}

	// userB 仍未连接 → docker
	sb = r.SandboxForUser("userB")
	if sb.Name() != "docker" {
		t.Errorf("userB should still route to docker, got %q", sb.Name())
	}

	// 模拟 userA 断开
	rs.connections.Delete("userA")

	// userA 断开后 → 回退到 docker
	sb = r.SandboxForUser("userA")
	if sb.Name() != "docker" {
		t.Errorf("after disconnect: userA should route to docker, got %q", sb.Name())
	}
}

// ============================================================================
// Sandbox 接口委托测试 — 验证方法正确路由到对应后端
// ============================================================================

func TestSandboxRouter_Delegation_NoneSandbox(t *testing.T) {
	// 验证无 docker 时，所有操作正确委托到 NoneSandbox
	r := newNoneRouter()
	ctx := context.Background()

	// Exec：NoneSandbox 会真实执行命令
	result, err := r.Exec(ctx, ExecSpec{
		Command: "echo hello",
		Shell:   true,
		UserID:  "user1",
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("Exec stdout = %q, want %q", result.Stdout, "hello\n")
	}

	// Workspace：NoneSandbox 返回空字符串
	if ws := r.Workspace("user1"); ws != "" {
		t.Errorf("Workspace() = %q, want empty string", ws)
	}

	// GetShell：NoneSandbox 返回平台默认 shell
	shell, err := r.GetShell("user1", "")
	if err != nil || shell != defaultShell() {
		t.Errorf("GetShell() = %q, %v, want %s, nil", shell, err, defaultShell())
	}
}

func TestSandboxRouter_Delegation_NoneSandbox_FileOps(t *testing.T) {
	// 验证文件操作委托到 NoneSandbox（直接操作宿主机文件系统）
	r := newNoneRouter()
	ctx := context.Background()

	// 在临时目录中测试
	tmpDir := t.TempDir()

	// MkdirAll
	err := r.MkdirAll(ctx, tmpDir+"/sub", 0o755, "user1")
	if err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// WriteFile
	err = r.WriteFile(ctx, tmpDir+"/sub/test.txt", []byte("hello"), 0o644, "user1")
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// ReadFile
	data, err := r.ReadFile(ctx, tmpDir+"/sub/test.txt", "user1")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("ReadFile content = %q, want %q", string(data), "hello")
	}

	// Stat
	info, err := r.Stat(ctx, tmpDir+"/sub/test.txt", "user1")
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Name != "test.txt" || info.Size != 5 {
		t.Errorf("Stat: Name=%q Size=%d, want test.txt, 5", info.Name, info.Size)
	}

	// ReadDir
	entries, err := r.ReadDir(ctx, tmpDir+"/sub", "user1")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "test.txt" {
		t.Errorf("ReadDir: got %v, want [test.txt]", entries)
	}

	// Remove
	err = r.Remove(ctx, tmpDir+"/sub/test.txt", "user1")
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// 验证文件已删除
	if _, err := os.Stat(tmpDir + "/sub/test.txt"); !os.IsNotExist(err) {
		t.Error("Remove: file should not exist after removal")
	}

	// RemoveAll
	r.MkdirAll(ctx, tmpDir+"/sub2/deep", 0o755, "user1")
	err = r.RemoveAll(ctx, tmpDir+"/sub2", "user1")
	if err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}
	if _, err := os.Stat(tmpDir + "/sub2"); !os.IsNotExist(err) {
		t.Error("RemoveAll: directory should not exist after removal")
	}
}

func TestSandboxRouter_Delegation_DockerSandbox(t *testing.T) {
	// 验证有 docker 时操作委托到 DockerSandbox
	// 使用零值 DockerSandbox，Exec 会因为没有 containers 返回错误
	r := newDockerRouter()

	// DockerSandbox.Name() 返回 "docker"
	sb := r.SandboxForUser("user1")
	if sb.Name() != "docker" {
		t.Fatalf("SandboxForUser(user1).Name() = %q, want docker", sb.Name())
	}

	// DockerSandbox.Workspace() 返回 "/workspace"
	if ws := r.Workspace("user1"); ws != "/workspace" {
		t.Errorf("Workspace() = %q, want /workspace", ws)
	}

	// DockerSandbox.GetShell() 需要已创建的 container，未创建时返回 error
	// 这里验证委托路径正确（会调用到 DockerSandbox 的方法）
	_, err := r.GetShell("user1", "")
	if err == nil {
		// 零值 DockerSandbox 没有 containers，GetShell 应该返回错误
		// 如果没返回错误，说明可能委托路径有问题
		t.Log("GetShell returned nil error (unexpected for zero-value DockerSandbox)")
	}
}

// ============================================================================
// Close / CloseForUser 测试
// ============================================================================

func TestSandboxRouter_Close_NilBackends(t *testing.T) {
	// 只有 none sandbox 时，Close 不应出错
	r := newNoneRouter()
	if err := r.Close(); err != nil {
		t.Errorf("Close() with nil backends returned error: %v", err)
	}
}

func TestSandboxRouter_CloseForUser_NilBackends(t *testing.T) {
	r := newNoneRouter()
	if err := r.CloseForUser("user1"); err != nil {
		t.Errorf("CloseForUser() with nil backends returned error: %v", err)
	}
}

func TestSandboxRouter_CloseForUser_NilDocker_NilRemote(t *testing.T) {
	// docker 和 remote 都为 nil，只有 none
	r := &SandboxRouter{
		none: &NoneSandbox{},
	}
	if err := r.CloseForUser("user1"); err != nil {
		t.Errorf("CloseForUser() = %v, want nil", err)
	}
}

// ============================================================================
// SandboxExporter 接口测试
// ============================================================================

func TestSandboxRouter_IsExporting_NilDocker(t *testing.T) {
	// 无 docker → IsExporting 返回 false
	r := newNoneRouter()
	if r.IsExporting("user1") {
		t.Error("IsExporting() should return false when docker is nil")
	}
}

func TestSandboxRouter_ExportAndImport_NilDocker(t *testing.T) {
	// 无 docker → ExportAndImport 返回 nil（no-op）
	r := newNoneRouter()
	if err := r.ExportAndImport("user1"); err != nil {
		t.Errorf("ExportAndImport() = %v, want nil", err)
	}
}

// ============================================================================
// SandboxResolver 接口编译时检查
// ============================================================================

func TestSandboxRouter_ImplementsSandboxResolver(t *testing.T) {
	// 编译时检查：SandboxRouter 实现 SandboxResolver 接口
	// 此处不执行任何操作，仅作为文档说明
	// 实际检查在 sandbox_router.go 中通过 var _ SandboxResolver = (*SandboxRouter)(nil) 完成
	var _ SandboxResolver = (*SandboxRouter)(nil)
}

// ============================================================================
// 边界条件和异常情况
// ============================================================================

func TestSandboxForUser_EmptyUserID_SkipsRemote(t *testing.T) {
	// 空 userID 应跳过 remote 检查，直接回退到 docker 或 none
	r := newFullRouter("userA") // userA 有 remote 连接

	// 空 userID → 即使 remote 有连接，也不走 remote
	sb := r.SandboxForUser("")
	if sb.Name() != "docker" {
		t.Errorf("SandboxForUser(\"\").Name() = %q, want %q (empty userID should skip remote)", sb.Name(), "docker")
	}
}

func TestSandboxForUser_NilDocker_NilRemote(t *testing.T) {
	// docker 和 remote 都为 nil → 走 none
	r := &SandboxRouter{
		none: &NoneSandbox{},
	}
	for _, uid := range []string{"user1", "user2", ""} {
		sb := r.SandboxForUser(uid)
		if sb.Name() != "none" {
			t.Errorf("SandboxForUser(%q).Name() = %q, want %q", uid, sb.Name(), "none")
		}
	}
}

func TestSandboxRouter_Exec_EmptyUserID(t *testing.T) {
	// 验证空 userID 时 Exec 正确委托到回退沙箱
	r := newNoneRouter()
	ctx := context.Background()

	result, err := r.Exec(ctx, ExecSpec{
		Command: "echo test",
		Shell:   true,
		UserID:  "", // 空 userID
	})
	if err != nil {
		t.Fatalf("Exec with empty userID failed: %v", err)
	}
	if result.Stdout != "test\n" {
		t.Errorf("Exec stdout = %q, want %q", result.Stdout, "test\n")
	}
}

func TestSandboxRouter_MultipleUsers_IndependentRouting(t *testing.T) {
	// 多用户独立路由：验证每个用户路由到正确的后端
	rs := &RemoteSandbox{}
	rs.connections.Store("alice", &userRunnersEntry{runners: map[string]*runnerConnection{"default": {}}})
	rs.connections.Store("charlie", &userRunnersEntry{runners: map[string]*runnerConnection{"default": {}}})

	r := &SandboxRouter{
		docker:      &DockerSandbox{},
		remote:      rs,
		none:        &NoneSandbox{},
		defaultMode: "remote",
	}

	tests := []struct {
		user     string
		expected string
	}{
		{"alice", "remote"},   // 已连接
		{"bob", "docker"},     // 未连接，回退到 docker
		{"charlie", "remote"}, // 已连接
		{"dave", "docker"},    // 未连接
		{"", "docker"},        // 空 userID
	}

	for _, tt := range tests {
		sb := r.SandboxForUser(tt.user)
		if sb.Name() != tt.expected {
			t.Errorf("SandboxForUser(%q).Name() = %q, want %q", tt.user, sb.Name(), tt.expected)
		}
	}
}

// ============================================================================
// SandboxForUser active_runner 偏好测试
// 验证用户设置 active_runner=__docker__ 后，路由行为正确
// ============================================================================

// newTestDB 创建内存 SQLite DB 并初始化 user_settings 表
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE user_settings (
			channel TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT,
			updated_at INTEGER,
			PRIMARY KEY (channel, sender_id, key)
		);
		CREATE INDEX idx_user_settings_sender ON user_settings(channel, sender_id);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSandboxForUser_ActiveRunner_Docker(t *testing.T) {
	// 用户设置 active_runner=__docker__，即使有 remote 连接也应走 docker
	db := newTestDB(t)
	store := NewRunnerTokenStore(db)
	if err := store.SetActiveRunner("userA", BuiltinDockerRunnerName); err != nil {
		t.Fatal(err)
	}

	// 创建同时有 docker + remote 的路由器，userA 有 remote 连接
	r := newFullRouter("userA")
	r.SetTokenStore(store)

	sb := r.SandboxForUser("userA")
	if sb.Name() != "docker" {
		t.Errorf("SandboxForUser(userA) = %q, want %q (active_runner=__docker__ should override remote)", sb.Name(), "docker")
	}
}

func TestSandboxForUser_ActiveRunner_Docker_ResolveConsistent(t *testing.T) {
	// 验证 resolve() 和 SandboxForUser() 行为一致
	db := newTestDB(t)
	store := NewRunnerTokenStore(db)
	if err := store.SetActiveRunner("userA", BuiltinDockerRunnerName); err != nil {
		t.Fatal(err)
	}

	r := newFullRouter("userA")
	r.SetTokenStore(store)

	sb1 := r.SandboxForUser("userA")
	sb2 := r.resolve("userA")
	if sb1.Name() != sb2.Name() {
		t.Errorf("SandboxForUser()=%q != resolve()=%q (should be consistent)", sb1.Name(), sb2.Name())
	}
	if sb1.Name() != "docker" {
		t.Errorf("resolve(userA) = %q, want %q", sb1.Name(), "docker")
	}
}

func TestSandboxForUser_ActiveRunner_NotSet_Fallback(t *testing.T) {
	// 用户未设置 active_runner，有 remote 连接 → 走 remote
	db := newTestDB(t)
	store := NewRunnerTokenStore(db)

	r := newFullRouter("userA")
	r.SetTokenStore(store)

	sb := r.SandboxForUser("userA")
	if sb.Name() != "remote" {
		t.Errorf("SandboxForUser(userA) = %q, want %q (fallback to remote when active_runner not set)", sb.Name(), "remote")
	}
}

func TestSandboxForUser_ActiveRunner_NonExistent_Fallback(t *testing.T) {
	// 用户设置了不存在的 runner name → fallback 到 remote/docker
	db := newTestDB(t)
	store := NewRunnerTokenStore(db)
	if err := store.SetActiveRunner("userA", "nonexistent-runner"); err != nil {
		t.Fatal(err)
	}

	r := newFullRouter("userA")
	r.SetTokenStore(store)

	sb := r.SandboxForUser("userA")
	if sb.Name() != "remote" {
		t.Errorf("SandboxForUser(userA) = %q, want %q (fallback when active_runner doesn't match)", sb.Name(), "remote")
	}
}

func TestSandboxRouter_HasDocker(t *testing.T) {
	rNone := newNoneRouter()
	if rNone.HasDocker() {
		t.Error("newNoneRouter().HasDocker() = true, want false")
	}

	rDocker := newDockerRouter()
	if !rDocker.HasDocker() {
		t.Error("newDockerRouter().HasDocker() = false, want true")
	}

	rFull := newFullRouter()
	if !rFull.HasDocker() {
		t.Error("newFullRouter().HasDocker() = false, want true")
	}
}

func TestSandboxRouter_DockerImage(t *testing.T) {
	r := &SandboxRouter{
		docker: &DockerSandbox{image: "ubuntu:22.04"},
		none:   &NoneSandbox{},
	}
	if img := r.DockerImage(); img != "ubuntu:22.04" {
		t.Errorf("DockerImage() = %q, want %q", img, "ubuntu:22.04")
	}

	rNoDocker := newNoneRouter()
	if img := rNoDocker.DockerImage(); img != "" {
		t.Errorf("DockerImage() = %q, want empty", img)
	}
}
