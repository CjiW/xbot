//go:build windows

package tools

import (
	"reflect"
	"testing"
)

func TestLoginShellArgs_PowerShell(t *testing.T) {
	got := LoginShellArgs("powershell.exe", "echo hello")
	want := []string{"powershell.exe", "-Command", "echo hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginShellArgs(powershell.exe, ...) = %v, want %v", got, want)
	}
}

func TestLoginShellArgs_PowerShellFullPath(t *testing.T) {
	got := LoginShellArgs("C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe", "echo hello")
	want := []string{"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe", "-Command", "echo hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginShellArgs(fullpath, ...) = %v, want %v", got, want)
	}
}
