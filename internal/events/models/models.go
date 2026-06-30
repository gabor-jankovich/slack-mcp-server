package models

import (
	"context"
	"time"
)

// WorkItemStatus represents the lifecycle state of a conversation work item.
type WorkItemStatus string

const (
	StatusNew        WorkItemStatus = "NEW"
	StatusLeased     WorkItemStatus = "LEASED"
	StatusProcessing WorkItemStatus = "PROCESSING"
	StatusAcked      WorkItemStatus = "ACKED"
	StatusArchived   WorkItemStatus = "ARCHIVED"
	StatusFailed     WorkItemStatus = "FAILED"
)

// WorkItem represents a conversation that has unprocessed changes for an agent.
type WorkItem struct {
	ID              string
	SourceID        string
	ChannelID       string
	ThreadTS        string
	NewestMessageTS string
	MessageText     string
	Status          WorkItemStatus
	RetryCount      int
	Version         int
	MessageCount    int
	AgentID         *string
	LeaseUntil      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeliveredAt     *time.Time
	AckedAt         *time.Time
}

// EventCandidate is a raw change detected by an EventSource.
type EventCandidate struct {
	SourceID        string
	ChannelID       string
	ThreadTS        string
	NewestMessageTS string
	MessageText     string
	Payload         []byte
}

// EventSource is a generic source of change candidates.
type EventSource interface {
	Start(ctx context.Context) error
	Stop() error
	Events() <-chan EventCandidate
}

// EventManager coordinates EventSource, Store and Queue logic.
type EventManager interface {
	CreateOrUpdateWorkItem(ctx context.Context, candidate EventCandidate) (WorkItem, error)
}

// EventStore persists work items, thread state and leases.
type EventStore interface {
	CreateWorkItem(ctx context.Context, item WorkItem) (WorkItem, error)
	UpdateWorkItem(ctx context.Context, item WorkItem) (WorkItem, error)
	LoadPending(ctx context.Context, limit int) ([]WorkItem, error)
	LoadByID(ctx context.Context, id string) (WorkItem, error)
	AcquireLease(ctx context.Context, itemID, agentID string, ttl time.Duration) (bool, error)
	RenewLease(ctx context.Context, itemID, agentID string, ttl time.Duration) (bool, error)
	ReleaseLease(ctx context.Context, itemID, agentID string) error
	Ack(ctx context.Context, itemID, agentID string, newestMessageTS string) (WorkItem, error)
	IncrementRetryAndReset(ctx context.Context, itemID string) (WorkItem, error)

	LoadThreadState(ctx context.Context, channelID, threadTS string) (ThreadState, error)
	SaveThreadState(ctx context.Context, state ThreadState) (ThreadState, error)

	RegisterAgent(ctx context.Context, agent Agent) (Agent, error)
	LoadAgent(ctx context.Context, agentID string) (Agent, error)
}

// ThreadState tracks the last processed message timestamp per thread.
type ThreadState struct {
	ThreadTS               string
	ChannelID              string
	LastProcessedMessageTS string
	UpdatedAt              time.Time
}

// ChannelState tracks the last processed message timestamp per channel.
type ChannelState struct {
	ChannelID              string
	LastProcessedMessageTS string
	UpdatedAt              time.Time
}

// Agent represents a registered agent session.
type Agent struct {
	AgentID       string
	TmuxSession   string
	Status        string
	Heartbeat     *time.Time
	CurrentThread *string
}

// WakeRequest is sent to a WakeProvider to notify an agent.
type WakeRequest struct {
	AgentID     string
	TmuxSession string
	WorkItemID  string
	MessageText string
	Priority    int
	Reason      string
}

// WakeProvider injects wake metadata into an agent session.
type WakeProvider interface {
	Wake(ctx context.Context, req WakeRequest) error
}

// Scheduler picks pending work items and wakes agents.
type Scheduler interface {
	Start(ctx context.Context) error
	Stop() error
}

// RuntimeConfig configures the agent runtime.
type RuntimeConfig struct {
	Enabled             bool
	SQLitePath          string
	LeaseDuration       time.Duration
	HeartbeatInterval   time.Duration
	MaxRetries          int
	SchedulerInterval   time.Duration
	RetryWorkerInterval time.Duration
	PollingMode         string
	PollingChannels     []string
	IdleInterval        time.Duration
	ActivityInterval    time.Duration
	HotInterval         time.Duration
	HotDuration         time.Duration
	CooldownInterval    time.Duration
	CooldownDuration    time.Duration
	DefaultAgentID      string
	BotUserID           string
}

// DefaultRuntimeConfig returns sensible defaults.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Enabled:             false,
		SQLitePath:          "./data/agent_runtime.db",
		LeaseDuration:       2 * time.Minute,
		HeartbeatInterval:   30 * time.Second,
		MaxRetries:          10,
		SchedulerInterval:   2 * time.Second,
		RetryWorkerInterval: 10 * time.Second,
		PollingMode:         "watch_threads",
		IdleInterval:        30 * time.Second,
		ActivityInterval:    10 * time.Second,
		HotInterval:         3 * time.Second,
		HotDuration:         30 * time.Second,
		CooldownInterval:    10 * time.Second,
		CooldownDuration:    30 * time.Second,
	}
}
