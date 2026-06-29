package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/korotovsky/slack-mcp-server/internal/events/models"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// AgentRuntimeHandler implements MCP tools for the agent runtime subsystem.
type AgentRuntimeHandler struct {
	apiProvider *provider.ApiProvider
	store       models.EventStore
	config      models.RuntimeConfig
	logger      *zap.Logger
}

// NewAgentRuntimeHandler creates a new handler for agent runtime tools.
func NewAgentRuntimeHandler(apiProvider *provider.ApiProvider, store models.EventStore, config models.RuntimeConfig, logger *zap.Logger) *AgentRuntimeHandler {
	return &AgentRuntimeHandler{
		apiProvider: apiProvider,
		store:       store,
		config:      config,
		logger:      logger,
	}
}

// ReadWorkItemHandler fetches unseen messages for a work item and moves it to PROCESSING.
func (h *AgentRuntimeHandler) ReadWorkItemHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workItemID := request.GetString("work_item_id", "")
	if workItemID == "" {
		return nil, errors.New("work_item_id is required")
	}

	item, err := h.store.LoadByID(ctx, workItemID)
	if err != nil {
		return nil, fmt.Errorf("loading work item: %w", err)
	}

	if item.Status != models.StatusLeased && item.Status != models.StatusProcessing {
		return nil, fmt.Errorf("work item is not in LEASED or PROCESSING state: %s", item.Status)
	}

	state, err := h.store.LoadThreadState(ctx, item.ChannelID, item.ThreadTS)
	if err != nil {
		return nil, fmt.Errorf("loading thread state: %w", err)
	}

	params := &slack.GetConversationRepliesParameters{
		ChannelID: item.ChannelID,
		Timestamp: item.ThreadTS,
		Limit:     100,
		Inclusive: false,
	}
	if state.LastProcessedMessageTS != "" {
		params.Oldest = state.LastProcessedMessageTS
	}

	replies, hasMore, nextCursor, err := h.apiProvider.Slack().GetConversationRepliesContext(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("fetching replies: %w", err)
	}

	var messages []Message
	for _, msg := range replies {
		if msg.Timestamp == state.LastProcessedMessageTS {
			continue
		}
		messages = append(messages, h.convertMessage(msg, item.ChannelID))
	}

	if item.Status == models.StatusLeased {
		item.Status = models.StatusProcessing
		item.UpdatedAt = time.Now().UTC()
		if _, err := h.store.UpdateWorkItem(ctx, item); err != nil {
			h.logger.Warn("failed to update work item status to PROCESSING", zap.Error(err))
		}
	}

	h.logger.Info("read work item",
		zap.String("work_item_id", workItemID),
		zap.Int("message_count", len(messages)))

	if len(messages) > 0 && hasMore {
		messages[len(messages)-1].Cursor = nextCursor
	}
	return marshalMessagesToCSV(messages)
}

// AckWorkItemHandler acknowledges a work item and advances the thread cursor.
func (h *AgentRuntimeHandler) AckWorkItemHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workItemID := request.GetString("work_item_id", "")
	if workItemID == "" {
		return nil, errors.New("work_item_id is required")
	}

	agentID := h.agentID(request)

	item, err := h.store.LoadByID(ctx, workItemID)
	if err != nil {
		return nil, fmt.Errorf("loading work item: %w", err)
	}

	if _, err := h.store.Ack(ctx, workItemID, agentID, item.NewestMessageTS); err != nil {
		return nil, fmt.Errorf("acking work item: %w", err)
	}

	h.logger.Info("acked work item",
		zap.String("work_item_id", workItemID),
		zap.String("agent_id", agentID))

	return mcp.NewToolResultText("acknowledged"), nil
}

// HeartbeatWorkItemHandler renews the lease for a work item.
func (h *AgentRuntimeHandler) HeartbeatWorkItemHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workItemID := request.GetString("work_item_id", "")
	if workItemID == "" {
		return nil, errors.New("work_item_id is required")
	}

	agentID := h.agentID(request)
	ok, err := h.store.RenewLease(ctx, workItemID, agentID, h.config.LeaseDuration)
	if err != nil {
		return nil, fmt.Errorf("renewing lease: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("lease not held by agent %s", agentID)
	}

	return mcp.NewToolResultText("heartbeat accepted"), nil
}

// RegisterAgentHandler registers an agent and its tmux session.
func (h *AgentRuntimeHandler) RegisterAgentHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentID := request.GetString("agent_id", "")
	if agentID == "" {
		agentID = h.config.DefaultAgentID
	}
	tmuxSession := request.GetString("tmux_session", "")

	agent := models.Agent{
		AgentID:     agentID,
		TmuxSession: tmuxSession,
		Status:      "registered",
	}
	if _, err := h.store.RegisterAgent(ctx, agent); err != nil {
		return nil, fmt.Errorf("registering agent: %w", err)
	}

	h.logger.Info("registered agent",
		zap.String("agent_id", agentID),
		zap.String("tmux_session", tmuxSession))

	return mcp.NewToolResultText(fmt.Sprintf("agent %s registered", agentID)), nil
}

// WatchThreadHandler registers a thread for polling.
func (h *AgentRuntimeHandler) WatchThreadHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	channelID := request.GetString("channel_id", "")
	threadTS := request.GetString("thread_ts", "")
	if channelID == "" || threadTS == "" {
		return nil, errors.New("channel_id and thread_ts are required")
	}

	state := models.ThreadState{
		ThreadTS:               threadTS,
		ChannelID:              channelID,
		LastProcessedMessageTS: "",
	}
	if _, err := h.store.SaveThreadState(ctx, state); err != nil {
		return nil, fmt.Errorf("saving thread state: %w", err)
	}

	h.logger.Info("watching thread",
		zap.String("channel_id", channelID),
		zap.String("thread_ts", threadTS))

	return mcp.NewToolResultText(fmt.Sprintf("watching thread %s in channel %s", threadTS, channelID)), nil
}

func (h *AgentRuntimeHandler) agentID(request mcp.CallToolRequest) string {
	agentID := request.GetString("agent_id", "")
	if agentID == "" {
		agentID = h.config.DefaultAgentID
	}
	if agentID == "" {
		agentID = "default"
	}
	return agentID
}

func (h *AgentRuntimeHandler) convertMessage(msg slack.Message, channelID string) Message {
	return Message{
		MsgID:    msg.Timestamp,
		UserID:   msg.User,
		UserName: msg.Username,
		Channel:  channelID,
		ThreadTs: msg.ThreadTimestamp,
		Text:     msg.Text,
		Time:     formatSlackTimestamp(msg.Timestamp),
	}
}

func formatSlackTimestamp(ts string) string {
	if ts == "" {
		return ""
	}
	// Slack timestamps are Unix seconds with fractional part.
	// We return the raw timestamp for simplicity.
	return ts
}
