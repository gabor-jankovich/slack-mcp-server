package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/korotovsky/slack-mcp-server/internal/db/migrations"
	"github.com/korotovsky/slack-mcp-server/internal/events"
	"github.com/korotovsky/slack-mcp-server/internal/events/models"
	"github.com/korotovsky/slack-mcp-server/internal/events/store"
	"github.com/korotovsky/slack-mcp-server/internal/wake"
	"go.uber.org/zap"
)

// AgentRuntime orchestrates the event sources, store, scheduler and wake providers.
type AgentRuntime struct {
	config       models.RuntimeConfig
	db           *sql.DB
	store        *store.SQLiteStore
	manager      *events.Manager
	scheduler    *events.Scheduler
	poller       *events.SlackEventSource
	wakeProvider models.WakeProvider
	logger       *zap.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates an AgentRuntime from the provided configuration.
func New(config models.RuntimeConfig, slackClient events.SlackClient, logger *zap.Logger) (*AgentRuntime, error) {
	if !config.Enabled {
		return &AgentRuntime{config: config, logger: logger}, nil
	}

	db, err := migrations.Open(context.Background(), config.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("opening agent runtime database: %w", err)
	}

	s := store.NewSQLiteStore(db)
	m := events.NewManager(s)

	wakeProvider := wake.NewTmuxProvider(os.Getenv("SLACK_MCP_TMUX_SESSION"), logger)

	sched := events.NewScheduler(config, s, wakeProvider, logger)

	var poller *events.SlackEventSource
	if slackClient != nil {
		poller = events.NewSlackEventSource(config, slackClient, s, logger)
	}

	return &AgentRuntime{
		config:       config,
		db:           db,
		store:        s,
		manager:      m,
		scheduler:    sched,
		poller:       poller,
		wakeProvider: wakeProvider,
		logger:       logger,
	}, nil
}

// Start launches the event source consumer, scheduler, and poller.
func (r *AgentRuntime) Start(ctx context.Context) error {
	if !r.config.Enabled {
		return nil
	}

	if r.cancel != nil {
		return fmt.Errorf("agent runtime already started")
	}

	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	if r.poller != nil {
		if err := r.poller.Start(ctx); err != nil {
			return fmt.Errorf("starting poller: %w", err)
		}
		r.wg.Add(1)
		go r.consumeCandidates(ctx)
	}

	if err := r.scheduler.Start(ctx); err != nil {
		return fmt.Errorf("starting scheduler: %w", err)
	}

	r.logger.Info("agent runtime started",
		zap.String("sqlite_path", r.config.SQLitePath),
		zap.String("polling_mode", r.config.PollingMode))

	return nil
}

// Stop shuts down all goroutines.
func (r *AgentRuntime) Stop() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()

	if r.scheduler != nil {
		if err := r.scheduler.Stop(); err != nil {
			r.logger.Warn("error stopping scheduler", zap.Error(err))
		}
	}
	if r.poller != nil {
		if err := r.poller.Stop(); err != nil {
			r.logger.Warn("error stopping poller", zap.Error(err))
		}
	}
	if r.db != nil {
		if err := r.db.Close(); err != nil {
			r.logger.Warn("error closing database", zap.Error(err))
		}
	}
	return nil
}

// Store returns the runtime store for use by MCP tool handlers.
func (r *AgentRuntime) Store() models.EventStore {
	return r.store
}

// Config returns the runtime configuration.
func (r *AgentRuntime) Config() models.RuntimeConfig {
	return r.config
}

func (r *AgentRuntime) consumeCandidates(ctx context.Context) {
	defer r.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case candidate, ok := <-r.poller.Events():
			if !ok {
				return
			}
			if _, err := r.manager.CreateOrUpdateWorkItem(ctx, candidate); err != nil {
				r.logger.Error("failed to create or update work item",
					zap.String("channel_id", candidate.ChannelID),
					zap.String("thread_ts", candidate.ThreadTS),
					zap.Error(err))
				continue
			}
			r.logger.Debug("created or updated work item",
				zap.String("channel_id", candidate.ChannelID),
				zap.String("thread_ts", candidate.ThreadTS))
		}
	}
}

// ConfigFromEnv builds a RuntimeConfig from environment variables.
func ConfigFromEnv() models.RuntimeConfig {
	cfg := models.DefaultRuntimeConfig()

	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_ENABLED"); v != "" {
		cfg.Enabled = parseBool(v, cfg.Enabled)
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_DB"); v != "" {
		cfg.SQLitePath = v
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgentID = v
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_POLLING_MODE"); v != "" {
		cfg.PollingMode = v
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_POLLING_CHANNELS"); v != "" {
		cfg.PollingChannels = strings.Split(v, ",")
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_LEASE_DURATION"); v != "" {
		cfg.LeaseDuration = parseDuration(v, cfg.LeaseDuration)
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_HEARTBEAT_INTERVAL"); v != "" {
		cfg.HeartbeatInterval = parseDuration(v, cfg.HeartbeatInterval)
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_MAX_RETRIES"); v != "" {
		cfg.MaxRetries = parseInt(v, cfg.MaxRetries)
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_IDLE_INTERVAL"); v != "" {
		cfg.IdleInterval = parseDuration(v, cfg.IdleInterval)
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_HOT_INTERVAL"); v != "" {
		cfg.HotInterval = parseDuration(v, cfg.HotInterval)
	}
	if v := os.Getenv("SLACK_MCP_AGENT_RUNTIME_BOT_USER_ID"); v != "" {
		cfg.BotUserID = v
	}

	return cfg
}

func parseBool(s string, fallback bool) bool {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fallback
	}
	return v
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

func parseInt(s string, fallback int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}
