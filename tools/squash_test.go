package tools

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestSquashTriggersAcrossRestarts is the regression test for the P0 bug:
// commitCount was stored in-memory (dockerContainer.commitCount), resetting to 0
// on every xbot restart, causing squash to NEVER trigger and user images to bloat.
// This test verifies that commit count persists across "restarts" via image labels.
func TestSquashTriggersAcrossRestarts(t *testing.T) {
	skipIfNoDocker(t)

	ws := t.TempDir()
	userID := "test-squash-" + time.Now().Format("20060102-150405")
	userImage := userImageName(userID)
	containerName := "xbot-" + userID
	threshold := 3

	defer func() {
		exec.Command("docker", "rm", "-f", containerName).Run()
		exec.Command("docker", "rmi", "-f", userImage).Run()
	}()

	// Phase 1: Legacy image (exists but has NO commit count label) should trigger
	// immediate squash on the next commit. This simulates a pre-existing image
	// from before the P0 fix was deployed.
	t.Run("legacy_image_triggers_squash", func(t *testing.T) {
		// Step 1: Manually create a legacy image WITHOUT the commit count label.
		// This simulates an image that was built up over many commits before
		// the label-based tracking was introduced.
		exec.Command("docker", "rm", "-f", containerName).Run()
		runCmd := exec.Command("docker", "run", "-d", "--name", containerName,
			"ubuntu:22.04", "tail", "-f", "/dev/null")
		if out, err := runCmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to create container: %v, %s", err, string(out))
		}
		exec.Command("docker", "exec", containerName, "sh", "-c",
			"echo legacy > /tmp/legacy.txt").Run()
		// Commit WITHOUT label → legacy image (no xbot.commit.count)
		commitCmd := exec.Command("docker", "commit", containerName, userImage)
		if out, err := commitCmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to commit legacy image: %v, %s", err, string(out))
		}
		exec.Command("docker", "rm", "-f", containerName).Run()

		// Verify setup: readCommitCount should return -1 (legacy)
		count := readCommitCount(userImage)
		if count != -1 {
			t.Fatalf("setup: expected legacy image to return -1, got %d", count)
		}
		t.Logf("✓ Legacy image created (readCommitCount = -1)")

		// Step 2: Use sandbox to make another change and Close.
		// commitIfDirty should detect legacy (-1), force commitCount = threshold,
		// and trigger immediate squash.
		s := newDockerSandboxWithThreshold("ubuntu:22.04", threshold)
		cmd, args, _ := s.Wrap("sh", []string{"-c", "echo more > /tmp/more.txt"}, nil, ws, userID)
		if out, err := exec.Command(cmd, args...).CombinedOutput(); err != nil {
			t.Fatalf("command failed: %v, output: %s", err, string(out))
		}

		if err := s.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}

		// After squash, label should be "0"
		out, err := exec.Command("docker", "image", "inspect", "-f",
			`{{index .Config.Labels "xbot.commit.count"}}`, userImage).CombinedOutput()
		if err != nil {
			t.Fatalf("image not found after squash: %v", err)
		}
		label := strings.TrimSpace(string(out))
		t.Logf("Commit count label after squash: %q", label)
		if label != "0" {
			t.Errorf("expected label '0' after squash, got %q", label)
		}
	})

	// Phase 2: Multiple restart cycles — count should accumulate from 0
	// (Phase 1 left label=0 after squash).
	t.Run("count_accumulates_across_restarts", func(t *testing.T) {
		// Clean up any leftover container before starting
		exec.Command("docker", "rm", "-f", containerName).Run()

		for i := 1; i <= threshold; i++ {
			// Clean up before each round to avoid stale container issues
			exec.Command("docker", "rm", "-f", containerName).Run()

			s := newDockerSandboxWithThreshold("ubuntu:22.04", threshold)

			// Make a change each cycle
			cmd, args, _ := s.Wrap("sh", []string{"-c",
				fmt.Sprintf("echo round%d > /tmp/round.txt", i)}, nil, ws, userID)
			if out, err := exec.Command(cmd, args...).CombinedOutput(); err != nil {
				t.Fatalf("round %d command failed: %v, output: %s", i, err, string(out))
			}

			if err := s.Close(); err != nil {
				t.Fatalf("round %d Close failed: %v", i, err)
			}

			// Check label
			out, _ := exec.Command("docker", "image", "inspect", "-f",
				`{{index .Config.Labels "xbot.commit.count"}}`, userImage).CombinedOutput()
			label := strings.TrimSpace(string(out))
			t.Logf("After round %d: commit count label = %q", i, label)

			if i < threshold {
				// Before threshold, count should be i
				if label != fmt.Sprintf("%d", i) {
					t.Errorf("round %d: expected count %d, got %q", i, i, label)
				}
			}
		}

		// After threshold rounds, squash should have triggered (label reset to 0)
		out, _ := exec.Command("docker", "image", "inspect", "-f",
			`{{index .Config.Labels "xbot.commit.count"}}`, userImage).CombinedOutput()
		label := strings.TrimSpace(string(out))
		t.Logf("After threshold (%d) rounds: commit count label = %q", threshold, label)
		if label != "0" {
			t.Errorf("expected label '0' after threshold squash, got %q", label)
		}
	})
}

// TestReadCommitCount_LabelParsing tests the readCommitCount function directly.
// It creates real Docker images with/without labels to verify parsing logic.
func TestReadCommitCount_LabelParsing(t *testing.T) {
	skipIfNoDocker(t)

	imageName := "xbot-test-readcount-" + time.Now().Format("20060102-150405")
	defer exec.Command("docker", "rmi", "-f", imageName).Run()

	// Case 1: Non-existent image → 0
	count := readCommitCount(imageName)
	if count != 0 {
		t.Errorf("non-existent image: expected 0, got %d", count)
	}
	t.Logf("✓ Non-existent image returns 0")

	// Create a container and commit without label (legacy)
	containerName := "xbot-test-readcount-c"
	exec.Command("docker", "rm", "-f", containerName).Run()
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	runCmd := exec.Command("docker", "run", "-d", "--name", containerName, "ubuntu:22.04", "sleep", "60")
	if out, err := runCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create container: %v, %s", err, string(out))
	}

	// Commit without label → should return -1 (legacy)
	commitCmd := exec.Command("docker", "commit", containerName, imageName)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to commit: %v, %s", err, string(out))
	}

	count = readCommitCount(imageName)
	if count != -1 {
		t.Errorf("image without label: expected -1 (legacy), got %d", count)
	}
	t.Logf("✓ Image without label returns -1 (legacy)")

	// Commit with label=5
	labelCmd := exec.Command("docker", "commit",
		"--change", "LABEL xbot.commit.count=5",
		containerName, imageName)
	if out, err := labelCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to commit with label: %v, %s", err, string(out))
	}

	count = readCommitCount(imageName)
	if count != 5 {
		t.Errorf("image with label=5: expected 5, got %d", count)
	}
	t.Logf("✓ Image with label=5 returns 5")

	// Commit with label=0 (after squash)
	labelCmd = exec.Command("docker", "commit",
		"--change", "LABEL xbot.commit.count=0",
		containerName, imageName)
	if out, err := labelCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to commit with label=0: %v, %s", err, string(out))
	}

	count = readCommitCount(imageName)
	if count != 0 {
		t.Errorf("image with label=0: expected 0, got %d", count)
	}
	t.Logf("✓ Image with label=0 returns 0")

	// Commit with invalid label value
	labelCmd = exec.Command("docker", "commit",
		"--change", "LABEL xbot.commit.count=abc",
		containerName, imageName)
	if out, err := labelCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to commit with invalid label: %v, %s", err, string(out))
	}

	count = readCommitCount(imageName)
	if count != -1 {
		t.Errorf("image with invalid label: expected -1, got %d", count)
	}
	t.Logf("✓ Image with invalid label returns -1")

	exec.Command("docker", "stop", "-t", "1", containerName).Run()
	exec.Command("docker", "rm", "-f", containerName).Run()
}
