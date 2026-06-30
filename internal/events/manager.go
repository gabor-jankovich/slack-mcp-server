package events

import (
	"context"
	"fmt"
	"time"

	"github.com/korotovsky/slack-mcp-server/internal/events/models"
)

// ManagerStore is the store surface used by the event manager.
type ManagerStore interface {
	models.EventStore
	FindPendingWorkItemByThread(ctx context.Context, channelID, threadTS string) (models.WorkItem, bool, error)
	NextVersionForThread(ctx context.Context, channelID, threadTS string) (int, error)
}

// Manager coordinates event sources and the persistent work queue.
type Manager struct {
	store ManagerStore
}

// NewManager creates a new event manager.
func NewManager(store ManagerStore) *Manager {
	return &Manager{store: store}
}

// CreateOrUpdateWorkItem ensures that a thread has exactly one pending work item.
// If a pending work item already exists, it updates newest_message_ts and message_count.
// Otherwise it creates a new work item with the next version number.
func (m *Manager) CreateOrUpdateWorkItem(ctx context.Context, candidate models.EventCandidate) (models.WorkItem, error) {
	now := time.Now().UTC()

	existing, found, err := m.store.FindPendingWorkItemByThread(ctx, candidate.ChannelID, candidate.ThreadTS)
	if err != nil {
		return models.WorkItem{}, fmt.Errorf("finding pending work item: %w", err)
	}

	if found {
		existing.NewestMessageTS = candidate.NewestMessageTS
		existing.MessageCount++
		existing.UpdatedAt = now
		return m.store.UpdateWorkItem(ctx, existing)
	}

	version, err := m.store.NextVersionForThread(ctx, candidate.ChannelID, candidate.ThreadTS)
	if err != nil {
		return models.WorkItem{}, fmt.Errorf("getting next version: %w", err)
	}

	item := models.WorkItem{
		ID:              fmt.Sprintf("%s-v%d", candidate.ThreadTS, version),
		SourceID:        candidate.SourceID,
		ChannelID:       candidate.ChannelID,
		ThreadTS:        candidate.ThreadTS,
		NewestMessageTS: candidate.NewestMessageTS,
		MessageText:     candidate.MessageText,
		Status:          models.StatusNew,
		RetryCount:      0,
		Version:         version,
		MessageCount:    1,
	}
	return m.store.CreateWorkItem(ctx, item)
}
