//go:build !windows

package tools

import (
	"reflect"
	"testing"
)

func TestLoginShellArgs_Bash(t *testing.T) {
	got := LoginShellArgs("bash", "echo hello")
	want := []string{"bash", "-l", "-c", "echo hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginShellArgs(bash, ...) = %v, want %v", got, want)
	}
}

func TestLoginShellArgs_Sh(t *testing.T) {
	got := LoginShellArgs("sh", "echo hello")
	want := []string{"sh", "-l", "-c", "echo hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginShellArgs(sh, ...) = %v, want %v", got, want)
	}
}

func TestLoginShellArgs_Dash(t *testing.T) {
	got := LoginShellArgs("dash", "echo hello")
	want := []string{"dash", "-l", "-c", "echo hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginShellArgs(dash, ...) = %v, want %v", got, want)
	}
}

func TestLoginShellArgs_Zsh(t *testing.T) {
	got := LoginShellArgs("zsh", "echo hello")
	want := []string{"zsh", "-c", "source ~/.zshrc 2>/dev/null; echo hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginShellArgs(zsh, ...) = %v, want %v", got, want)
	}
}

func TestLoginShellArgs_ZshWithFullPath(t *testing.T) {
	got := LoginShellArgs("/usr/bin/zsh", "echo hello")
	want := []string{"/usr/bin/zsh", "-c", "source ~/.zshrc 2>/dev/null; echo hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginShellArgs(/usr/bin/zsh, ...) = %v, want %v", got, want)
	}
}

func TestLoginShellArgs_BashWithFullPath(t *testing.T) {
	got := LoginShellArgs("/usr/local/bin/bash", "echo hello")
	want := []string{"/usr/local/bin/bash", "-l", "-c", "echo hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginShellArgs(/usr/local/bin/bash, ...) = %v, want %v", got, want)
	}
}
