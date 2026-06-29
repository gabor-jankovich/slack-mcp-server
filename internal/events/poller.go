package events

import (
	"context"
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
}

// PollerStore is the store surface needed by the poller.
type PollerStore interface {
	LoadAllThreadStates(ctx context.Context) ([]models.ThreadState, error)
	LoadThreadState(ctx context.Context, channelID, threadTS string) (models.ThreadState, error)
	SaveThreadState(ctx context.Context, state models.ThreadState) (models.ThreadState, error)
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
		if shouldIgnoreMessage(msg) {
			continue
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

func shouldIgnoreMessage(msg slack.Message) bool {
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
	return false
}
