package wake

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/korotovsky/slack-mcp-server/internal/events/models"
	"go.uber.org/zap"
)

// TmuxProvider wakes agents by injecting a prompt into a tmux session.
type TmuxProvider struct {
	defaultSession string
	logger         *zap.Logger
}

// NewTmuxProvider creates a tmux-based wake provider.
func NewTmuxProvider(defaultSession string, logger *zap.Logger) *TmuxProvider {
	return &TmuxProvider{
		defaultSession: defaultSession,
		logger:         logger,
	}
}

// Wake sends a tmux key sequence to the configured session.
func (p *TmuxProvider) Wake(ctx context.Context, req models.WakeRequest) error {
	session := p.defaultSession
	if session == "" {
		return fmt.Errorf("tmux default session is not configured")
	}

	message := fmt.Sprintf(
		"System event received.\n\nwork_item_id=%s\n\nPlease call:\n\nslack_read_work_item(work_item_id='%s')\n",
		req.WorkItemID, req.WorkItemID,
	)

	cmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, message, "C-m")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "session not found") {
			return fmt.Errorf("tmux session %q not found: %w", session, err)
		}
		return fmt.Errorf("tmux send-keys failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	p.logger.Info("wrote wake message to tmux session",
		zap.String("session", session),
		zap.String("work_item_id", req.WorkItemID),
		zap.String("agent_id", req.AgentID))
	return nil
}

// ValidateSession checks whether the configured tmux session exists.
func (p *TmuxProvider) ValidateSession(ctx context.Context) error {
	if p.defaultSession == "" {
		return fmt.Errorf("tmux default session is not configured")
	}

	cmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", p.defaultSession)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux session %q does not exist: %w", p.defaultSession, err)
	}
	return nil
}
