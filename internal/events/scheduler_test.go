package events

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/internal/events/models"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type mockSchedulerStore struct {
	items         map[string]models.WorkItem
	agents        map[string]models.Agent
	threadStates  map[string]models.ThreadState
	loadPending   []models.WorkItem
	loadExpired   []models.WorkItem
	acquireResult bool
	wakeCalls     []models.WakeRequest
}

func newMockSchedulerStore() *mockSchedulerStore {
	return &mockSchedulerStore{
		items:        make(map[string]models.WorkItem),
		agents:       make(map[string]models.Agent),
		threadStates: make(map[string]models.ThreadState),
	}
}

func (m *mockSchedulerStore) CreateWorkItem(ctx context.Context, item models.WorkItem) (models.WorkItem, error) {
	m.items[item.ID] = item
	return item, nil
}

func (m *mockSchedulerStore) UpdateWorkItem(ctx context.Context, item models.WorkItem) (models.WorkItem, error) {
	m.items[item.ID] = item
	return item, nil
}

func (m *mockSchedulerStore) LoadPending(ctx context.Context, limit int) ([]models.WorkItem, error) {
	return m.loadPending, nil
}

func (m *mockSchedulerStore) LoadByID(ctx context.Context, id string) (models.WorkItem, error) {
	item, ok := m.items[id]
	if !ok {
		return models.WorkItem{}, sql.ErrNoRows
	}
	return item, nil
}

func (m *mockSchedulerStore) AcquireLease(ctx context.Context, itemID, agentID string, duration time.Duration) (bool, error) {
	item, ok := m.items[itemID]
	if !ok {
		return false, errors.New("not found")
	}
	if item.Status != models.StatusNew {
		return false, nil
	}
	until := time.Now().UTC().Add(duration)
	item.Status = models.StatusLeased
	item.AgentID = &agentID
	item.LeaseUntil = &until
	item.UpdatedAt = time.Now().UTC()
	m.items[itemID] = item
	return true, nil
}

func (m *mockSchedulerStore) RenewLease(ctx context.Context, itemID, agentID string, duration time.Duration) (bool, error) {
	item, ok := m.items[itemID]
	if !ok || item.AgentID == nil || *item.AgentID != agentID {
		return false, nil
	}
	until := time.Now().UTC().Add(duration)
	item.LeaseUntil = &until
	item.UpdatedAt = time.Now().UTC()
	m.items[itemID] = item
	return true, nil
}

func (m *mockSchedulerStore) ReleaseLease(ctx context.Context, itemID, agentID string) error {
	item, ok := m.items[itemID]
	if !ok {
		return errors.New("not found")
	}
	item.Status = models.StatusNew
	item.AgentID = nil
	item.LeaseUntil = nil
	item.UpdatedAt = time.Now().UTC()
	m.items[itemID] = item
	return nil
}

func (m *mockSchedulerStore) Ack(ctx context.Context, itemID, agentID, newestMessageTS string) (models.WorkItem, error) {
	item, ok := m.items[itemID]
	if !ok {
		return models.WorkItem{}, errors.New("not found")
	}
	now := time.Now().UTC()
	item.Status = models.StatusAcked
	item.AckedAt = &now
	item.UpdatedAt = now
	m.items[itemID] = item

	state := m.threadStates[item.ThreadTS]
	state.ThreadTS = item.ThreadTS
	state.ChannelID = item.ChannelID
	state.LastProcessedMessageTS = newestMessageTS
	state.UpdatedAt = now
	m.threadStates[item.ThreadTS] = state

	return item, nil
}

func (m *mockSchedulerStore) IncrementRetryAndReset(ctx context.Context, itemID string) (models.WorkItem, error) {
	item, ok := m.items[itemID]
	if !ok {
		return models.WorkItem{}, errors.New("not found")
	}
	item.RetryCount++
	item.Status = models.StatusNew
	item.AgentID = nil
	item.LeaseUntil = nil
	item.UpdatedAt = time.Now().UTC()
	m.items[itemID] = item
	return item, nil
}

func (m *mockSchedulerStore) LoadThreadState(ctx context.Context, channelID, threadTS string) (models.ThreadState, error) {
	state, ok := m.threadStates[threadTS]
	if !ok {
		return models.ThreadState{}, sql.ErrNoRows
	}
	return state, nil
}

func (m *mockSchedulerStore) SaveThreadState(ctx context.Context, state models.ThreadState) (models.ThreadState, error) {
	m.threadStates[state.ThreadTS] = state
	return state, nil
}

func (m *mockSchedulerStore) RegisterAgent(ctx context.Context, agent models.Agent) (models.Agent, error) {
	m.agents[agent.AgentID] = agent
	return agent, nil
}

func (m *mockSchedulerStore) LoadAgent(ctx context.Context, agentID string) (models.Agent, error) {
	agent, ok := m.agents[agentID]
	if !ok {
		return models.Agent{}, sql.ErrNoRows
	}
	return agent, nil
}

func (m *mockSchedulerStore) FindPendingWorkItemByThread(ctx context.Context, channelID, threadTS string) (models.WorkItem, bool, error) {
	for _, item := range m.items {
		if item.ChannelID == channelID && item.ThreadTS == threadTS && item.Status != models.StatusAcked && item.Status != models.StatusFailed {
			return item, true, nil
		}
	}
	return models.WorkItem{}, false, nil
}

func (m *mockSchedulerStore) NextVersionForThread(ctx context.Context, channelID, threadTS string) (int, error) {
	max := 0
	for _, item := range m.items {
		if item.ChannelID == channelID && item.ThreadTS == threadTS && item.Version > max {
			max = item.Version
		}
	}
	return max + 1, nil
}

func (m *mockSchedulerStore) LoadExpiredLeases(ctx context.Context, limit int) ([]models.WorkItem, error) {
	return m.loadExpired, nil
}

func (m *mockSchedulerStore) LoadAllThreadStates(ctx context.Context) ([]models.ThreadState, error) {
	var states []models.ThreadState
	for _, state := range m.threadStates {
		states = append(states, state)
	}
	return states, nil
}

type mockWakeProvider struct {
	requests []models.WakeRequest
	err      error
}

func (m *mockWakeProvider) Wake(ctx context.Context, req models.WakeRequest) error {
	m.requests = append(m.requests, req)
	return m.err
}

func TestSchedulerDispatchOnce(t *testing.T) {
	store := newMockSchedulerStore()
	wake := &mockWakeProvider{}
	logger := zap.NewNop()

	cfg := models.DefaultRuntimeConfig()
	cfg.DefaultAgentID = "default-agent"

	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusNew,
	}
	_, err := store.CreateWorkItem(context.Background(), item)
	require.NoError(t, err)
	store.loadPending = []models.WorkItem{item}

	sched := NewScheduler(cfg, store, wake, logger)
	sched.dispatchOnce(context.Background())

	require.Len(t, wake.requests, 1)
	require.Equal(t, item.ID, wake.requests[0].WorkItemID)
	require.Equal(t, cfg.DefaultAgentID, wake.requests[0].AgentID)

	loaded, err := store.LoadByID(context.Background(), item.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusLeased, loaded.Status)
	require.NotNil(t, loaded.AgentID)
	require.Equal(t, cfg.DefaultAgentID, *loaded.AgentID)
}

func TestSchedulerRetryOnce(t *testing.T) {
	store := newMockSchedulerStore()
	wake := &mockWakeProvider{}
	logger := zap.NewNop()

	cfg := models.DefaultRuntimeConfig()
	cfg.MaxRetries = 2

	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusLeased,
		RetryCount:      1,
	}
	_, err := store.CreateWorkItem(context.Background(), item)
	require.NoError(t, err)
	store.loadExpired = []models.WorkItem{item}

	sched := NewScheduler(cfg, store, wake, logger)
	sched.retryOnce(context.Background())

	loaded, err := store.LoadByID(context.Background(), item.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusNew, loaded.Status)
	require.Equal(t, 2, loaded.RetryCount)
	require.Nil(t, loaded.AgentID)
	require.Nil(t, loaded.LeaseUntil)
}

func TestSchedulerRetryOnceMaxRetries(t *testing.T) {
	store := newMockSchedulerStore()
	wake := &mockWakeProvider{}
	logger := zap.NewNop()

	cfg := models.DefaultRuntimeConfig()
	cfg.MaxRetries = 2

	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusLeased,
		RetryCount:      2,
	}
	_, err := store.CreateWorkItem(context.Background(), item)
	require.NoError(t, err)
	store.loadExpired = []models.WorkItem{item}

	sched := NewScheduler(cfg, store, wake, logger)
	sched.retryOnce(context.Background())

	loaded, err := store.LoadByID(context.Background(), item.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusFailed, loaded.Status)
}
