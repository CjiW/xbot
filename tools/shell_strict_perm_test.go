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

func TestCheckDangerousCommand_NoPermControl_AllowsAnySudo(t *testing.T) {
	// No permission control: all sudo forms allowed (bare, -n, -S)
	for _, cmd := range []string{"sudo whoami", "sudo -n whoami", "sudo -S whoami"} {
		blocked, reason := checkDangerousCommand(context.Background(), cmd, false)
		if blocked {
			t.Fatalf("expected %q to be allowed when permission control is disabled, got: %q", cmd, reason)
		}
	}
}
