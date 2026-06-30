# Agent Runtime Extension

This fork extends `slack-mcp-server` with an optional **Agent Runtime** subsystem that turns the server into a stateful, event-driven coordinator for long-running AI agents.

## What is the Agent Runtime?

Instead of having the AI agent poll Slack, the MCP server polls on behalf of the agent and creates persistent **conversation work items** in SQLite. The server wakes the agent when a thread has new activity, the agent reads the unseen messages, and then acknowledges the work item.

The feature is **disabled by default** and does not change existing tools when it is off.

## New MCP tools

| Tool | Purpose |
|------|---------|
| `slack_watch_thread(channel_id, thread_ts)` | Subscribe a thread to polling. Required for the default `watch_threads` mode. |
| `slack_read_work_item(work_item_id)` | Fetch unseen messages for a leased work item and move it to `PROCESSING`. |
| `slack_ack_work_item(work_item_id)` | Mark a `PROCESSING` work item as done and advance the thread cursor. |
| `slack_heartbeat_work_item(work_item_id)` | Renew the lease while the agent is still working. |
| `slack_register_agent(agent_id, tmux_session)` | Register an agent and its tmux session for wake messages. |

## Work item lifecycle

```text
NEW -> LEASED -> PROCESSING -> ACKED -> ARCHIVED
```

If a lease expires without heartbeat or ACK, the work item returns to `NEW`, `retry_count` increments, and the scheduler tries again. After 10 retries the work item moves to `FAILED`.

## Enable the runtime

```bash
export SLACK_MCP_AGENT_RUNTIME_ENABLED=true
export SLACK_MCP_TMUX_SESSION=claude-main
export SLACK_MCP_AGENT_RUNTIME_TOOLS=1  # registers the runtime tools by default
```

## Key configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `SLACK_MCP_AGENT_RUNTIME_ENABLED` | `false` | Master switch. |
| `SLACK_MCP_AGENT_RUNTIME_DB` | `./data/agent_runtime.db` | SQLite database path. |
| `SLACK_MCP_AGENT_RUNTIME_DEFAULT_AGENT` | `default` | Default agent used by the scheduler. |
| `SLACK_MCP_AGENT_RUNTIME_POLLING_MODE` | `watch_threads` | Polling mode. |
| `SLACK_MCP_AGENT_RUNTIME_LEASE_DURATION` | `2m` | Lease duration. |
| `SLACK_MCP_AGENT_RUNTIME_HEARTBEAT_INTERVAL` | `30s` | Heartbeat interval. |
| `SLACK_MCP_AGENT_RUNTIME_MAX_RETRIES` | `10` | Maximum retries before `FAILED`. |
| `SLACK_MCP_TMUX_SESSION` | `''` | Default tmux session for wake messages. |

## Architecture

- `internal/events` — models, SQLite store, poller, manager, scheduler.
- `internal/wake` — `WakeProvider` interface and `TmuxWakeProvider`.
- `internal/runtime` — `AgentRuntime` orchestrator.
- `pkg/handler/agent_runtime.go` — MCP tool handlers.

For the full design, see the ADR in the sibling documentation repository: `docs/architecture/ADR-002-agent-runtime-conversation-work-items.md`.
