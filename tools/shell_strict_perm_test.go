package tools

import (
	"context"
	"strings"
	"testing"
)

func TestCheckDangerousCommand_StrictPermControlBlocksAllSudo(t *testing.T) {
	ctx := WithPermUsers(context.Background(), "user", "root")
	blocked, reason := checkDangerousCommand(ctx, "sudo -n whoami", false)
	if !blocked {
		t.Fatal("expected sudo to be blocked when permission control is enabled")
	}
	if !strings.Contains(reason, "permission control is enabled") {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestCheckDangerousCommand_RunAsStillBlocksSudo(t *testing.T) {
	ctx := WithPermUsers(context.Background(), "user", "root")
	blocked, reason := checkDangerousCommand(ctx, "sudo -n whoami", true)
	if !blocked {
		t.Fatal("expected sudo to be blocked when run_as is set")
	}
	if !strings.Contains(reason, "run_as is set") {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestCheckDangerousCommand_NoPermControl_AllowsNonBareSudo(t *testing.T) {
	blocked, reason := checkDangerousCommand(context.Background(), "sudo -n whoami", false)
	if blocked {
		t.Fatalf("expected sudo -n to remain allowed when permission control is disabled, got: %q", reason)
	}
}

func TestCheckDangerousCommand_NoPermControl_BlocksBareSudo(t *testing.T) {
	blocked, reason := checkDangerousCommand(context.Background(), "sudo whoami", false)
	if !blocked {
		t.Fatal("expected bare sudo to be blocked")
	}
	if !strings.Contains(reason, "bare sudo") {
		t.Fatalf("unexpected reason: %q", reason)
	}
}
