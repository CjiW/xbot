package tools

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker not available, skipping test")
	}
	checkCmd := exec.Command("docker", "info")
	if err := checkCmd.Run(); err != nil {
		t.Skip("Docker daemon not running, skipping test")
	}
}

func TestDockerSandboxShellPath(t *testing.T) {
	skipIfNoDocker(t)

	ws, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp workspace: %v", err)
	}
	defer os.RemoveAll(ws)

	s := newDockerSandbox("node:20-slim")

	cmd, args, err := s.Wrap("echo", []string{"hello"}, nil, ws, "test-shell-path")
	if err != nil {
		t.Fatalf("Failed to wrap command: %v", err)
	}

	execCmd := exec.Command(cmd, args...)
	execCmd.Dir = ws
	output, err := execCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to execute wrapped command: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(string(output), "hello") {
		t.Errorf("Unexpected output: %s", string(output))
	}

	s.Close()
}

func TestDockerSandboxEnvVariables(t *testing.T) {
	skipIfNoDocker(t)

	ws, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp workspace: %v", err)
	}
	defer os.RemoveAll(ws)

	s := newDockerSandbox("node:20-slim")

	env := []string{"MY_VAR=test_value"}
	// 确保容器已创建（Wrap 会自动 getOrCreateContainer）
	cmd, args, err := s.Wrap("sh", []string{"-c", "echo $MY_VAR"}, env, ws, "test-env")
	if err != nil {
		t.Fatalf("Failed to wrap command: %v", err)
	}

	execCmd := exec.Command(cmd, args...)
	execCmd.Dir = ws
	output, err := execCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to execute wrapped command: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(string(output), "test_value") {
		t.Errorf("Environment variable not passed, output: %s", string(output))
	}

	s.Close()
}

func TestDockerSandboxExportPersistence(t *testing.T) {
	skipIfNoDocker(t)

	ws, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp workspace: %v", err)
	}
	defer os.RemoveAll(ws)

	userID := "test-export"
	userImage := userImageName(userID)

	defer func() {
		exec.Command("docker", "rm", "-f", "xbot-"+userID).Run()
		exec.Command("docker", "rmi", userImage).Run()
	}()

	// 第一轮：创建文件并 Close（触发 export+import）
	s1 := newDockerSandbox("node:20-slim")
	cmd, args, err := s1.Wrap("sh", []string{"-c", "echo persist > /workspace/testfile"}, nil, ws, userID)
	if err != nil {
		t.Fatalf("Wrap failed: %v", err)
	}
	execCmd := exec.Command(cmd, args...)
	if out, err := execCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create testfile: %v\nOutput: %s", err, out)
	}
	s1.Close()

	// 第二轮：用新 sandbox 实例（模拟重启），应自动使用 export+import 镜像
	s2 := newDockerSandbox("node:20-slim")
	cmd, args, err = s2.Wrap("cat", []string{"/workspace/testfile"}, nil, ws, userID)
	if err != nil {
		t.Fatalf("Wrap failed: %v", err)
	}
	execCmd = exec.Command(cmd, args...)
	output, err := execCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to read testfile: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(string(output), "persist") {
		t.Errorf("Data not persisted across export, got: %s", string(output))
	}

	s2.Close()
}

func newDockerSandbox(image string) *dockerSandbox {
	return &dockerSandbox{
		image:      image,
		containers: make(map[string]*dockerContainer),
	}
}
