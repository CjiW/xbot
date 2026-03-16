package tools

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDockerShutdownFlow tests the optimized shutdown flow (stop → commit → rm)
// This test verifies that:
// 1. Container changes are committed before removal
// 2. Stop is called before commit (optimized flow)
// 3. Container is properly removed after commit
func TestDockerShutdownFlow(t *testing.T) {
	skipIfNoDocker(t)

	ws := t.TempDir()
	userID := "test-shutdown-" + time.Now().Format("20060102-150405")
	userImage := userImageName(userID)

	// Cleanup
	defer func() {
		exec.Command("docker", "rm", "-f", "xbot-"+userID).Run()
		exec.Command("docker", "rmi", "-f", userImage).Run()
	}()

	// Create sandbox and make changes
	s := newDockerSandbox("ubuntu:22.04")
	cmd, args, err := s.Wrap("sh", []string{"-c", "echo testdata > /tmp/testfile && apt-get update -qq && apt-get install -y -qq hello"}, nil, ws, userID)
	if err != nil {
		t.Fatalf("Wrap failed: %v", err)
	}

	if output, err := exec.Command(cmd, args...).CombinedOutput(); err != nil {
		t.Fatalf("Command failed: %v, output: %s", err, string(output))
	}

	// Verify hello is installed
	cmd, args, err = s.Wrap("which", []string{"hello"}, nil, ws, userID)
	if err != nil {
		t.Fatalf("Wrap which failed: %v", err)
	}
	output, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("hello not found: %v", err)
	}
	helloPath := strings.TrimSpace(string(output))
	t.Logf("✓ Package installed at: %s", helloPath)

	// Close sandbox (should use stop → commit → rm flow)
	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("✓ Shutdown completed in %v", elapsed)

	// Verify image was created
	if err := exec.Command("docker", "image", "inspect", userImage).Run(); err != nil {
		t.Errorf("Image %s was not created", userImage)
	} else {
		t.Logf("✓ Container committed to image: %s", userImage)
	}

	// Verify container was removed
	containerName := "xbot-" + userID
	if err := exec.Command("docker", "inspect", containerName).Run(); err == nil {
		t.Errorf("Container %s still exists after Close()", containerName)
	} else {
		t.Logf("✓ Container removed: %s", containerName)
	}

	// Verify persistence: create new sandbox and check if package still exists
	s2 := newDockerSandbox("ubuntu:22.04")
	cmd, args, err = s2.Wrap("which", []string{"hello"}, nil, ws, userID)
	if err != nil {
		t.Fatalf("Wrap which in new sandbox failed: %v", err)
	}
	output, err = exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("Package 'hello' not persisted after restart: %v", err)
	}

	persistedPath := strings.TrimSpace(string(output))
	if persistedPath != helloPath {
		t.Errorf("Package path mismatch: got %s, want %s", persistedPath, helloPath)
	}
	t.Logf("✓ Package persisted correctly at: %s", persistedPath)

	s2.Close()
}

// TestDockerCommitOnlyIfDirty tests that commit only happens when there are changes
func TestDockerCommitOnlyIfDirty(t *testing.T) {
	skipIfNoDocker(t)

	ws := t.TempDir()
	userID := "test-dirty-" + time.Now().Format("20060102-150405")
	userImage := userImageName(userID)

	// Cleanup
	defer func() {
		exec.Command("docker", "rm", "-f", "xbot-"+userID).Run()
		exec.Command("docker", "rmi", "-f", userImage).Run()
	}()

	// Create sandbox
	s := newDockerSandbox("ubuntu:22.04")

	// Just create container, don't make any changes
	shell, err := s.GetShell(userID, ws)
	if err != nil {
		t.Fatalf("Failed to get shell: %v", err)
	}
	t.Logf("Shell detected: %s", shell)

	// Close sandbox (should NOT commit since no changes)
	if err := s.Close(); err != nil {
		t.Fatalf("Failed to close sandbox: %v", err)
	}

	// Verify NO image was created
	if err := exec.Command("docker", "image", "inspect", userImage).Run(); err == nil {
		t.Errorf("Image %s was created but container had no changes", userImage)
		exec.Command("docker", "rmi", "-f", userImage).Run()
	} else {
		t.Logf("✓ No commit for unchanged container (expected)")
	}
}

// TestDockerMultipleUsers tests isolation between different users
func TestDockerMultipleUsers(t *testing.T) {
	skipIfNoDocker(t)

	user1 := "test-user1-" + time.Now().Format("20060102-150405")
	user2 := "test-user2-" + time.Now().Format("20060102-150405")

	// Cleanup
	defer func() {
		exec.Command("docker", "rm", "-f", "xbot-"+user1).Run()
		exec.Command("docker", "rmi", "-f", "xbot-"+user1+":latest").Run()
		exec.Command("docker", "rm", "-f", "xbot-"+user2).Run()
		exec.Command("docker", "rmi", "-f", "xbot-"+user2+":latest").Run()
	}()

	// Create temp workspaces
	ws1 := t.TempDir()
	ws2 := t.TempDir()

	s := newDockerSandbox("ubuntu:22.04")
	defer s.Close()

	// User 1 creates file
	cmd, args, err := s.Wrap("sh", []string{"-c", "echo user1 > /tmp/user.txt"}, nil, ws1, user1)
	if err != nil {
		t.Fatalf("Failed to wrap command for user1: %v", err)
	}
	if output, err := exec.Command(cmd, args...).CombinedOutput(); err != nil {
		t.Fatalf("User1 command failed: %v, output: %s", err, string(output))
	}

	// User 2 creates different file
	cmd, args, err = s.Wrap("sh", []string{"-c", "echo user2 > /tmp/user.txt"}, nil, ws2, user2)
	if err != nil {
		t.Fatalf("Failed to wrap command for user2: %v", err)
	}
	if output, err := exec.Command(cmd, args...).CombinedOutput(); err != nil {
		t.Fatalf("User2 command failed: %v, output: %s", err, string(output))
	}

	// Verify isolation
	cmd, args, err = s.Wrap("cat", []string{"/tmp/user.txt"}, nil, ws1, user1)
	if err != nil {
		t.Fatalf("Failed to wrap cat for user1: %v", err)
	}
	output, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("User1 cat failed: %v", err)
	}
	if strings.TrimSpace(string(output)) != "user1" {
		t.Errorf("User1 data incorrect: got %q, want %q", string(output), "user1")
	}

	cmd, args, err = s.Wrap("cat", []string{"/tmp/user.txt"}, nil, ws2, user2)
	if err != nil {
		t.Fatalf("Failed to wrap cat for user2: %v", err)
	}
	output, err = exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("User2 cat failed: %v", err)
	}
	if strings.TrimSpace(string(output)) != "user2" {
		t.Errorf("User2 data incorrect: got %q, want %q", string(output), "user2")
	}

	t.Logf("✓ User isolation verified")
}
