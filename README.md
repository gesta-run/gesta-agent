# Gesta Agent

Agents have deeds; Gesta records, governs, and proves them.

This repo contains the endpoint daemon for Gesta. It connects to a Gesta control
plane with a user-scoped API key, sends heartbeats, gathers metadata-only usage
events, evaluates command and prompt policies, redacts sensitive fields, and
keeps a local JSONL queue while offline.

On macOS, daemon heartbeats report Codex Desktop separately from the Codex
command-line adapter and include the application bundle version.

## Run

Create a connect token from the Gesta Console while signed in as the user who
will run the agent, then pass that user-bound token to the daemon:

```bash
go run ./cmd/gesta-agent run --control-url http://localhost:8080 --apikey sk-... --interval 1m --usage-window 10m
go run ./cmd/gesta-agent run --control-url http://localhost:8080 --apikey sk-... -- kubectl delete pod api
./scripts/install.sh --control-url http://localhost:8080 --apikey sk-...
cd "${HOME:-/tmp}" && curl -fsSL https://artifacts.gesta.run/gesta/install-agent.sh | bash -s -- --control-url http://localhost:8080 --apikey sk-...
```

The daemon does not require a separate enrollment step. `--apikey` is used
directly for heartbeats and event ingestion. Local queue and usage accounting
state live under `~/.gesta` by default. Each run loop also syncs active
control-plane policies into `~/.gesta/policies.json` for guard enforcement and
offline fallback.

Protocol-v3 events use the transactional bbolt database
`~/.gesta/queue-v3.db`. The queue retains at most 30 days and 512 MiB of encoded
events, replaces duplicate machine-state snapshots, and evicts the oldest
remaining events when it reaches the byte limit. OS-backed locking prevents
concurrent runners from sending the same batch and is released automatically
after a crash. The first protocol-v3 startup removes the incompatible
`queue-v2.db` and its drain lock. Upgrades intentionally do not replay the
former `queue.jsonl`; the daemon reports only aggregate metadata about that
legacy file and leaves it untouched for manual inspection.

## Commands

```bash
go run ./cmd/gesta-agent run --control-url http://localhost:8080 --apikey sk-... --interval 1m --usage-window 10m
go run ./cmd/gesta-agent run --control-url http://localhost:8080 --apikey sk-... -- kubectl delete pod api
go run ./cmd/gesta-agent install --control-url http://localhost:8080 --apikey sk-...
go run ./cmd/gesta-agent status
go run ./cmd/gesta-agent guard --agent codex -- kubectl delete pod api
```

`run` starts the daemon when no command is provided. When a command follows
`--`, `run` evaluates the latest control-plane policy before executing it. If
the control plane is temporarily unreachable, it uses the local policy cache; if
no cache exists, no policy rule is applied. The legacy `guard` command remains
as a compatibility alias for the same enforcement path.

`run` also installs and trusts Codex `PreToolUse`, `Stop`, and
`UserPromptSubmit` hooks in `~/.codex/hooks.json` and enables `[features].hooks` in
`~/.codex/config.toml`. It installs the corresponding Claude Code hooks,
including `PostToolUse` and `Stop`, in `~/.claude/settings.json`. `install`
performs the integration setup, which is useful during installation. The helper
script builds the daemon from this checkout when run locally, or downloads the
published binary from the artifact site:

```bash
./scripts/install.sh --control-url http://localhost:8080 --apikey sk-...
cd "${HOME:-/tmp}" && curl -fsSL https://artifacts.gesta.run/gesta/install-agent.sh | bash -s -- --control-url http://localhost:8080 --apikey sk-...
```

The installer requires both `--control-url` and `--apikey`. It saves daemon state under
`~/.gesta/state.json` so the Codex hook can fetch current policies before the
daemon loop is running. It does not print the API key or include it in the
long-running process arguments. By default it also starts the daemon in the
background. Published installs place the binary at `~/.gesta/bin/gesta-agent`
unless `--install-dir` or `--agent-bin` is provided. The installer detects the
host platform and downloads the matching published binary, including
`linux/amd64` for x86_64 Linux hosts and `darwin/arm64` for Apple Silicon Macs.
Use `--no-daemon` to install
only the hook and saved config. On macOS, the default daemon mode installs and
loads `~/Library/LaunchAgents/com.gesta.agent.plist` with `KeepAlive` enabled so
launchd restarts the agent after logout/login, sleep/wake recovery, or an
external process kill.

If the installer is run with `sudo`, it targets the original user account,
repairs ownership under `~/.gesta`, writes the macOS LaunchAgent under the
original user's `~/Library/LaunchAgents`, and starts the daemon as that user
when the platform supports it. That keeps later automatic agent upgrades
user-writable instead of requiring another interactive sudo prompt.

The script asks before restarting Codex Desktop so hook changes are picked up.
Set `GESTA_RESTART_CODEX=1` to restart without prompting, or
`GESTA_RESTART_CODEX=0` to skip the restart in automation.

When Codex hooks are enabled, `PreToolUse` evaluates shell command tool calls
before they run, including arbitrary Bash commands such as `ls -al`.
`UserPromptSubmit` evaluates prompts before submission so sensitive-data rules
can block prompt text before it leaves the local client. The hook uses the local
policy and sensitive-rule caches first, then falls back to the control plane or
built-in defaults. Restart Codex Desktop or open a new Codex thread after
installing the hook so the runtime reloads hook configuration.

Organization Context `keyword_any` rules match case-insensitively. Keywords
whose edges use ASCII letters, digits, or underscores match only at word
boundaries, so `PR` matches `PR #42` but not `prompt`, and `deploy` does not
match `deployment`. Non-ASCII keywords such as Chinese text continue to match
within continuous text. Use a regular-expression rule when partial-word
matching is intentional.

Output measurement uses agent-owned records rather than workspace or Git
inference. Codex `Stop` reads the completed turn through the official App Server
and counts added text from completed `fileChange` and `mcpToolCall` items.
Claude Code file writes and MCP input are measured after successful execution at
its public `PostToolUse` boundary. Raw diffs and tool arguments are discarded
locally; only counts and hashed correlation metadata are queued.

At the end of a primary-agent turn, Codex and Claude Code prepare one concise
`Gesta governance` line when a keyword or regex Organization Context rule matched,
or measurable output was durably queued. Every-prompt context remains active but
does not count toward the notice. The notice includes only the targeted context
append count and output summary, never rule names. `Stop` stores bounded
structured activity locally without blocking or starting another
model response. The next allowed prompt in the same session consumes that state,
creates a local detail only when the loopback UI is healthy, and receives an
internal instruction to place the formatted line at the bottom of its normal
response. Turns
with neither a targeted match nor output remain silent. Blocked prompts do not
consume pending notices. Local turn state contains only hashed identities,
bounded targeted-rule snapshots, aggregate counts, and at most one pending notice
per agent and session; all expire after 24 hours. When targeted context matched
and the daemon's loopback activity UI is available at notice consumption time,
the notice includes a `Details` link to
`127.0.0.1:3333`. The branded, server-rendered page shows rule names, match
types, priorities, the exact targeted context appended for that turn, and
aggregate output. It never stores or renders prompt text, keywords, regular
expressions, file paths, or file contents. Context snapshots stay on the local
machine and are never added to the Control event payload. The detail store is
capped at 256 records with a 24-hour TTL.

## Checks

```bash
make verify
```

## Project Structure

```text
cmd/gesta-agent/    Executable entrypoint
pkg/agent/          Command routing and process composition
pkg/agent/options/  Command option definitions
pkg/controlclient/  Control-plane HTTP transport
pkg/daemon/         Collection loop and agent integrations
pkg/eventqueue/     Durable, bounded event delivery queue
pkg/model/          Control-plane wire contracts
pkg/rulecache/      Validated local policy and runtime-setting caches
```

`cmd/gesta-agent` contains process wiring only. `pkg/agent` is the application
composition root; domain and foundation packages do not depend on it.
