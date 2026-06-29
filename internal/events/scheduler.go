package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/korotovsky/slack-mcp-server/internal/events/models"
	"go.uber.org/zap"
)

// SchedulerStore is the store surface used by the scheduler.
type SchedulerStore interface {
	models.EventStore
	LoadExpiredLeases(ctx context.Context, limit int) ([]models.WorkItem, error)
}

// Scheduler picks pending work items and wakes agents.
// It also runs a retry worker that resets expired leases.
type Scheduler struct {
	config       models.RuntimeConfig
	store        SchedulerStore
	wakeProvider models.WakeProvider
	logger       *zap.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewScheduler creates a new scheduler.
func NewScheduler(config models.RuntimeConfig, store SchedulerStore, wakeProvider models.WakeProvider, logger *zap.Logger) *Scheduler {
	return &Scheduler{
		config:       config,
		store:        store,
		wakeProvider: wakeProvider,
		logger:       logger,
	}
}

// Start launches the dispatcher and retry worker goroutines.
func (s *Scheduler) Start(ctx context.Context) error {
	if s.cancel != nil {
		return fmt.Errorf("scheduler already started")
	}

	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	s.wg.Add(2)
	go s.dispatcherLoop(ctx)
	go s.retryLoop(ctx)

	return nil
}

// Stop shuts down the scheduler goroutines.
func (s *Scheduler) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	return nil
}

func (s *Scheduler) dispatcherLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.SchedulerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchOnce(ctx)
		}
	}
}

func (s *Scheduler) dispatchOnce(ctx context.Context) {
	items, err := s.store.LoadPending(ctx, 1)
	if err != nil {
		s.logger.Error("scheduler failed to load pending work items", zap.Error(err))
		return
	}
	if len(items) == 0 {
		return
	}

	item := items[0]
	agentID := s.config.DefaultAgentID

	acquired, err := s.store.AcquireLease(ctx, item.ID, agentID, s.config.LeaseDuration)
	if err != nil {
		s.logger.Error("scheduler failed to acquire lease",
			zap.String("work_item_id", item.ID),
			zap.Error(err))
		return
	}
	if !acquired {
		return
	}

	if _, err := s.store.RegisterAgent(ctx, models.Agent{
		AgentID:       agentID,
		Status:        "active",
		CurrentThread: &item.ThreadTS,
	}); err != nil {
		s.logger.Warn("scheduler failed to update agent current thread",
			zap.String("agent_id", agentID),
			zap.Error(err))
	}

	req := models.WakeRequest{
		AgentID:    agentID,
		WorkItemID: item.ID,
		Priority:   1,
		Reason:     fmt.Sprintf("new work item from %s", item.SourceID),
	}
	if err := s.wakeProvider.Wake(ctx, req); err != nil {
		s.logger.Error("wake provider failed",
			zap.String("work_item_id", item.ID),
			zap.String("agent_id", agentID),
			zap.Error(err))
		return
	}

	s.logger.Info("dispatched work item",
		zap.String("work_item_id", item.ID),
		zap.String("agent_id", agentID),
		zap.String("thread_ts", item.ThreadTS),
		zap.String("channel_id", item.ChannelID))
}

func (s *Scheduler) retryLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.RetryWorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.retryOnce(ctx)
		}
	}
}

func (s *Scheduler) retryOnce(ctx context.Context) {
	items, err := s.store.LoadExpiredLeases(ctx, 10)
	if err != nil {
		s.logger.Error("retry worker failed to load expired leases", zap.Error(err))
		return
	}

	for _, item := range items {
		if item.RetryCount >= s.config.MaxRetries {
			now := time.Now().UTC()
			item.Status = models.StatusFailed
			item.LeaseUntil = nil
			item.AgentID = nil
			item.UpdatedAt = now
			if _, err := s.store.UpdateWorkItem(ctx, item); err != nil {
				s.logger.Error("retry worker failed to mark work item failed",
					zap.String("work_item_id", item.ID),
					zap.Error(err))
			}
			s.logger.Warn("work item failed after max retries",
				zap.String("work_item_id", item.ID),
				zap.Int("retry_count", item.RetryCount))
			continue
		}

		updated, err := s.store.IncrementRetryAndReset(ctx, item.ID)
		if err != nil {
			s.logger.Error("retry worker failed to reset work item",
				zap.String("work_item_id", item.ID),
				zap.Error(err))
			continue
		}

		s.logger.Info("work item lease expired, reset to NEW",
			zap.String("work_item_id", item.ID),
			zap.Int("retry_count", updated.RetryCount))
	}
}
