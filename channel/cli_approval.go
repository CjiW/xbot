package channel

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
	resultCh := make(chan tools.ApprovalResult, 1)

	// Send approval request to the TUI
	if h.program != nil {
		h.program.Send(approvalRequestMsg{
			request:  req,
			resultCh: resultCh,
		})
	}

	// Wait for user response or context cancellation
	select {
	case result := <-resultCh:
		return result, nil
	case <-ctx.Done():
		return tools.ApprovalResult{Approved: false}, fmt.Errorf("approval request timed out")
	}
}

// approvalRequestMsg is a Tea message that triggers the approval dialog.
type approvalRequestMsg struct {
	request  tools.ApprovalRequest
	resultCh chan<- tools.ApprovalResult
}

// --- Panel: Update (key handling) ---

// updateApprovalPanel handles key events for the approval dialog.
func (m *cliModel) updateApprovalPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.approvalEnteringDeny {
		var cmd tea.Cmd
		m.approvalDenyInput, cmd = m.approvalDenyInput.Update(msg)
		m.renderCacheValid = false
		switch msg.Code {
		case tea.KeyEnter:
			m.resolveApproval(false, strings.TrimSpace(m.approvalDenyInput.Value()))
			return true, m, nil
		case tea.KeyEscape:
			m.approvalEnteringDeny = false
			m.approvalDenyInput.Blur()
			return true, m, nil
		}
		return true, m, cmd
	}

	switch msg.Code {
	case tea.KeyLeft, tea.KeyUp:
		m.approvalCursor = 0 // Approve
		m.renderCacheValid = false
		return true, m, nil
	case tea.KeyRight, tea.KeyDown:
		m.approvalCursor = 1 // Deny
		m.renderCacheValid = false
		return true, m, nil
	case tea.KeyTab:
		m.approvalCursor = (m.approvalCursor + 1) % 2
		m.renderCacheValid = false
		return true, m, nil
	case tea.KeyEnter:
		if m.approvalCursor == 0 {
			m.resolveApproval(true, "")
		} else {
			m.approvalEnteringDeny = true
			m.approvalDenyInput.Focus()
		}
		return true, m, nil
	case tea.KeyEscape:
		m.resolveApproval(false, "") // Esc = deny without reason
		return true, m, nil
	}

	if msg.Code == 0 {
		switch msg.String() {
		case "y", "Y":
			m.resolveApproval(true, "")
			return true, m, nil
		case "n", "N":
			m.approvalEnteringDeny = true
			m.approvalDenyInput.Focus()
			m.renderCacheValid = false
			return true, m, nil
		}
	}

	return true, m, nil // swallow all keys in approval mode
}

// resolveApproval sends the result and closes the approval panel.
func (m *cliModel) resolveApproval(approved bool, denyReason string) {
	if m.approvalResultCh != nil {
		m.approvalResultCh <- tools.ApprovalResult{Approved: approved, DenyReason: denyReason}
		m.approvalResultCh = nil
	}
	m.approvalRequest = nil
	m.approvalCursor = 0
	m.approvalEnteringDeny = false
	m.panelMode = ""
	m.renderCacheValid = false
}

// --- Panel: View (rendering) ---

// viewApprovalPanel renders the approval dialog content.
func (m *cliModel) viewApprovalPanel() string {
	if m.approvalRequest == nil {
		return ""
	}

	s := m.styles
	req := m.approvalRequest
	var sb strings.Builder

	// Header
	sb.WriteString(s.PanelHeader.Render("⚠ Permission Required"))
	sb.WriteString("\n")
	sb.WriteString(s.SettingsDivider.Render("┈" + strings.Repeat("┈", 30)))
	sb.WriteString("\n\n")

	// Details
	warnSt := lipgloss.NewStyle().Foreground(s.PanelHeader.GetForeground()).Bold(true)
	labelSt := lipgloss.NewStyle().Foreground(s.PanelDesc.GetForeground())
	valueSt := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))

	sb.WriteString(warnSt.Render(fmt.Sprintf("  LLM wants to execute as %q", req.RunAs)))
	sb.WriteString("\n\n")

	sb.WriteString(labelSt.Render("  Tool:    "))
	sb.WriteString(valueSt.Render(req.ToolName))
	sb.WriteString("\n")

	if req.Command != "" {
		sb.WriteString(labelSt.Render("  Command: "))
		sb.WriteString(valueSt.Render(req.Command))
		sb.WriteString("\n")
	}
	if req.FilePath != "" {
		sb.WriteString(labelSt.Render("  File:    "))
		sb.WriteString(valueSt.Render(req.FilePath))
		sb.WriteString("\n")
	}
	if req.ArgsSummary != "" && req.ArgsSummary != req.Command && req.ArgsSummary != req.FilePath {
		sb.WriteString(labelSt.Render("  Args:    "))
		sb.WriteString(valueSt.Render(req.ArgsSummary))
		sb.WriteString("\n")
	}
	if req.Reason != "" {
		sb.WriteString(labelSt.Render("  Reason:  "))
		sb.WriteString(valueSt.Render(req.Reason))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	if m.approvalEnteringDeny {
		sb.WriteString(labelSt.Render("  Deny note: "))
		sb.WriteString("\n")
		sb.WriteString("  " + m.approvalDenyInput.View())
		sb.WriteString("\n")
		sb.WriteString(labelSt.Render("  Enter submit deny, Esc back"))
		sb.WriteString("\n\n")
	}

	// Buttons
	approveLabel := "  Approve (y)  "
	denyLabel := "  Deny (n)  "

	activeSt := lipgloss.NewStyle().Background(lipgloss.Color("#22c55e")).Foreground(lipgloss.Color("#000000")).Bold(true)
	activeRedSt := lipgloss.NewStyle().Background(lipgloss.Color("#ef4444")).Foreground(lipgloss.Color("#ffffff")).Bold(true)
	inactiveSt := lipgloss.NewStyle().Foreground(s.PanelDesc.GetForeground()).Faint(true)

	var approve, deny string
	if m.approvalCursor == 0 {
		approve = activeSt.Render(approveLabel)
		deny = inactiveSt.Render(denyLabel)
	} else {
		approve = inactiveSt.Render(approveLabel)
		deny = activeRedSt.Render(denyLabel)
	}

	sb.WriteString("  " + approve + "    " + deny)
	sb.WriteString("\n")

	return sb.String()
}
