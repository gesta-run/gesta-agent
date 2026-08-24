#!/bin/sh
set -eu

target_user=${USER:-}
target_uid=$(id -u)
if [ "$target_uid" = 0 ] && [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER:-}" != root ]; then
  target_user=$SUDO_USER
  target_uid=${SUDO_UID:-$(id -u "$target_user")}
  if command -v dscl >/dev/null 2>&1; then
    target_home=$(dscl . -read "/Users/$target_user" NFSHomeDirectory 2>/dev/null | awk '{print $2; exit}')
  elif command -v getent >/dev/null 2>&1; then
    target_home=$(getent passwd "$target_user" | awk -F: '{print $6; exit}')
  else
    target_home=
  fi
  if [ -n "$target_home" ]; then
    HOME=$target_home
    export HOME
  fi
fi

run_as_target_user() {
  if [ "$(id -u)" = 0 ] && [ -n "$target_user" ] && [ "$target_user" != root ]; then
    sudo -u "$target_user" env HOME="$HOME" "$@"
  else
    "$@"
  fi
}

yes=0
keep_data=0
daemon_label=${GESTA_DAEMON_LABEL:-com.gesta.agent}
data_dir=${GESTA_DAEMON_DATA_DIR:-${GESTA_DATA_DIR:-"$HOME/.gesta"}}
launch_agents_dir=${GESTA_LAUNCH_AGENTS_DIR:-"$HOME/Library/LaunchAgents"}
agent_bin_arg=

usage() {
  printf '%s\n' \
    'Usage: ./scripts/uninstall.sh [options]' \
    '' \
    'Options:' \
    '  --yes                 Skip the confirmation prompt.' \
    '  --keep-data           Keep agent state and logs; remove the binary and service.' \
    '  --data-dir path       Agent state directory. Default: ~/.gesta' \
    '  --agent-bin path      Installed gesta-agent binary.' \
    '  --daemon-label label  launchd label. Default: com.gesta.agent' \
    '  --help                Show this help.'
}

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

agent_process_running() {
  process_id=$1
  process_command=$(ps -p "$process_id" -o command= 2>/dev/null || true)
  case "$process_command" in
    *"$agent_bin"*" run"*) return 0 ;;
    *) return 1 ;;
  esac
}

stop_agent_process() {
  process_id=$1
  agent_process_running "$process_id" || return 0
  kill "$process_id" 2>/dev/null || true
  stop_attempt=0
  while agent_process_running "$process_id" && [ "$stop_attempt" -lt 10 ]; do
    sleep 1
    stop_attempt=$((stop_attempt + 1))
  done
  if agent_process_running "$process_id"; then
    kill -9 "$process_id" 2>/dev/null || true
    sleep 1
  fi
  agent_process_running "$process_id" && fail "could not stop Gesta Agent process $process_id"
}

canonical_directory() {
  path=$1
  if [ -d "$path" ]; then
    CDPATH='' cd -- "$path" 2>/dev/null && pwd -P
    return
  fi
  parent=$(dirname -- "$path")
  base=$(basename -- "$path")
  parent=$(CDPATH='' cd -- "$parent" 2>/dev/null && pwd -P) || return 1
  printf '%s/%s\n' "$parent" "$base"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --yes|-y)
      yes=1
      ;;
    --keep-data)
      keep_data=1
      ;;
    --data-dir)
      [ "$#" -ge 2 ] || fail "--data-dir requires a value"
      data_dir=$2
      shift
      ;;
    --agent-bin)
      [ "$#" -ge 2 ] || fail "--agent-bin requires a value"
      agent_bin_arg=$2
      shift
      ;;
    --daemon-label)
      [ "$#" -ge 2 ] || fail "--daemon-label requires a value"
      daemon_label=$2
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
  shift
done

data_dir=$(canonical_directory "$data_dir") || fail "cannot resolve data directory: $data_dir"
home_dir=$(canonical_directory "$HOME") || fail "cannot resolve home directory: $HOME"
install_dir=${GESTA_AGENT_INSTALL_DIR:-"$data_dir/bin"}
agent_bin=${GESTA_AGENT_BIN:-"$install_dir/gesta-agent"}
if [ -n "$agent_bin_arg" ]; then
  agent_bin=$agent_bin_arg
  install_dir=$(dirname -- "$agent_bin")
fi
daemon_plist=${GESTA_DAEMON_PLIST:-"$launch_agents_dir/$daemon_label.plist"}
legacy_daemon_plist="$data_dir/$daemon_label.plist"
daemon_pid=${GESTA_DAEMON_PID:-"$data_dir/daemon.pid"}

case "$data_dir" in
  ""|/|"$home_dir")
    fail "refusing unsafe data directory: $data_dir"
    ;;
esac

if [ "$yes" != 1 ]; then
  if [ ! -r /dev/tty ]; then
    fail "confirmation requires a terminal; rerun with --yes"
  fi
  if [ "$keep_data" = 1 ]; then
    prompt="Uninstall Gesta Agent and keep local data at $data_dir? [y/N] "
  else
    prompt="Uninstall Gesta Agent and remove local data from $data_dir? [y/N] "
  fi
  printf '%s' "$prompt" >/dev/tty
  IFS= read -r answer </dev/tty || answer=
  case "$answer" in
    y|Y|yes|YES|Yes) ;;
    *)
      printf '%s\n' 'Uninstall cancelled.'
      exit 0
      ;;
  esac
fi

if [ -x "$agent_bin" ]; then
  printf '%s\n' 'Removing Gesta hooks...'
  run_as_target_user "$agent_bin" uninstall-hooks || fail "hook cleanup failed; installation was left in place"
else
  fail "agent binary not found at $agent_bin; reinstall the current agent, then rerun uninstall"
fi

if command -v launchctl >/dev/null 2>&1; then
  launchctl bootout "gui/$target_uid/$daemon_label" >/dev/null 2>&1 || \
    launchctl unload "$daemon_plist" >/dev/null 2>&1 || true
fi

if [ -f "$daemon_pid" ]; then
  pid=$(sed -n '1p' "$daemon_pid" 2>/dev/null || true)
  case "$pid" in
    ''|*[!0-9]*) ;;
    *)
      stop_agent_process "$pid"
      ;;
  esac
fi

if run_as_target_user "$agent_bin" capabilities 2>/dev/null | grep -qx 'deregister'; then
  printf '%s\n' 'Removing Gesta Agent from the control plane...'
  run_as_target_user "$agent_bin" deregister --data-dir "$data_dir" || \
    fail "control-plane removal failed; local files were kept so you can retry"
else
  printf '%s\n' 'WARNING: This older Agent cannot remove its server record; continuing with local uninstall.' >&2
fi

rm -f -- "$daemon_plist" "$legacy_daemon_plist" "$daemon_pid" "$agent_bin"

if [ "$keep_data" = 1 ]; then
  rmdir "$install_dir" 2>/dev/null || true
  printf 'Gesta Agent removed; local data kept at %s.\n' "$data_dir"
else
  rm -rf -- "$data_dir"
  printf '%s\n' 'Gesta Agent and local data removed.'
fi
