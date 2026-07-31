# Package Structure Alignment

## Status

Approved for implementation.

## Goal

Remove the root `internal/` directory and align the repository with the
control-plane `cmd/* + pkg/*` structure.

This is a behavior-neutral Go package relocation. It changes source paths and
repository-local imports only.

## Target structure

```text
cmd/
  gesta-agent/       Executable entrypoint

pkg/
  agent/             Command routing and process composition
    options/         Command options
  agentupgrade/      Upgrade decisions and binary replacement
  atomicfile/        Atomic file persistence
  controlclient/     Control-plane HTTP transport
  hookinstall/       Codex and Claude hook installation
  lockfile/          Cross-process file locking
  mcpmeta/           MCP identity normalization
  statecleanup/      Deprecated-state cleanup
  architecture/      Import-boundary tests

  activitydetail/
  codexapp/
  contextmatch/
  daemon/
  eventqueue/
  localactivity/
  model/
  policy/
  privacy/
  promptscope/
  rulecache/
  toolinput/
  turnreceipt/
  util/
```

## Moves

- Command routing moves to `pkg/agent`; its package name becomes `agent`.
- Command options move below `pkg/agent/options`.
- Upgrade, HTTP client, hook installation, atomic file, lock, MCP metadata, and
  cleanup packages move directly below `pkg`.
- The executable imports `pkg/agent`.
- The root `internal/` directory disappears.

## Dependency rules

- `cmd/gesta-agent` imports only `pkg/agent` from this module.
- `pkg/agent` is the process composition root.
- Packages outside `pkg/agent` must not import `pkg/agent`.
- No Go import may contain `/internal/`.

Architecture tests enforce these rules.

## Unchanged behavior

The move does not change:

- CLI commands, flags, output, errors, or exit codes.
- Hook installation paths or command lines.
- Configuration, queue, cache, cursor, lock, receipt, or activity-detail data.
- Control API endpoints, headers, authentication, or JSON.
- Collection, retention, cleanup, upgrade, or delivery behavior.
- Binary names, module path, installer, or release artifacts.

## Implementation

1. Move packages and update package declarations and imports.
2. Update the executable entrypoint.
3. Add architecture boundary tests.
4. Update README and design-document paths.
5. Confirm no root `internal` directory or stale import remains.
6. Run formatting, vet, static analysis, race tests, and the full build.

## Architecture and product review

### High-confidence problem

The current `pkg/* -> internal/*` dependency shape is misleading and makes the
repository harder to navigate. A single package model fixes that directly.

### Over-engineering excluded

This change does not introduce a local-state abstraction, split collectors,
generate contracts, alter protocols, or clean up legacy CLI behavior. Those are
separate concerns.

### Smallest sufficient version

Moving the existing packages, updating imports, adding one boundary test, and
updating documentation is sufficient. No compatibility layer or migration code
is needed.
