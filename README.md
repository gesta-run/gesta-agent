# Gesta Agent

Agents have deeds; Gesta records, governs, and proves them.

This repo contains the endpoint daemon for Gesta. It connects to a Gesta control
plane with a user-scoped API key, sends heartbeats, gathers metadata-only usage
events, evaluates command and prompt policies, redacts sensitive fields, and
keeps a local JSONL queue while offline.

## Run

```bash
go run ./cmd run --control-url http://localhost:8080 --apikey sk-dev-local --interval 1m --usage-window 10m
go run ./cmd run --control-url http://localhost:8080 --apikey sk-dev-local -- kubectl delete pod api
./scripts/install.sh --control-url http://localhost:8080 --apikey sk-dev-local
cd "${HOME:-/tmp}" && curl -fsSL https://gesta-run.github.io/gesta-agent/install-agent.sh | bash -s -- --control-url http://localhost:8080 --apikey sk-dev-local
```

The daemon does not require a separate enrollment step. `--apikey` is used
directly for heartbeats and event ingestion. Local queue and usage accounting
state live under `~/.gesta` by default. Each run loop also syncs active
control-plane policies into `~/.gesta/policies.json` for guard enforcement and
offline fallback.

## Commands

```bash
go run ./cmd run --control-url http://localhost:8080 --apikey sk-dev-local --interval 1m --usage-window 10m
go run ./cmd run --control-url http://localhost:8080 --apikey sk-dev-local -- kubectl delete pod api
go run ./cmd install --control-url http://localhost:8080 --apikey sk-dev-local
go run ./cmd status
go run ./cmd guard --agent codex -- kubectl delete pod api
```

`run` starts the daemon when no command is provided. When a command follows
`--`, `run` evaluates the latest control-plane policy before executing it. If
the control plane is temporarily unreachable, it uses the local policy cache; if
no cache exists, no policy rule is applied. The legacy `guard` command remains
as a compatibility alias for the same enforcement path.

`run` also installs and trusts Codex `PreToolUse` and `UserPromptSubmit` hooks in
`~/.codex/hooks.json` and enables `[features].hooks` in
`~/.codex/config.toml`. `install` performs the integration setup, which is
useful during installation. The helper script builds the daemon from this
checkout when run locally, or downloads the published binary when run from
GitHub Pages:

```bash
./scripts/install.sh --control-url http://localhost:8080 --apikey sk-dev-local
cd "${HOME:-/tmp}" && curl -fsSL https://gesta-run.github.io/gesta-agent/install-agent.sh | bash -s -- --control-url http://localhost:8080 --apikey sk-dev-local
```

The installer requires both `--control-url` and `--apikey`. It saves daemon state under
`~/.gesta/state.json` so the Codex hook can fetch current policies before the
daemon loop is running. It prints the effective API key and a copyable
`gesta-agent run` command. By default it also starts the daemon in the
background. Published installs place the binary at `~/.gesta/bin/gesta-agent`
unless `--install-dir` or `--agent-bin` is provided. The installer detects the
host platform and downloads the matching published binary, including
`linux/amd64` for x86_64 Linux hosts and `darwin/arm64` for Apple Silicon Macs.
The daemon process reads the saved state and does not include
`--apikey` in its long-running process arguments. Use `--no-daemon` to install
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

## Checks

```bash
make verify
```
