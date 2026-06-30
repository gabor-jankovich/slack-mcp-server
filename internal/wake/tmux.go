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

// slashCommands maps !-prefixed Slack commands to Claude Code slash commands.
var slashCommands = map[string]string{
	"compact": "/compact",
	"clear":   "/clear",
	"status":  "/status",
}

// Wake sends a tmux key sequence to the configured session.
// If the message starts with "!", it is treated as a direct slash command.
func (p *TmuxProvider) Wake(ctx context.Context, req models.WakeRequest) error {
	session := req.TmuxSession
	if session == "" {
		session = p.defaultSession
	}
	if session == "" {
		return fmt.Errorf("tmux session is not configured for agent %q", req.AgentID)
	}

	// Check if this is a ! command that maps to a slash command.
	msgText := strings.TrimSpace(req.MessageText)
	if strings.HasPrefix(msgText, "!") {
		cmdName := strings.TrimPrefix(msgText, "!")
		cmdName = strings.SplitN(cmdName, " ", 2)[0] // first word only
		if slashCmd, ok := slashCommands[cmdName]; ok {
			return p.sendSlashCommand(ctx, session, slashCmd, req)
		}
		// Not a known slash command — fall through to normal system event.
	}

	message := fmt.Sprintf(
		"System event received.\n\nwork_item_id=%s\n\nPlease call:\n\nslack_read_work_item(work_item_id='%s')",
		req.WorkItemID, req.WorkItemID,
	)

	if err := p.sendKeys(ctx, session, message); err != nil {
		return err
	}

	p.logger.Info("wrote wake message to tmux session",
		zap.String("session", session),
		zap.String("work_item_id", req.WorkItemID),
		zap.String("agent_id", req.AgentID))
	return nil
}

// sendSlashCommand sends a Claude Code slash command (e.g. /compact) to the tmux session.
func (p *TmuxProvider) sendSlashCommand(ctx context.Context, session, slashCmd string, req models.WakeRequest) error {
	if err := p.sendKeys(ctx, session, slashCmd); err != nil {
		return err
	}

	p.logger.Info("sent slash command to tmux session",
		zap.String("session", session),
		zap.String("command", slashCmd),
		zap.String("work_item_id", req.WorkItemID),
		zap.String("agent_id", req.AgentID))
	return nil
}

// sendKeys sends literal text followed by Enter to a tmux session.
func (p *TmuxProvider) sendKeys(ctx context.Context, session, text string) error {
	// Send the text literally (-l flag prevents interpreting special keys in the text).
	cmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "-l", text)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "session not found") {
			return fmt.Errorf("tmux session %q not found: %w", session, err)
		}
		return fmt.Errorf("tmux send-keys failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Send Enter as a separate key event to submit the text.
	enterCmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "Enter")
	if enterOut, enterErr := enterCmd.CombinedOutput(); enterErr != nil {
		return fmt.Errorf("tmux send-keys Enter failed: %w: %s", enterErr, strings.TrimSpace(string(enterOut)))
	}
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
