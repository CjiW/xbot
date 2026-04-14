//go:build !windows

package tools

import (
	"bytes"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestExecKeepAlive_ChildHoldsPipeOpen reproduces the hang where a child process
// inherits pipe FDs and prevents cmd.Wait() from returning.
// The scenario: login shell sources profile, spawning background processes that
// hold stdout/stderr FDs open. Even after the main process exits, io.Copy blocks.
func TestExecKeepAlive_ChildHoldsPipeOpen(t *testing.T) {
	// This command exits immediately but spawns a background child holding pipe FDs.
	// The subshell background sleep inherits stdout, so the pipe write end stays open.
	// Use ";" (not "&&") to ensure echo runs unconditionally regardless of background exit status.
	cmd := exec.Command("/bin/sh", "-c", "(sleep 300 &); echo done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, stderrPipe, err := setupPipes(cmd)
	if err != nil {
		t.Fatalf("setupPipes: %v", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)

	capture := func(dst *bytes.Buffer, r interface{ Read([]byte) (int, error) }) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				dst.Write(buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}
	go capture(&stdoutBuf, stdoutPipe)
	go capture(&stderrBuf, stderrPipe)

	// Use cmd.Process.Wait() + kill process group + close pipes approach.
	// After the direct child exits, grandchild processes (e.g. "sleep 300 &")
	// may still hold pipe FDs open. We must kill the entire process group
	// before closing pipes, otherwise Read() in capture goroutines never
	// returns EOF and wg.Wait() hangs.
	waitCh := make(chan int, 1)
	go func() {
		state, err := cmd.Process.Wait()
		code := -1
		if err == nil && state != nil {
			code = extractExitCodeFromState(state)
		}
		// Kill process group to release pipe FDs held by background
		// sleep, then wait briefly for capture goroutines to drain
		// before closing our pipe ends (avoids race on slow CI).
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		time.Sleep(10 * time.Millisecond)
		stdoutPipe.Close()
		stderrPipe.Close()
		wg.Wait()
		waitCh <- code
	}()

	select {
	case code := <-waitCh:
		t.Logf("Process exited with code %d in time", code)
		if code != 0 {
			t.Errorf("expected exit code 0, got %d", code)
		}
		output := stdoutBuf.String()
		if output != "done\n" {
			t.Errorf("expected 'done\\n', got %q", output)
		}
	case <-time.After(10 * time.Second):
		// Kill the process group to clean up
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("execKeepAlive hung: cmd.Process.Wait() did not return within 10s")
	}
}

// TestExecKeepAlive_ContextCancel tests that canceling the context kills the
// process group immediately, even when child processes hold pipes open.
func TestExecKeepAlive_ContextCancel(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo started; sleep 300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, stderrPipe, err := setupPipes(cmd)
	if err != nil {
		t.Fatalf("setupPipes: %v", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)

	capture := func(dst *bytes.Buffer, r interface{ Read([]byte) (int, error) }) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				dst.Write(buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}
	go capture(&stdoutBuf, stdoutPipe)
	go capture(&stderrBuf, stderrPipe)

	done := make(chan int, 1)
	go func() {
		state, err := cmd.Process.Wait()
		code := -1
		if err == nil && state != nil {
			code = extractExitCodeFromState(state)
		}
		stdoutPipe.Close()
		stderrPipe.Close()
		wg.Wait()
		done <- code
	}()

	// Give the process a moment to start
	time.Sleep(100 * time.Millisecond)

	// Simulate Ctrl+C: kill the entire process group
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

	select {
	case code := <-done:
		t.Logf("Process killed, exit code %d", code)
		output := stdoutBuf.String()
		if output != "started\n" {
			t.Errorf("expected 'started\\n', got %q", output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Process group kill did not take effect within 5s")
	}
}
