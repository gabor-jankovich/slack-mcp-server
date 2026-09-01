package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/korotovsky/slack-mcp-server/internal/events/models"
)

// SQLiteStore implements models.EventStore using SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates a store backed by the provided SQLite database.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

func (s *SQLiteStore) CreateWorkItem(ctx context.Context, item models.WorkItem) (models.WorkItem, error) {
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_work_items (
			id, source_id, channel_id, thread_ts, newest_message_ts, message_text, status,
			retry_count, version, message_count, agent_id, lease_until,
			created_at, updated_at, delivered_at, acked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		item.ID, item.SourceID, item.ChannelID, item.ThreadTS, item.NewestMessageTS, item.MessageText, item.Status,
		item.RetryCount, item.Version, item.MessageCount, nullStringPtr(item.AgentID), nullTimePtr(item.LeaseUntil),
		item.CreatedAt, item.UpdatedAt, nullTimePtr(item.DeliveredAt), nullTimePtr(item.AckedAt),
	)
	if err != nil {
		return models.WorkItem{}, fmt.Errorf("creating work item: %w", err)
	}
	return item, nil
}

func (s *SQLiteStore) UpdateWorkItem(ctx context.Context, item models.WorkItem) (models.WorkItem, error) {
	item.UpdatedAt = time.Now().UTC()

	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_work_items
		SET source_id = ?, channel_id = ?, thread_ts = ?, newest_message_ts = ?, message_text = ?, status = ?,
			retry_count = ?, version = ?, message_count = ?, agent_id = ?, lease_until = ?,
			updated_at = ?, delivered_at = ?, acked_at = ?
		WHERE id = ?
	`,
		item.SourceID, item.ChannelID, item.ThreadTS, item.NewestMessageTS, item.MessageText, item.Status,
		item.RetryCount, item.Version, item.MessageCount, nullStringPtr(item.AgentID), nullTimePtr(item.LeaseUntil),
		item.UpdatedAt, nullTimePtr(item.DeliveredAt), nullTimePtr(item.AckedAt),
		item.ID,
	)
	if err != nil {
		return models.WorkItem{}, fmt.Errorf("updating work item: %w", err)
	}
	return item, nil
}

func (s *SQLiteStore) LoadPending(ctx context.Context, limit int) ([]models.WorkItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_id, channel_id, thread_ts, newest_message_ts, message_text, status,
			retry_count, version, message_count, agent_id, lease_until,
			created_at, updated_at, delivered_at, acked_at
		FROM conversation_work_items
		WHERE status = ?
		ORDER BY created_at
		LIMIT ?
	`, models.StatusNew, limit)
	if err != nil {
		return nil, fmt.Errorf("loading pending work items: %w", err)
	}
	defer rows.Close()

	return scanWorkItems(rows)
}

func (s *SQLiteStore) LoadByID(ctx context.Context, id string) (models.WorkItem, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, source_id, channel_id, thread_ts, newest_message_ts, message_text, status,
			retry_count, version, message_count, agent_id, lease_until,
			created_at, updated_at, delivered_at, acked_at
		FROM conversation_work_items
		WHERE id = ?
	`, id)
	return scanWorkItem(row)
}

func (s *SQLiteStore) AcquireLease(ctx context.Context, itemID, agentID string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	leaseUntil := now.Add(ttl)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("beginning lease transaction: %w", err)
	}
	defer tx.Rollback()

	var status string
	var existingAgentID sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT status, agent_id FROM conversation_work_items WHERE id = ?
	`, itemID).Scan(&status, &existingAgentID); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("loading work item for lease: %w", err)
	}

	if status != string(models.StatusNew) {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_work_items
		SET status = ?, agent_id = ?, lease_until = ?, updated_at = ?, delivered_at = ?
		WHERE id = ?
	`, models.StatusLeased, agentID, leaseUntil, now, now, itemID); err != nil {
		return false, fmt.Errorf("updating work item lease: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO leases (work_item_id, agent_id, lease_until)
		VALUES (?, ?, ?)
		ON CONFLICT(work_item_id) DO UPDATE SET
			agent_id = excluded.agent_id,
			lease_until = excluded.lease_until
	`, itemID, agentID, leaseUntil); err != nil {
		return false, fmt.Errorf("upserting lease: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("committing lease transaction: %w", err)
	}
	return true, nil
}

func (s *SQLiteStore) RenewLease(ctx context.Context, itemID, agentID string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	leaseUntil := now.Add(ttl)

	var currentAgentID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT agent_id FROM conversation_work_items WHERE id = ?`, itemID).Scan(&currentAgentID); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("loading work item for renew: %w", err)
	}

	// Allow renew if agent_id in DB is NULL/empty (legacy) or matches.
	if currentAgentID.Valid && currentAgentID.String != "" && currentAgentID.String != agentID {
		return false, nil
	}

	if _, err := s.db.ExecContext(ctx, `
		UPDATE conversation_work_items
		SET lease_until = ?, agent_id = ?, updated_at = ?
		WHERE id = ?
	`, leaseUntil, agentID, now, itemID); err != nil {
		return false, fmt.Errorf("renewing work item lease: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO leases (work_item_id, agent_id, lease_until)
		VALUES (?, ?, ?)
		ON CONFLICT(work_item_id) DO UPDATE SET
			agent_id = excluded.agent_id,
			lease_until = excluded.lease_until
	`, itemID, agentID, leaseUntil); err != nil {
		return false, fmt.Errorf("renewing lease: %w", err)
	}
	return true, nil
}

func (s *SQLiteStore) ReleaseLease(ctx context.Context, itemID, agentID string) error {
	now := time.Now().UTC()

	if _, err := s.db.ExecContext(ctx, `
		UPDATE conversation_work_items
		SET status = ?, agent_id = NULL, lease_until = NULL, updated_at = ?
		WHERE id = ? AND agent_id = ?
	`, models.StatusNew, now, itemID, agentID); err != nil {
		return fmt.Errorf("releasing work item lease: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM leases WHERE work_item_id = ? AND agent_id = ?
	`, itemID, agentID); err != nil {
		return fmt.Errorf("deleting lease: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Ack(ctx context.Context, itemID, agentID string, newestMessageTS string) (models.WorkItem, error) {
	now := time.Now().UTC()

	var status string
	var currentAgentID sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT status, agent_id FROM conversation_work_items WHERE id = ?
	`, itemID).Scan(&status, &currentAgentID); err != nil {
		if err == sql.ErrNoRows {
			return models.WorkItem{}, fmt.Errorf("work item not found")
		}
		return models.WorkItem{}, fmt.Errorf("loading work item for ack: %w", err)
	}

	if status != string(models.StatusProcessing) && status != string(models.StatusLeased) {
		return models.WorkItem{}, fmt.Errorf("work item is not in PROCESSING or LEASED state")
	}
	// Allow ack if the stored agent_id is NULL/empty (legacy leases) or matches.
	if currentAgentID.Valid && currentAgentID.String != "" && currentAgentID.String != agentID {
		return models.WorkItem{}, fmt.Errorf("work item not leased by agent %s", agentID)
	}

	var item models.WorkItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.WorkItem{}, fmt.Errorf("beginning ack transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_work_items
		SET status = ?, lease_until = NULL, agent_id = NULL, updated_at = ?, acked_at = ?
		WHERE id = ?
	`, models.StatusAcked, now, now, itemID); err != nil {
		return models.WorkItem{}, fmt.Errorf("acking work item: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM leases WHERE work_item_id = ?
	`, itemID); err != nil {
		return models.WorkItem{}, fmt.Errorf("deleting lease on ack: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO thread_state (thread_ts, channel_id, last_processed_message_ts, updated_at)
		VALUES (
			(SELECT thread_ts FROM conversation_work_items WHERE id = ?),
			(SELECT channel_id FROM conversation_work_items WHERE id = ?),
			?,
			?
		)
		ON CONFLICT(thread_ts, channel_id) DO UPDATE SET
			last_processed_message_ts = excluded.last_processed_message_ts,
			updated_at = excluded.updated_at
	`, itemID, itemID, newestMessageTS, now); err != nil {
		return models.WorkItem{}, fmt.Errorf("updating thread state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return models.WorkItem{}, fmt.Errorf("committing ack transaction: %w", err)
	}

	item, err = s.LoadByID(ctx, itemID)
	if err != nil {
		return models.WorkItem{}, fmt.Errorf("loading acked work item: %w", err)
	}
	return item, nil
}

func (s *SQLiteStore) IncrementRetryAndReset(ctx context.Context, itemID string) (models.WorkItem, error) {
	now := time.Now().UTC()

	var item models.WorkItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.WorkItem{}, fmt.Errorf("beginning retry transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_work_items
		SET status = ?, retry_count = retry_count + 1, agent_id = NULL, lease_until = NULL, updated_at = ?
		WHERE id = ?
	`, models.StatusNew, now, itemID); err != nil {
		return models.WorkItem{}, fmt.Errorf("incrementing retry: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM leases WHERE work_item_id = ?
	`, itemID); err != nil {
		return models.WorkItem{}, fmt.Errorf("deleting lease on retry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return models.WorkItem{}, fmt.Errorf("committing retry transaction: %w", err)
	}

	item, err = s.LoadByID(ctx, itemID)
	if err != nil {
		return models.WorkItem{}, fmt.Errorf("loading retried work item: %w", err)
	}
	return item, nil
}

func (s *SQLiteStore) LoadAllThreadStates(ctx context.Context) ([]models.ThreadState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT thread_ts, channel_id, last_processed_message_ts, updated_at
		FROM thread_state
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("loading all thread states: %w", err)
	}
	defer rows.Close()

	var states []models.ThreadState
	for rows.Next() {
		var state models.ThreadState
		var lastProcessed sql.NullString
		if err := rows.Scan(&state.ThreadTS, &state.ChannelID, &lastProcessed, &state.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning thread state: %w", err)
		}
		if lastProcessed.Valid {
			state.LastProcessedMessageTS = lastProcessed.String
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating thread states: %w", err)
	}
	return states, nil
}

func (s *SQLiteStore) LoadThreadState(ctx context.Context, channelID, threadTS string) (models.ThreadState, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT thread_ts, channel_id, last_processed_message_ts, updated_at
		FROM thread_state
		WHERE thread_ts = ? AND channel_id = ?
	`, threadTS, channelID)

	var state models.ThreadState
	var lastProcessed sql.NullString
	if err := row.Scan(&state.ThreadTS, &state.ChannelID, &lastProcessed, &state.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return models.ThreadState{ThreadTS: threadTS, ChannelID: channelID}, nil
		}
		return models.ThreadState{}, fmt.Errorf("loading thread state: %w", err)
	}
	if lastProcessed.Valid {
		state.LastProcessedMessageTS = lastProcessed.String
	}
	return state, nil
}

func (s *SQLiteStore) SaveThreadState(ctx context.Context, state models.ThreadState) (models.ThreadState, error) {
	now := time.Now().UTC()
	state.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO thread_state (thread_ts, channel_id, last_processed_message_ts, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(thread_ts, channel_id) DO UPDATE SET
			last_processed_message_ts = excluded.last_processed_message_ts,
			updated_at = excluded.updated_at
	`, state.ThreadTS, state.ChannelID, nullString(state.LastProcessedMessageTS), now)
	if err != nil {
		return models.ThreadState{}, fmt.Errorf("saving thread state: %w", err)
	}
	return state, nil
}

func (s *SQLiteStore) LoadChannelState(ctx context.Context, channelID string) (models.ChannelState, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT channel_id, last_processed_message_ts, updated_at
		FROM channel_state
		WHERE channel_id = ?
	`, channelID)

	var state models.ChannelState
	var lastProcessed sql.NullString
	if err := row.Scan(&state.ChannelID, &lastProcessed, &state.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return models.ChannelState{}, sql.ErrNoRows
		}
		return models.ChannelState{}, fmt.Errorf("loading channel state: %w", err)
	}
	if lastProcessed.Valid {
		state.LastProcessedMessageTS = lastProcessed.String
	}
	return state, nil
}

func (s *SQLiteStore) SaveChannelState(ctx context.Context, state models.ChannelState) (models.ChannelState, error) {
	now := time.Now().UTC()
	state.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_state (channel_id, last_processed_message_ts, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(channel_id) DO UPDATE SET
			last_processed_message_ts = excluded.last_processed_message_ts,
			updated_at = excluded.updated_at
	`, state.ChannelID, nullString(state.LastProcessedMessageTS), now)
	if err != nil {
		return models.ChannelState{}, fmt.Errorf("saving channel state: %w", err)
	}
	return state, nil
}

func (s *SQLiteStore) RegisterAgent(ctx context.Context, agent models.Agent) (models.Agent, error) {
	now := time.Now().UTC()
	agent.Heartbeat = &now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_registry (agent_id, tmux_session, status, heartbeat, current_thread)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			tmux_session = excluded.tmux_session,
			status = excluded.status,
			heartbeat = excluded.heartbeat,
			current_thread = excluded.current_thread
	`, agent.AgentID, agent.TmuxSession, agent.Status, now, nullStringPtr(agent.CurrentThread))
	if err != nil {
		return models.Agent{}, fmt.Errorf("registering agent: %w", err)
	}
	return agent, nil
}

func (s *SQLiteStore) LoadAgent(ctx context.Context, agentID string) (models.Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT agent_id, tmux_session, status, heartbeat, current_thread
		FROM agent_registry
		WHERE agent_id = ?
	`, agentID)

	var agent models.Agent
	var heartbeat sql.NullTime
	var currentThread sql.NullString
	if err := row.Scan(&agent.AgentID, &agent.TmuxSession, &agent.Status, &heartbeat, &currentThread); err != nil {
		if err == sql.ErrNoRows {
			return models.Agent{}, fmt.Errorf("agent not found")
		}
		return models.Agent{}, fmt.Errorf("loading agent: %w", err)
	}
	if heartbeat.Valid {
		t := heartbeat.Time
		agent.Heartbeat = &t
	}
	if currentThread.Valid {
		agent.CurrentThread = &currentThread.String
	}
	return agent, nil
}

// FindPendingWorkItemByThread returns any non-terminal work item for a thread.
func (s *SQLiteStore) FindPendingWorkItemByThread(ctx context.Context, channelID, threadTS string) (models.WorkItem, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, source_id, channel_id, thread_ts, newest_message_ts, message_text, status,
			retry_count, version, message_count, agent_id, lease_until,
			created_at, updated_at, delivered_at, acked_at
		FROM conversation_work_items
		WHERE channel_id = ? AND thread_ts = ?
			AND status IN (?, ?, ?)
		ORDER BY created_at DESC
		LIMIT 1
	`, channelID, threadTS, models.StatusNew, models.StatusLeased, models.StatusProcessing)

	item, err := scanWorkItem(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return models.WorkItem{}, false, nil
		}
		return models.WorkItem{}, false, err
	}
	return item, true, nil
}

// NextVersionForThread returns the next version number for a thread.
func (s *SQLiteStore) NextVersionForThread(ctx context.Context, channelID, threadTS string) (int, error) {
	var maxVersion sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT MAX(version) FROM conversation_work_items
		WHERE channel_id = ? AND thread_ts = ?
	`, channelID, threadTS).Scan(&maxVersion); err != nil {
		return 0, fmt.Errorf("finding max version: %w", err)
	}
	if !maxVersion.Valid {
		return 1, nil
	}
	return int(maxVersion.Int64) + 1, nil
}

// LoadExpiredLeases returns work items whose lease has expired and are still LEASED.
func (s *SQLiteStore) LoadExpiredLeases(ctx context.Context, limit int) ([]models.WorkItem, error) {
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_id, channel_id, thread_ts, newest_message_ts, message_text, status,
			retry_count, version, message_count, agent_id, lease_until,
			created_at, updated_at, delivered_at, acked_at
		FROM conversation_work_items
		WHERE status = ? AND lease_until < ?
		ORDER BY lease_until
		LIMIT ?
	`, models.StatusLeased, now, limit)
	if err != nil {
		return nil, fmt.Errorf("loading expired leases: %w", err)
	}
	defer rows.Close()

	return scanWorkItems(rows)
}

func scanWorkItem(row *sql.Row) (models.WorkItem, error) {
	var item models.WorkItem
	var agentID sql.NullString
	var leaseUntil sql.NullTime
	var deliveredAt, ackedAt sql.NullTime

	if err := row.Scan(
		&item.ID, &item.SourceID, &item.ChannelID, &item.ThreadTS, &item.NewestMessageTS, &item.MessageText, &item.Status,
		&item.RetryCount, &item.Version, &item.MessageCount, &agentID, &leaseUntil,
		&item.CreatedAt, &item.UpdatedAt, &deliveredAt, &ackedAt,
	); err != nil {
		return models.WorkItem{}, err
	}

	if agentID.Valid {
		item.AgentID = &agentID.String
	}
	if leaseUntil.Valid {
		t := leaseUntil.Time
		item.LeaseUntil = &t
	}
	if deliveredAt.Valid {
		t := deliveredAt.Time
		item.DeliveredAt = &t
	}
	if ackedAt.Valid {
		t := ackedAt.Time
		item.AckedAt = &t
	}
	return item, nil
}

func scanWorkItems(rows *sql.Rows) ([]models.WorkItem, error) {
	var items []models.WorkItem
	for rows.Next() {
		var item models.WorkItem
		var agentID sql.NullString
		var leaseUntil, deliveredAt, ackedAt sql.NullTime

		if err := rows.Scan(
			&item.ID, &item.SourceID, &item.ChannelID, &item.ThreadTS, &item.NewestMessageTS, &item.MessageText, &item.Status,
			&item.RetryCount, &item.Version, &item.MessageCount, &agentID, &leaseUntil,
			&item.CreatedAt, &item.UpdatedAt, &deliveredAt, &ackedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning work item: %w", err)
		}

		if agentID.Valid {
			item.AgentID = &agentID.String
		}
		if leaseUntil.Valid {
			t := leaseUntil.Time
			item.LeaseUntil = &t
		}
		if deliveredAt.Valid {
			t := deliveredAt.Time
			item.DeliveredAt = &t
		}
		if ackedAt.Valid {
			t := ackedAt.Time
			item.AckedAt = &t
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating work items: %w", err)
	}
	return items, nil
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullStringPtr(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: *s, Valid: true}
}

func nullTimePtr(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{Valid: false}
	}
	return sql.NullTime{Time: *t, Valid: true}
}
