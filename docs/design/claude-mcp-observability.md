# Claude Code MCP Observability

## Status

Accepted on 2026-07-24.

## Problem

The Claude Code adapter currently discovers MCP servers by running:

```text
claude mcp list
```

That command health-checks configured servers and is executed by the daemon from
its service working directory. It can therefore time out and it does not
reliably represent MCP servers configured in other Claude Code projects.

Claude Code transcripts are already parsed for session and token usage, but the
parser ignores `tool_use` content blocks. As a result, the control plane receives
neither reliable Claude Code MCP inventory nor actual Claude Code MCP calls.

## Goals

- Report configured Claude Code MCP server names without starting or
  health-checking MCP servers.
- Report actual Claude Code MCP tool calls from transcripts.
- Reuse the existing `MCPInventoryStatus` heartbeat field and emit the
  identity-aware `tool.call` contract consumed by the control plane.
- Upload metadata only. Never upload MCP configuration values, environment
  variables, commands, URLs, headers, tool inputs, tool results, or transcript
  text as part of MCP telemetry.
- Preserve the existing first-run behavior: historical sessions and historical
  tool calls are not backfilled.
- Parse each Claude transcript only once per collection cycle.

## Non-goals

- Do not search for, open, or parse project `.mcp.json` files.
- Do not infer MCP inventory by walking project directories.
- Do not test MCP connectivity or report MCP health.
- Do not add Claude-specific control-plane storage or API behavior.
- Do not add a general transcript ingestion framework in this change.

## Proposed design

### 1. Configuration inventory from `~/.claude.json`

Replace `claude mcp list` in the Claude adapter with a bounded, read-only parser
for `~/.claude.json`.

The parser reads names from:

- top-level `mcpServers`, which represents user-scoped configuration;
- `projects[*].mcpServers`, which represents Claude's centrally recorded
  project-local configuration.

It does not follow project paths and does not read project `.mcp.json` files.
Project paths are not included in the output. Server names from all supported
scopes are normalized, de-duplicated, sorted, and reported as configured.

Only object keys are retained. Configuration values remain `json.RawMessage`
and are discarded after parsing, preventing commands, arguments, environment
variables, URLs, and headers from entering telemetry payloads or logs.

Behavior:

- missing file or missing `mcpServers`: successful empty inventory;
- valid configuration: `scan_status=ok`, stable hash, sorted server names;
- unreadable, oversized, or malformed file: `scan_status=error` with a bounded
  error code and no configuration content;
- all discovered entries use `enabled=true`, where enabled means configured,
  not reachable.

The parser uses a fixed maximum file size. It performs one read and one JSON
decode per collection cycle.

### 2. MCP calls from the existing transcript scan

Extend `claudeTranscriptRecord` parsing to inspect assistant
`message.content` blocks with:

```json
{
  "type": "tool_use",
  "id": "toolu_...",
  "name": "mcp__server__tool"
}
```

Only names that can be split into a non-empty MCP server and tool are retained.
The `input` field and all `tool_result` blocks are ignored.

The parsed per-session value gains a metadata-only tool-call collection:

- raw call ID, held in memory only;
- MCP server name;
- MCP tool name;
- transcript timestamp.

Calls are de-duplicated inside each transcript by call ID, with a deterministic
metadata fallback for malformed records that have no call ID. When Claude
splits or resumes a session across files, tool calls are merged by the same
identity before events are built.

No second transcript read is introduced. Usage, session text, and MCP metadata
are fanned out from the same parsed session objects.

### 3. Identity-aware wire contract and idempotency

For each observed MCP call, emit:

```text
event_type: tool.call
source: claude_code
agent_type: claude_code
payload:
  metadata_only: true
  tool_type: mcp
  tool_name: mcp__server__tool
  mcp_server_id: normalized server namespace
  mcp_server_name: trusted configured name, omitted for UUID-shaped IDs
  mcp_tool: tool
  session_id: hashed session ID
  session_id_hash: hashed session ID
  session_id_is_hashed: true
  call_id_hash: hashed call ID, when present
  observed_at: transcript timestamp
```

When a call ID is present, the event ID is deterministic over the agent type,
hashed session ID, and hashed call ID. Transcript block position and mutable
metadata are excluded so the identity remains stable if Claude rewrites or
resumes a transcript. Records without a call ID use a deterministic metadata
fallback.

Tool-call events have a durable per-session cursor. On first observation, the
session and its existing tool calls are baselined and omitted. Later collection
cycles emit only calls after the cursor. The cursor stores the latest timestamp
and the event IDs sharing that timestamp, which bounds persistent state while
preserving multiple calls recorded at the same instant.

Collection only prepares the cursor update. The runner commits it after every
returned event has been appended to the local durable queue. If queue append
fails or the daemon exits before that point, the cursor remains unchanged and
the same deterministic events are prepared again on the next cycle.

### 4. Privacy boundary

Allowed:

- normalized MCP server name;
- normalized MCP tool name;
- hashed session and call identifiers;
- call timestamp;
- agent type.

Forbidden:

- MCP command, arguments, URL, headers, environment, or secrets;
- tool input or tool output;
- user prompt or assistant response;
- local project or transcript path.

Tests serialize emitted heartbeat and event payloads and assert that sentinel
configuration secrets, inputs, results, and local paths are absent.

## Detailed code changes

- `pkg/daemon/claude.go`
  - replace the CLI inventory command with the local configuration parser;
  - emit Claude MCP tool-call events from baseline-approved sessions.
- `pkg/daemon/claude_mcp_inventory.go`
  - add the bounded `~/.claude.json` parser and inventory normalization.
- `pkg/daemon/claude_transcript_parse.go`
  - extract `tool_use` MCP metadata during the existing scan.
- `pkg/daemon/claude_transcript_merge.go`
  - merge and de-duplicate calls across transcript files for one session.
- `pkg/daemon/claude_usage_types.go`
  - add the internal Claude MCP call type and per-session collection.
- `pkg/daemon/claude_mcp_events.go`
  - build metadata-only `tool.call` envelopes with deterministic IDs.
- `pkg/daemon/claude_baseline.go`
  - filter tool calls through a bounded per-session cursor and return a deferred
    baseline commit.
- `pkg/daemon/collect.go` and `pkg/daemon/runner.go`
  - aggregate adapter commits and execute them only after durable queue append.
- Existing Claude tests
  - cover inventory parsing, transcript extraction, merge de-duplication,
    first-run baseline behavior, queue-safe cursor commits, repeated-cycle
    idempotency, and privacy.

## Versioning decision

Backward and forward compatibility with existing agent versions is explicitly
not required. No compatibility shim or data migration will be added.

The heartbeat inventory remains unchanged. The `tool.call` payload intentionally
replaces `mcp_server` with a required `mcp_server_id` and optional
`mcp_server_name`. This is a coordinated Agent, Control, and Console upgrade.
Old queued events remain generic session events but do not repopulate MCP
analytics after its planned reset.

## Performance and limits

- Configuration work is linear in one bounded `~/.claude.json` file.
- Transcript work remains linear in the bytes already scanned; MCP extraction is
  an additional branch in the existing JSONL loop.
- Session merge memory grows with MCP calls in the already selected transcript
  set. Inputs and results are never retained.
- Each cycle still reconstructs in-memory metadata from the selected
  transcripts, but only post-cursor calls enter the durable queue. Persistent
  cursor storage is bounded to one timestamp plus the event IDs at that
  timestamp per session.

## Validation

1. Unit-test inventory parsing for user scope, centrally recorded local scope,
   duplicates, missing file, malformed JSON, oversized input, and secret
   exclusion.
2. Unit-test transcript parsing for MCP calls, non-MCP tools, repeated streaming
   chunks, missing IDs, and malformed blocks.
3. Unit-test cross-file session merge and call-ID-stable deterministic event
   IDs.
4. Unit-test first-run no-backfill, subsequent calls, same-timestamp calls, and
   retry before cursor commit.
5. Run the repository's full build and test suite.
6. Run the daemon locally against a fixture Claude home and verify that the
   heartbeat and queued events contain only approved metadata.

## Architecture and product review

### High-confidence issues

1. **Configured does not mean healthy.** Direct configuration parsing fixes the
   timeout and coverage problem, but the UI must continue to describe the list
   as configured or observed, not connected.
2. **Centrally recorded project-local entries can be stale.** Reading
   `projects[*].mcpServers` captures Claude's default local scope without opening
   projects, but old project entries may remain after a project is deleted.
   Actual-call timestamps remain the reliable usage signal.
3. **Transcript parsing still starts at the beginning of each selected file.**
   The bounded event cursor prevents repeated queue and network work, but it
   does not reduce transcript read cost. A byte-offset cursor is only warranted
   if measured local parsing cost becomes significant.

### Overengineering to avoid

- Walking every known project and parsing `.mcp.json`.
- Starting MCP servers to determine health.
- Adding a control-plane table or a Claude-specific API.
- Persisting full tool inputs/results for richer analytics.
- Adding a byte-offset cursor before real queue/network measurements justify
  the additional crash-recovery state machine.

### Smaller viable version

A smaller version can solve the reported problem:

1. parse only MCP names already present in `~/.claude.json`;
2. extract only `mcp__*` `tool_use` blocks during the existing transcript pass;
3. emit the existing deterministic `tool.call` event.

This version is sufficient for pre, keeps the data contract stable, avoids
project `.mcp.json` discovery, and uses the bounded queue-safe event cursor for
incremental delivery.
