# Windows Agent RC

## Status

Approved and implemented with the compatibility decisions below. Windows runner
verification and the release smoke test remain required before publication.

## Goal

Provide a native Windows RC that an employee can install from the Gesta Console
with one PowerShell command and without administrator privileges. The installed
agent must connect as the current employee, configure supported Codex CLI and
Claude Code hooks, recover after sign-out and sign-in, and retain the bounded
offline behavior used on macOS and Linux.

## Compatibility

- Existing macOS and Linux artifacts, `install.sh`, paths, launch behavior, and
  automatic upgrades remain unchanged.
- Windows state uses `%USERPROFILE%\.gesta`, matching the existing `~/.gesta`
  convention.
- Protocol, heartbeat, queue, policy, and hook JSON formats do not change.
- Windows is an additive release target and requires no control-plane schema
  migration.

## RC scope

### Included

- Windows 10 22H2 or newer and Windows 11 on `windows/amd64`.
- Windows PowerShell 5.1 and PowerShell 7.
- A native `gesta-agent-windows-amd64.exe` release artifact.
- A user-level PowerShell installer published as `install-agent.ps1`.
- SHA-256 verification before the executable is installed or run.
- Installation under `%USERPROFILE%\.gesta\bin\gesta-agent.exe`.
- Existing Codex CLI and Claude Code integrations, with Windows-safe hook
  command quoting.
- A limited current-user Scheduled Task named `Gesta Agent` that starts at
  logon and restarts after failure without containing an API key.
- Runtime health verification after installation.
- Idempotent reinstall and repair using the same Connect command.
- Windows CI compilation and platform-specific tests.

### Deferred

- Windows ARM64.
- MSI, WinGet, Scoop, Chocolatey, Intune, and Group Policy packaging.
- Windows Service installation because it requires elevation and changes the
  per-user ownership model.
- Native Codex Desktop discovery until its Windows executable and local data
  contract are explicitly supported.
- Authenticode signing. A private RC may remain unsigned, but signing is
  required before broad production rollout.

## Installation experience

The Console will eventually render the employee-bound command. The agent RC
provides this command shape:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "& { $p = Join-Path $env:TEMP 'install-gesta.ps1'; Invoke-WebRequest 'https://artifacts.gesta.run/gesta/install-agent.ps1' -OutFile $p; & $p -ControlUrl '<control-url>' -ApiKey '<connect-token>' }"
```

`install-agent.ps1` performs these operations:

1. Reject unsupported Windows versions and architectures.
2. Create user-owned Gesta directories.
3. Download the RC checksum file and Windows executable to a temporary file.
4. Verify the exact artifact SHA-256 and fail closed on mismatch.
5. Install or repair the local executable.
6. Call `gesta-agent install` so existing state and hook logic remain the
   source of truth.
7. Register or update the current-user Scheduled Task.
8. Start the task and wait for the configured daemon health endpoint.
9. Print the installed version, state path, task name, and status command.

The API key is passed only to the short-lived installer and `install` process.
It is persisted in the current user's state file and omitted from the Scheduled
Task action and long-running process arguments. The installer applies a
current-user-only ACL to the state directory because Unix permission bits do not
provide equivalent protection on Windows.

## Runtime changes

### Release build

The release build produces the console `gesta-agent-windows-amd64.exe` and the
GUI-subsystem `gesta-agent-hook-launcher-windows-amd64.exe`. Both are included
in `SHA256SUMS`; the release manifest associates the launcher with the Windows
agent so installs and automatic upgrades verify and replace them together.

### File locking and atomic writes

The Windows queue drain lock uses `LockFileEx` and `UnlockFileEx` from
`golang.org/x/sys/windows`, preserving the existing timeout and single-drainer
semantics.

Windows atomic replacement uses `MoveFileEx` with replace-existing semantics so
readers never observe partial JSON state.

### Process restart and executable replacement

`syscall.Exec`, Unix ownership restoration, and replacement of a running
executable are separated behind platform files.

Windows upgrades run through a detached helper and replace the agent and hook
launcher as one rollback-safe bundle. After an older installation upgrades the
main executable, the new agent detects a missing or outdated launcher and
applies the same-version companion migration on its next heartbeat. macOS and
Linux automatic upgrades remain unchanged.

### Hook commands

Windows hook commands use a quoted absolute executable path and support user
profiles containing spaces. Reinstall replaces existing Gesta hook entries
idempotently instead of duplicating them.

### Background task and health

The Scheduled Task runs the equivalent of:

```text
%USERPROFILE%\.gesta\bin\gesta-agent.exe run --control-url <url> --data-dir "%USERPROFILE%\.gesta" --interval 1m --usage-window 10m
```

It uses the installing user with limited privileges and reads authentication
from saved state. After starting the task, the installer calls
`gesta-agent status --data-dir <path> --require-running --wait 15s`. The local health endpoint
identifies the configured daemon ID so an unrelated listener cannot satisfy the
check.

## Capacity and performance

Windows uses the existing bbolt queue bounds: 30 days and 512 MiB. The design
adds no per-event process creation or polling. Adapter collection remains
concurrent and the one-minute heartbeat interval is unchanged.

The Windows lock is held only during the existing drain critical section.
Installer downloads and checksum calculation stream data to keep memory usage
constant as artifacts grow.

## Failure and recovery

- A failed checksum leaves the installed executable untouched.
- A failed hook update preserves existing valid user configuration.
- A task that fails to become healthy causes installation to fail.
- Re-running the Connect command repairs the binary, hooks, state, and task.
- Installation logs redact the API key and never print it back to the terminal.
- This RC includes no uninstall workflow.

## Verification

- Windows cross-compilation must succeed.
- Windows tests cover queue locking, atomic replacement, paths with spaces,
  hook quoting, runtime identity health, checksum failure, idempotent install,
  task action secret absence, and non-admin installation.
- Existing macOS and Linux tests and artifact names remain unchanged.
- A Windows 11 smoke test must install from the published RC command, observe
  two heartbeats, exercise one safe Codex or Claude hook, sign out and in, and
  confirm that the Scheduled Task restores the daemon.

## Delivery sequence

1. Merge the platform and installer changes.
2. Merge the release workflow support without publishing a public bootstrap.
3. Publish the next RC with the Windows binary, immutable installer, manifest,
   and public bootstrap in one release pull request.
4. Complete the Windows 11 smoke test before exposing the command in Console.

## Accepted decisions

1. Use `%USERPROFILE%\.gesta` for cross-platform compatibility.
2. Require reinstall for Windows RC upgrades while preserving automatic
   upgrades on macOS and Linux.
3. Allow an unsigned private `windows/amd64` RC and require Authenticode before
   general availability.
