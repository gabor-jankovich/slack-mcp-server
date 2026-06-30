package events

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/korotovsky/slack-mcp-server/internal/events/models"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// SlackClient is the minimal Slack API surface needed by the poller.
type SlackClient interface {
	GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) (msgs []slack.Message, hasMore bool, nextCursor string, err error)
	GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
}

// PollerStore is the store surface needed by the poller.
type PollerStore interface {
	LoadAllThreadStates(ctx context.Context) ([]models.ThreadState, error)
	LoadThreadState(ctx context.Context, channelID, threadTS string) (models.ThreadState, error)
	SaveThreadState(ctx context.Context, state models.ThreadState) (models.ThreadState, error)
	LoadChannelState(ctx context.Context, channelID string) (models.ChannelState, error)
	SaveChannelState(ctx context.Context, state models.ChannelState) (models.ChannelState, error)
}

// PollerState is the polling interval state machine.
type PollerState string

const (
	PollerStateIdle     PollerState = "IDLE"
	PollerStateActivity PollerState = "ACTIVITY"
	PollerStateHot      PollerState = "HOT"
	PollerStateCooldown PollerState = "COOLDOWN"
)

// SlackEventSource polls Slack for new thread activity.
type SlackEventSource struct {
	config models.RuntimeConfig
	client SlackClient
	store  PollerStore
	logger *zap.Logger

	candidates chan models.EventCandidate

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu            sync.Mutex
	state         PollerState
	lastActivity  time.Time
	stateDeadline time.Time
}

// NewSlackEventSource creates a Slack polling event source.
func NewSlackEventSource(config models.RuntimeConfig, client SlackClient, store PollerStore, logger *zap.Logger) *SlackEventSource {
	return &SlackEventSource{
		config:     config,
		client:     client,
		store:      store,
		logger:     logger,
		candidates: make(chan models.EventCandidate, 16),
		state:      PollerStateIdle,
	}
}

// Start launches the polling goroutine.
func (p *SlackEventSource) Start(ctx context.Context) error {
	if p.cancel != nil {
		return fmt.Errorf("slack event source already started")
	}

	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	p.wg.Add(1)
	go p.loop(ctx)

	return nil
}

// Stop shuts down the polling goroutine.
func (p *SlackEventSource) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	close(p.candidates)
	return nil
}

// Events returns the channel of detected event candidates.
func (p *SlackEventSource) Events() <-chan models.EventCandidate {
	return p.candidates
}

func (p *SlackEventSource) loop(ctx context.Context) {
	defer p.wg.Done()

	for {
		interval := p.currentInterval()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			activity, err := p.poll(ctx)
			if err != nil {
				p.logger.Error("slack poll failed", zap.Error(err))
				continue
			}
			p.updateState(activity)
		}
	}
}

func (p *SlackEventSource) poll(ctx context.Context) (bool, error) {
	if p.config.PollingMode == "watch_channels" {
		return p.pollChannels(ctx)
	}

	states, err := p.store.LoadAllThreadStates(ctx)
	if err != nil {
		return false, fmt.Errorf("loading thread states: %w", err)
	}

	if len(states) == 0 {
		return false, nil
	}

	activity := false
	for _, state := range states {
		found, err := p.pollThread(ctx, state)
		if err != nil {
			p.logger.Error("polling thread failed",
				zap.String("channel_id", state.ChannelID),
				zap.String("thread_ts", state.ThreadTS),
				zap.Error(err))
			continue
		}
		if found {
			activity = true
		}
	}

	return activity, nil
}

func (p *SlackEventSource) pollThread(ctx context.Context, state models.ThreadState) (bool, error) {
	params := &slack.GetConversationRepliesParameters{
		ChannelID: state.ChannelID,
		Timestamp: state.ThreadTS,
		Limit:     100,
	}

	if state.LastProcessedMessageTS != "" {
		params.Oldest = state.LastProcessedMessageTS
	}

	msgs, _, _, err := p.client.GetConversationRepliesContext(ctx, params)
	if err != nil {
		return false, fmt.Errorf("fetching replies: %w", err)
	}

	var newestTS string
	var firstMsgText string
	found := false
	for _, msg := range msgs {
		// Skip the parent message itself if oldest is empty.
		if state.LastProcessedMessageTS == "" && msg.Timestamp == state.ThreadTS {
			continue
		}
		// Skip messages at or before the cursor.
		if state.LastProcessedMessageTS != "" && msg.Timestamp <= state.LastProcessedMessageTS {
			continue
		}
		// Skip bot noise, activity messages, and reactions.
		if shouldIgnoreMessage(msg, p.config.BotUserID) {
			continue
		}
		if !found {
			firstMsgText = msg.Text
		}
		found = true
		if msg.Timestamp > newestTS {
			newestTS = msg.Timestamp
		}
	}

	if !found || newestTS == "" {
		return false, nil
	}

	candidate := models.EventCandidate{
		SourceID:        "slack",
		ChannelID:       state.ChannelID,
		ThreadTS:        state.ThreadTS,
		NewestMessageTS: newestTS,
		MessageText:     firstMsgText,
	}

	select {
	case p.candidates <- candidate:
	case <-ctx.Done():
		return false, ctx.Err()
	}

	p.logger.Debug("emitted slack event candidate",
		zap.String("channel_id", state.ChannelID),
		zap.String("thread_ts", state.ThreadTS),
		zap.String("newest_message_ts", newestTS))

	return true, nil
}

func (p *SlackEventSource) pollChannels(ctx context.Context) (bool, error) {
	if len(p.config.PollingChannels) == 0 {
		p.logger.Warn("watch_channels mode enabled but no channels configured")
		return false, nil
	}

	activity := false
	for _, channelID := range p.config.PollingChannels {
		found, err := p.pollChannel(ctx, channelID)
		if err != nil {
			p.logger.Error("polling channel failed",
				zap.String("channel_id", channelID),
				zap.Error(err))
			continue
		}
		if found {
			activity = true
		}
	}

	return activity, nil
}

func (p *SlackEventSource) pollChannel(ctx context.Context, channelID string) (bool, error) {
	state, err := p.store.LoadChannelState(ctx, channelID)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("loading channel state: %w", err)
	}

	params := &slack.GetConversationHistoryParameters{
		ChannelID: channelID,
		Limit:     100,
	}
	if state.LastProcessedMessageTS != "" {
		params.Oldest = state.LastProcessedMessageTS
	}

	resp, err := p.client.GetConversationHistoryContext(ctx, params)
	if err != nil {
		return false, fmt.Errorf("fetching channel history: %w", err)
	}

	var newestTS string
	type threadInfo struct {
		newestMsgTS string
		firstText   string
	}
	threadCandidates := make(map[string]*threadInfo) // thread_ts -> info
	for _, msg := range resp.Messages {
		if state.LastProcessedMessageTS != "" && msg.Timestamp <= state.LastProcessedMessageTS {
			continue
		}
		if shouldIgnoreMessage(msg, p.config.BotUserID) {
			continue
		}

		threadTS := msg.Timestamp
		if msg.ThreadTimestamp != "" {
			threadTS = msg.ThreadTimestamp
		}

		if msg.Timestamp > newestTS {
			newestTS = msg.Timestamp
		}
		if info, ok := threadCandidates[threadTS]; !ok {
			threadCandidates[threadTS] = &threadInfo{newestMsgTS: msg.Timestamp, firstText: msg.Text}
		} else if msg.Timestamp > info.newestMsgTS {
			info.newestMsgTS = msg.Timestamp
		}
	}

	if len(threadCandidates) == 0 {
		return false, nil
	}

	now := time.Now()
	for threadTS, info := range threadCandidates {
		threadState := models.ThreadState{
			ChannelID:              channelID,
			ThreadTS:               threadTS,
			LastProcessedMessageTS: "",
			UpdatedAt:              now,
		}
		if _, err := p.store.SaveThreadState(ctx, threadState); err != nil {
			p.logger.Error("saving thread state from channel poll failed",
				zap.String("channel_id", channelID),
				zap.String("thread_ts", threadTS),
				zap.Error(err))
			continue
		}

		candidate := models.EventCandidate{
			SourceID:        "slack",
			ChannelID:       channelID,
			ThreadTS:        threadTS,
			NewestMessageTS: info.newestMsgTS,
			MessageText:     info.firstText,
		}
		select {
		case p.candidates <- candidate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	if newestTS != "" {
		state.ChannelID = channelID
		state.LastProcessedMessageTS = newestTS
		state.UpdatedAt = now
		if _, err := p.store.SaveChannelState(ctx, state); err != nil {
			p.logger.Error("saving channel state failed",
				zap.String("channel_id", channelID),
				zap.Error(err))
		}
	}

	p.logger.Debug("emitted channel candidates",
		zap.String("channel_id", channelID),
		zap.Int("candidate_count", len(threadCandidates)),
		zap.String("newest_message_ts", newestTS))

	return true, nil
}

func (p *SlackEventSource) updateState(activity bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()

	if activity {
		p.lastActivity = now
		switch p.state {
		case PollerStateIdle:
			p.state = PollerStateActivity
			p.stateDeadline = now.Add(p.config.HotDuration)
		case PollerStateActivity:
			p.state = PollerStateHot
			p.stateDeadline = now.Add(p.config.HotDuration)
		case PollerStateCooldown:
			p.state = PollerStateActivity
			p.stateDeadline = now.Add(p.config.HotDuration)
		case PollerStateHot:
			p.stateDeadline = now.Add(p.config.HotDuration)
		}
		return
	}

	if now.Before(p.stateDeadline) {
		return
	}

	switch p.state {
	case PollerStateActivity:
		p.state = PollerStateIdle
	case PollerStateHot:
		p.state = PollerStateCooldown
		p.stateDeadline = now.Add(p.config.CooldownDuration)
	case PollerStateCooldown:
		p.state = PollerStateIdle
	}
}

func (p *SlackEventSource) currentInterval() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch p.state {
	case PollerStateHot:
		return p.config.HotInterval
	case PollerStateActivity, PollerStateCooldown:
		return p.config.CooldownInterval
	default:
		return p.config.IdleInterval
	}
}

func shouldIgnoreMessage(msg slack.Message, botUserID string) bool {
	// Ignore reactions and typing events.
	if msg.SubType == "reaction" || msg.SubType == "typing" {
		return true
	}
	// Ignore channel joins/leaves.
	if msg.SubType == "channel_join" || msg.SubType == "channel_leave" {
		return true
	}
	// Ignore message edits.
	if msg.SubType == "message_changed" {
		return true
	}
	// Ignore bot messages (subtypes or explicit bot_id field).
	if msg.SubType == "bot_message" || msg.BotID != "" {
		return true
	}
	// Ignore messages sent by the bot's own user ID.
	if botUserID != "" && msg.User == botUserID {
		return true
	}
	return false
}
