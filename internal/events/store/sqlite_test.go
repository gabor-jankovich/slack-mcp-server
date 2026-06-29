package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/internal/db/migrations"
	"github.com/korotovsky/slack-mcp-server/internal/events/models"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) (*SQLiteStore, func()) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db, err := migrations.Open(context.Background(), path)
	require.NoError(t, err)

	s := NewSQLiteStore(db)
	cleanup := func() {
		db.Close()
		os.Remove(path)
	}
	return s, cleanup
}

func TestCreateAndLoadWorkItem(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusNew,
		RetryCount:      0,
		Version:         1,
		MessageCount:    1,
	}

	created, err := s.CreateWorkItem(ctx, item)
	require.NoError(t, err)
	require.Equal(t, item.ID, created.ID)

	loaded, err := s.LoadByID(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, item.ChannelID, loaded.ChannelID)
	require.Equal(t, item.ThreadTS, loaded.ThreadTS)
	require.Equal(t, models.StatusNew, loaded.Status)
}

func TestAcquireAndRenewLease(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusNew,
	}
	_, err := s.CreateWorkItem(ctx, item)
	require.NoError(t, err)

	agentID := "agent-1"
	leaseDuration := 2 * time.Minute

	acquired, err := s.AcquireLease(ctx, item.ID, agentID, leaseDuration)
	require.NoError(t, err)
	require.True(t, acquired)

	loaded, err := s.LoadByID(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusLeased, loaded.Status)
	require.NotNil(t, loaded.AgentID)
	require.Equal(t, agentID, *loaded.AgentID)
	require.NotNil(t, loaded.LeaseUntil)
	require.True(t, loaded.LeaseUntil.After(time.Now()))

	// Another agent cannot acquire.
	acquired, err = s.AcquireLease(ctx, item.ID, "agent-2", leaseDuration)
	require.NoError(t, err)
	require.False(t, acquired)

	// Same agent can renew.
	renewed, err := s.RenewLease(ctx, item.ID, agentID, leaseDuration)
	require.NoError(t, err)
	require.True(t, renewed)

	// Wrong agent cannot renew.
	renewed, err = s.RenewLease(ctx, item.ID, "agent-2", leaseDuration)
	require.NoError(t, err)
	require.False(t, renewed)
}

func TestAck(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusNew,
	}
	_, err := s.CreateWorkItem(ctx, item)
	require.NoError(t, err)

	agentID := "agent-1"
	_, err = s.AcquireLease(ctx, item.ID, agentID, 2*time.Minute)
	require.NoError(t, err)

	item, err = s.LoadByID(ctx, item.ID)
	require.NoError(t, err)
	item.Status = models.StatusProcessing
	item.UpdatedAt = time.Now().UTC()
	_, err = s.UpdateWorkItem(ctx, item)
	require.NoError(t, err)

	_, err = s.Ack(ctx, item.ID, agentID, "123.789")
	require.NoError(t, err)

	loaded, err := s.LoadByID(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusAcked, loaded.Status)
	require.NotNil(t, loaded.AckedAt)

	state, err := s.LoadThreadState(ctx, item.ChannelID, item.ThreadTS)
	require.NoError(t, err)
	require.Equal(t, "123.789", state.LastProcessedMessageTS)
}

func TestIncrementRetryAndReset(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusLeased,
	}
	_, err := s.CreateWorkItem(ctx, item)
	require.NoError(t, err)

	agentID := "agent-1"
	_, err = s.AcquireLease(ctx, item.ID, agentID, 2*time.Minute)
	require.NoError(t, err)

	updated, err := s.IncrementRetryAndReset(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusNew, updated.Status)
	require.Equal(t, 1, updated.RetryCount)
	require.Nil(t, updated.AgentID)
	require.Nil(t, updated.LeaseUntil)
}

func TestLoadExpiredLeases(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusLeased,
	}
	_, err := s.CreateWorkItem(ctx, item)
	require.NoError(t, err)

	// Manually set an expired lease using SQL to avoid waiting.
	_, err = s.db.ExecContext(ctx, `
		UPDATE conversation_work_items
		SET agent_id = ?, lease_until = datetime('now', '-1 minute'), updated_at = datetime('now')
		WHERE id = ?
	`, "agent-1", item.ID)
	require.NoError(t, err)

	expired, err := s.LoadExpiredLeases(ctx, 10)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	require.Equal(t, item.ID, expired[0].ID)
}

func TestFindPendingWorkItemByThread(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusNew,
	}
	_, err := s.CreateWorkItem(ctx, item)
	require.NoError(t, err)

	found, ok, err := s.FindPendingWorkItemByThread(ctx, "C123", "123.456")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, item.ID, found.ID)

	_, ok, err = s.FindPendingWorkItemByThread(ctx, "C999", "123.456")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestNextVersionForThread(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()

	v, err := s.NextVersionForThread(ctx, "C123", "123.456")
	require.NoError(t, err)
	require.Equal(t, 1, v)

	item := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusAcked,
		Version:         1,
	}
	_, err = s.CreateWorkItem(ctx, item)
	require.NoError(t, err)

	v, err = s.NextVersionForThread(ctx, "C123", "123.456")
	require.NoError(t, err)
	require.Equal(t, 2, v)
}

func TestLoadPendingExcludesAckedAndFailed(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()

	newItem := models.WorkItem{
		ID:              "123.456-v1",
		SourceID:        "slack",
		ChannelID:       "C123",
		ThreadTS:        "123.456",
		NewestMessageTS: "123.789",
		Status:          models.StatusNew,
	}
	_, err := s.CreateWorkItem(ctx, newItem)
	require.NoError(t, err)

	ackedItem := models.WorkItem{
		ID:              "999.000-v1",
		SourceID:        "slack",
		ChannelID:       "C999",
		ThreadTS:        "999.000",
		NewestMessageTS: "999.001",
		Status:          models.StatusAcked,
	}
	_, err = s.CreateWorkItem(ctx, ackedItem)
	require.NoError(t, err)

	pending, err := s.LoadPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, newItem.ID, pending[0].ID)
}

func TestRegisterAndLoadAgent(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	agent := models.Agent{
		AgentID:     "agent-1",
		Status:      "active",
		TmuxSession: "claude",
	}
	_, err := s.RegisterAgent(ctx, agent)
	require.NoError(t, err)

	loaded, err := s.LoadAgent(ctx, "agent-1")
	require.NoError(t, err)
	require.Equal(t, agent.AgentID, loaded.AgentID)
	require.Equal(t, agent.Status, loaded.Status)
	require.Equal(t, agent.TmuxSession, loaded.TmuxSession)
}

// Ensure SQLiteStore implements the extra manager/poller surfaces.
var _ interface {
	models.EventStore
	FindPendingWorkItemByThread(ctx context.Context, channelID, threadTS string) (models.WorkItem, bool, error)
	NextVersionForThread(ctx context.Context, channelID, threadTS string) (int, error)
	LoadExpiredLeases(ctx context.Context, limit int) ([]models.WorkItem, error)
	LoadAllThreadStates(ctx context.Context) ([]models.ThreadState, error)
} = (*SQLiteStore)(nil)

var _ = sql.ErrNoRows
