package channel

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"xbot/tools"
)

// CLIApprovalHandler implements tools.ApprovalHandler for the CLI channel.
// It uses the Bubble Tea TUI to present approval dialogs.
type CLIApprovalHandler struct {
	program *tea.Program
}

// NewCLIApprovalHandler creates a new CLIApprovalHandler.
func NewCLIApprovalHandler(program *tea.Program) *CLIApprovalHandler {
	return &CLIApprovalHandler{program: program}
}

// RequestApproval sends an approval request to the TUI and blocks until the user responds.
func (h *CLIApprovalHandler) RequestApproval(ctx context.Context, req tools.ApprovalRequest) (tools.ApprovalResult, error) {
	// Create a channel to receive the user's response
	resultCh := make(chan bool, 1)

	// Send approval request to the TUI
	if h.program != nil {
		h.program.Send(approvalRequestMsg{
			request:  req,
			resultCh: resultCh,
		})
	}

	// Wait for user response or context cancellation
	select {
	case approved := <-resultCh:
		if approved {
			return tools.ApprovalApproved, nil
		}
		return tools.ApprovalDenied, nil
	case <-ctx.Done():
		return tools.ApprovalDenied, fmt.Errorf("approval request timed out")
	}
}

// approvalRequestMsg is a Tea message that triggers the approval dialog.
type approvalRequestMsg struct {
	request  tools.ApprovalRequest
	resultCh chan<- bool
}
