# MCP Tools Reference

Complete reference for all MCP tools exposed by `slack-mcp-server`.

> **Token compatibility notes**
> - `conversations_search_messages`, `conversations_unreads`: not available with bot tokens (`xoxb`)
> - `saved_list`, `saved_update`, `saved_clear_completed`: requires browser session tokens (`xoxc`/`xoxd`) only
> - Agent Runtime tools (`slack_*`): require `SLACK_MCP_AGENT_RUNTIME_ENABLED=true`
> - `conversations_add_message`: requires `SLACK_MCP_ADD_MESSAGE_TOOL=1` env var (or explicit tool list)
> - `reactions_add`, `reactions_remove`: require `SLACK_MCP_REACTION_TOOL=1`
> - `attachment_get_data`: requires `SLACK_MCP_ATTACHMENT_TOOL=1`

---

## Conversations

### `conversations_history`

Get messages from a channel or DM. Supports pagination via cursor.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `channel_id` | string | ✅ | — | Channel ID (`Cxxxxxxxxxx`) or name (`#general`, `@username`) |
| `include_activity_messages` | boolean | | `false` | Include join/leave activity messages |
| `cursor` | string | | — | Pagination cursor from previous response (last column of last row) |
| `limit` | string | | `1d` | Time range (`1d`, `1w`, `30d`, `90d`) or message count (`50`) |

**Example:**
```
conversations_history(channel_id="C0BCC0P6C5S", limit="1d")
conversations_history(channel_id="#general", limit="50")
conversations_history(channel_id="C0BCC0P6C5S", cursor="dXNlcjpVMDYx...")
```

---

### `conversations_replies`

Get all messages in a thread.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `channel_id` | string | ✅ | — | Channel ID or name |
| `thread_ts` | string | ✅ | — | Parent message timestamp (`1234567890.123456`) |
| `include_activity_messages` | boolean | | `false` | Include activity messages |
| `cursor` | string | | — | Pagination cursor |
| `limit` | string | | `1d` | Time range or message count |

**Example:**
```
conversations_replies(channel_id="C0BCC0P6C5S", thread_ts="1782740266.609719")
conversations_replies(channel_id="#general", thread_ts="1782740266.609719", limit="50")
```

---

### `conversations_add_message`

Send a message to a channel or reply to a thread.

> Requires `SLACK_MCP_ADD_MESSAGE_TOOL=1` or explicit tool list.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `channel_id` | string | ✅ | — | Channel ID or name |
| `thread_ts` | string | | — | If provided, sends as a thread reply |
| `text` | string | | — | Message text |
| `content_type` | string | | `text/markdown` | `text/markdown` or `text/plain` |
| `blocks` | string | | — | Raw Slack Block Kit JSON array (overrides text rendering) |
| `username` | string | | — | Override bot display name (requires `chat:write.customize` scope) |
| `icon_emoji` | string | | — | Override bot icon emoji, e.g. `:robot_face:` |

**Example:**
```
# Send to channel
conversations_add_message(channel_id="C0BCC0P6C5S", text="Hello, world!")

# Reply in thread
conversations_add_message(channel_id="C0BCC0P6C5S", thread_ts="1782740266.609719", text="pong")

# Markdown
conversations_add_message(channel_id="#general", text="## Report\n- item one\n- item two", content_type="text/markdown")
```

---

### `conversations_search_messages`

Search messages with filters.

> Not available with bot tokens (`xoxb`).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `search_query` | string | | — | Search text or full Slack message URL |
| `filter_in_channel` | string | | — | Channel ID or name to search in |
| `filter_in_im_or_mpim` | string | | — | DM/MPIM ID or `@username` |
| `filter_users_with` | string | | — | User ID or `@username` in threads/DMs |
| `filter_users_from` | string | | — | Filter by message author |
| `filter_date_before` | string | | — | `YYYY-MM-DD`, `Today`, `Yesterday` |
| `filter_date_after` | string | | — | `YYYY-MM-DD`, `Today`, `Yesterday` |
| `filter_date_on` | string | | — | `YYYY-MM-DD`, `Today`, `Yesterday` |
| `filter_date_during` | string | | — | `July`, `Yesterday`, `Today` |
| `filter_threads_only` | boolean | | `false` | Only return thread messages |
| `cursor` | string | | — | Pagination cursor |
| `limit` | number | | `20` | Max results (1–100) |

**Example:**
```
conversations_search_messages(search_query="deployment failed", filter_in_channel="#ops")
conversations_search_messages(filter_users_from="@alice", filter_date_after="2026-06-01")
conversations_search_messages(search_query="https://slack.com/archives/C0BCC0P6C5S/p1782740266609719")
```

---

### `conversations_unreads`

Get unread messages across all channels.

> Not available with bot tokens (`xoxb`).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `include_messages` | boolean | | `true` | Return actual messages (false = summary only) |
| `channel_types` | string | | `all` | `all`, `dm`, `group_dm`, `partner`, `internal` |
| `max_channels` | number | | `50` | Max channels to check |
| `max_messages_per_channel` | number | | `10` | Max messages per channel |
| `mentions_only` | boolean | | `false` | Only channels with @mentions |
| `include_muted` | boolean | | `false` | Include muted channels |

**Example:**
```
conversations_unreads()
conversations_unreads(channel_types="dm", mentions_only=true)
conversations_unreads(include_messages=false)
```

---

### `conversations_mark`

Mark a channel as read.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `channel_id` | string | ✅ | — | Channel ID or name |
| `ts` | string | | — | Read up to this timestamp; omit to mark all as read |

**Example:**
```
conversations_mark(channel_id="C0BCC0P6C5S")
conversations_mark(channel_id="#general", ts="1782740266.609719")
```

---

### `conversations_leave`

Leave a channel.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `channel_id` | string | ✅ | — | Channel ID or name |

**Example:**
```
conversations_leave(channel_id="C0BCC0P6C5S")
```

---

### `conversations_join`

Join a public channel.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `channel_id` | string | ✅ | — | Channel ID or name starting with `#` |

**Example:**
```
conversations_join(channel_id="#general")
```

---

## Reactions

### `reactions_add`

Add an emoji reaction to a message.

> Requires `SLACK_MCP_REACTION_TOOL=1`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `channel_id` | string | ✅ | Channel ID or name |
| `timestamp` | string | ✅ | Message timestamp |
| `emoji` | string | ✅ | Emoji name without colons (e.g. `thumbsup`, `rocket`) |

**Example:**
```
reactions_add(channel_id="C0BCC0P6C5S", timestamp="1782740266.609719", emoji="thumbsup")
```

---

### `reactions_remove`

Remove an emoji reaction from a message.

> Requires `SLACK_MCP_REACTION_TOOL=1`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `channel_id` | string | ✅ | Channel ID or name |
| `timestamp` | string | ✅ | Message timestamp |
| `emoji` | string | ✅ | Emoji name without colons |

**Example:**
```
reactions_remove(channel_id="C0BCC0P6C5S", timestamp="1782740266.609719", emoji="thumbsup")
```

---

## Files

### `attachment_get_data`

Download an attachment by file ID. Returns metadata and content (text as-is, binary as base64). Max 5 MB.

> Requires `SLACK_MCP_ATTACHMENT_TOOL=1`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `file_id` | string | ✅ | File ID (`Fxxxxxxxxxx`), found in message `attachmentIDs` field |

**Example:**
```
attachment_get_data(file_id="F0BB7NC9072")
```

---

## Channels

### `channels_list`

List workspace channels.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `channel_types` | string | ✅ | — | Comma-separated: `mpim`, `im`, `public_channel`, `private_channel` |
| `sort` | string | | — | `popularity` to sort by member count |
| `limit` | number | | `100` | Max results (1–999) |
| `cursor` | string | | — | Pagination cursor |
| `query` | string | | — | Filter by keyword (case-insensitive) |
| `query_targets` | string | | `name` | Fields to search: `name`, `topic`, `purpose` |

**Example:**
```
channels_list(channel_types="public_channel,private_channel")
channels_list(channel_types="im", query="alice")
channels_list(channel_types="public_channel", sort="popularity", limit=20)
```

---

### `channels_me`

List channels you are a member of.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `channel_types` | string | | `public_channel,private_channel` | Comma-separated channel types |
| `limit` | number | | `100` | Max results (1–999) |
| `cursor` | string | | — | Pagination cursor |

**Example:**
```
channels_me()
channels_me(channel_types="public_channel,private_channel,im")
```

---

## Users

### `users_search`

Search for users by name, display name, email, or Slack user ID.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | ✅ | — | Name, email, `@username`, or `Uxxxxxxxxxx` user ID |
| `limit` | number | | `10` | Max results (1–100) |

**Example:**
```
users_search(query="alice")
users_search(query="U07VCEPP4N5")
users_search(query="alice@example.com")
```

---

## User Groups

### `usergroups_list`

List all user groups (mention groups like `@engineering`).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `include_users` | boolean | | `false` | Include user ID list per group |
| `include_count` | boolean | | `true` | Include member count |
| `include_disabled` | boolean | | `false` | Include archived/disabled groups |

**Example:**
```
usergroups_list()
usergroups_list(include_users=true)
```

---

### `usergroups_me`

Manage your own group membership.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | ✅ | `list`, `join`, or `leave` |
| `usergroup_id` | string | | Group ID (`Sxxxxxxxxxx`), required for `join`/`leave` |

**Example:**
```
usergroups_me(action="list")
usergroups_me(action="join", usergroup_id="S0123456789")
usergroups_me(action="leave", usergroup_id="S0123456789")
```

---

### `usergroups_create`

Create a new user group.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | ✅ | Display name (e.g. `Engineering Team`) |
| `handle` | string | | `@mention` handle without `@` (e.g. `engineering`) |
| `description` | string | | Purpose/description |
| `channels` | string | | Comma-separated default channel IDs |

**Example:**
```
usergroups_create(name="AI Agents", handle="ai-agents", description="Autonomous agent team")
```

---

### `usergroups_update`

Update a user group's metadata. At least one field required.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `usergroup_id` | string | ✅ | Group ID (`Sxxxxxxxxxx`) |
| `name` | string | | New display name |
| `handle` | string | | New `@mention` handle |
| `description` | string | | New description |
| `channels` | string | | New default channel IDs (replaces existing) |

**Example:**
```
usergroups_update(usergroup_id="S0123456789", description="Updated description")
```

---

### `usergroups_users_update`

Replace all members of a user group. **Destructive** — replaces the entire member list.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `usergroup_id` | string | ✅ | Group ID (`Sxxxxxxxxxx`) |
| `users` | string | ✅ | Comma-separated user IDs (complete new member list) |

**Example:**
```
usergroups_users_update(usergroup_id="S0123456789", users="U0001,U0002,U0003")
```

---

## Saved Items

> Requires browser session tokens (`xoxc`/`xoxd`).

### `saved_list`

List items from the "Save for Later" panel.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `filter` | string | | `saved` | `saved`, `completed`, or `archived` |
| `limit` | number | | `50` | Max results |
| `include_messages` | boolean | | `true` | Fetch actual message content |
| `max_messages_per_item` | number | | `5` | Max messages per saved item |

**Example:**
```
saved_list()
saved_list(filter="completed", include_messages=false)
```

---

### `saved_update`

Mark a saved item as completed or set a due date.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `item_id` | string | ✅ | Channel/DM ID from `saved_list` |
| `ts` | string | ✅ | Message timestamp from `saved_list` |
| `mark` | string | | `completed` to mark done |
| `date_due` | number | | Unix timestamp for reminder; `0` to clear |

**Example:**
```
saved_update(item_id="C0BCC0P6C5S", ts="1782740266.609719", mark="completed")
saved_update(item_id="C0BCC0P6C5S", ts="1782740266.609719", date_due=1751356800)
```

---

### `saved_clear_completed`

Clear all completed saved items. No parameters.

**Example:**
```
saved_clear_completed()
```

---

## Agent Runtime Tools

> Requires `SLACK_MCP_AGENT_RUNTIME_ENABLED=true`.

### `slack_register_agent`

Register an agent and its tmux session for wake messages. Call this at agent startup.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `agent_id` | string | | Agent ID; defaults to configured default agent |
| `tmux_session` | string | | tmux session name for wake messages (e.g. `claude-sda-main`) |

**Example:**
```
slack_register_agent(agent_id="agent-01", tmux_session="claude-sda-main")
```

---

### `slack_watch_thread`

Subscribe a thread to polling. The runtime creates work items when new messages appear.

> Used in `watch_threads` polling mode.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `channel_id` | string | ✅ | Channel containing the thread |
| `thread_ts` | string | ✅ | Parent message timestamp of the thread |

**Example:**
```
slack_watch_thread(channel_id="C0BCC0P6C5S", thread_ts="1782740266.609719")
```

---

### `slack_read_work_item`

Fetch unseen messages for a leased work item. Moves the item from `LEASED` → `PROCESSING`. Returns CSV with columns: `msgID`, `userID`, `userUser`, `realName`, `channelID`, `ThreadTs`, `text`, `time`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `work_item_id` | string | ✅ | Work item ID from the wake message |
| `agent_id` | string | | Agent holding the lease; defaults to configured default |

**Example:**
```
slack_read_work_item(work_item_id="1782740266.609719-v1")
```

**Response columns:**

| Column | Description |
|--------|-------------|
| `msgID` | Message timestamp — use as `thread_ts` if `ThreadTs` is empty |
| `userID` | Sender's Slack user ID |
| `userUser` | Sender's username |
| `realName` | Sender's real name |
| `channelID` | Channel ID — use for reply |
| `ThreadTs` | Thread parent timestamp — use as `thread_ts` for replies |
| `text` | Message text |
| `time` | Raw Slack timestamp |

**Ping→Pong pattern:**
```python
result = slack_read_work_item(work_item_id='1782740266.609719-v1')
# from CSV:
channel_id = row['channelID']
thread_ts  = row['ThreadTs'] or row['msgID']

if row['text'] == 'ping':
    conversations_add_message(channel_id=channel_id, thread_ts=thread_ts, text="pong")

slack_ack_work_item(work_item_id='1782740266.609719-v1')
```

---

### `slack_ack_work_item`

Acknowledge a processed work item. Advances the thread cursor so the next poll skips already-seen messages.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `work_item_id` | string | ✅ | Work item ID to acknowledge |
| `agent_id` | string | | Agent that processed the item |

**Example:**
```
slack_ack_work_item(work_item_id="1782740266.609719-v1")
```

---

### `slack_heartbeat_work_item`

Renew the lease while the agent is still processing. Call every ~30s for long-running tasks.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `work_item_id` | string | ✅ | Work item ID to renew |
| `agent_id` | string | | Agent holding the lease |

**Example:**
```
slack_heartbeat_work_item(work_item_id="1782740266.609719-v1")
```

---

## Typical Agent Flow

```text
1. slack_register_agent(agent_id="agent-01", tmux_session="claude-sda-main")

   [wake message arrives via tmux]

2. slack_read_work_item(work_item_id="<id>")
   → returns CSV with new messages

3. [process messages, optionally call conversations_add_message to reply]

   [for long tasks, periodically:]
   slack_heartbeat_work_item(work_item_id="<id>")

4. slack_ack_work_item(work_item_id="<id>")
```
