#!/bin/sh
set -eu

ensure_stable_cwd() {
  if pwd >/dev/null 2>&1; then
    return
  fi
  if [ -n "${HOME:-}" ] && [ -d "$HOME" ] && cd "$HOME" 2>/dev/null; then
    return
  fi
  cd /tmp 2>/dev/null || cd /
}

ensure_stable_cwd

script_dir=
local_agent_root=
case "$0" in
  */*)
    script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || script_dir=
    if [ -n "$script_dir" ] && [ -f "$script_dir/../go.mod" ] && [ -d "$script_dir/../cmd" ]; then
      local_agent_root=$(CDPATH= cd -- "$script_dir/.." && pwd) || local_agent_root=
    fi
    ;;
esac

# --- output styling -----------------------------------------------------------
# Normal progress (ok/step/note/field) goes to stdout; diagnostics (warn/fail/
# enote) go to stderr. Color for each stream is decided independently so that
# redirecting either stream yields plain text on that stream: "... > log" keeps
# the log clean while warnings stay colored on the terminal, and "... 2> err"
# keeps err clean. Honors the NO_COLOR convention and TERM=dumb. This mirrors the
# Go install subcommand (output.go), which gates stdout/stderr the same way.
if [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  _green=$(printf '\033[32m'); _yellow=$(printf '\033[33m'); _red=$(printf '\033[31m')
  _cyan=$(printf '\033[36m'); _bold=$(printf '\033[1m'); _dim=$(printf '\033[2m')
  _reset=$(printf '\033[0m')
else
  _green= _yellow= _red= _cyan= _bold= _dim= _reset=
fi

if [ -t 1 ]; then
  c_reset=$_reset c_bold=$_bold c_dim=$_dim c_green=$_green c_cyan=$_cyan
else
  c_reset= c_bold= c_dim= c_green= c_cyan=
fi

if [ -t 2 ]; then
  e_reset=$_reset e_dim=$_dim e_yellow=$_yellow e_red=$_red
else
  e_reset= e_dim= e_yellow= e_red=
fi

ok()    { printf '%s✓%s %s\n' "$c_green" "$c_reset" "$*"; }
step()  { printf '%s→%s %s\n' "$c_cyan" "$c_reset" "$*"; }
note()  { printf '  %s%s%s\n' "$c_dim" "$*" "$c_reset"; }
field() { printf '  %s%-12s%s %s\n' "$c_dim" "$1" "$c_reset" "$2"; }
warn()  { printf '%s!%s %s\n' "$e_yellow" "$e_reset" "$*" >&2; }
fail()  { printf '%s✗%s %s\n' "$e_red" "$e_reset" "$*" >&2; }
enote() { printf '  %s%s%s\n' "$e_dim" "$*" "$e_reset" >&2; }

lookup_home_dir() {
  user_name=$1
  if command -v dscl >/dev/null 2>&1; then
    dscl . -read "/Users/$user_name" NFSHomeDirectory 2>/dev/null | awk '{print $2; exit}'
    return
  fi
  if command -v getent >/dev/null 2>&1; then
    getent passwd "$user_name" | awk -F: '{print $6; exit}'
    return
  fi
  printf '%s\n' ""
}

run_uid=$(id -u)
target_uid=$run_uid
target_gid=$(id -g)
target_user=${USER:-}
target_home=${HOME:-}
if [ "$run_uid" = "0" ] && [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER:-}" != "root" ]; then
  target_user=$SUDO_USER
  target_uid=${SUDO_UID:-$(id -u "$target_user" 2>/dev/null || printf '%s' "")}
  target_gid=${SUDO_GID:-$(id -g "$target_user" 2>/dev/null || printf '%s' "")}
  detected_home=$(lookup_home_dir "$target_user")
  if [ -n "$detected_home" ]; then
    target_home=$detected_home
    HOME=$target_home
    export HOME
  fi
  USER=$target_user
  LOGNAME=$target_user
  export USER LOGNAME
fi

external_agent_bin=0
control_url=${GESTA_CONTROL_URL:-}
api_key=${GESTA_API_KEY:-${GESTA_APIKEY:-}}
interval=${GESTA_DAEMON_INTERVAL:-1m}
usage_window=${GESTA_DAEMON_USAGE_WINDOW:-10m}
start_daemon=${GESTA_START_DAEMON:-auto}
daemon_label=${GESTA_DAEMON_LABEL:-com.gesta.agent}
data_dir=${GESTA_DAEMON_DATA_DIR:-${GESTA_DATA_DIR:-"$HOME/.gesta"}}
install_dir=${GESTA_AGENT_INSTALL_DIR:-"$data_dir/bin"}
agent_root=${local_agent_root:-"$data_dir/agent"}
agent_bin=${GESTA_AGENT_BIN:-"$install_dir/gesta-agent"}
install_base_url=${GESTA_AGENT_INSTALL_BASE_URL:-https://artifacts.gesta.run/gesta/agent/rc}
if [ -n "${GESTA_AGENT_BIN:-}" ]; then
  external_agent_bin=1
fi
launch_agents_dir=${GESTA_LAUNCH_AGENTS_DIR:-"$HOME/Library/LaunchAgents"}
daemon_plist=${GESTA_DAEMON_PLIST:-"$launch_agents_dir/$daemon_label.plist"}
legacy_daemon_plist="$data_dir/$daemon_label.plist"
daemon_pid=${GESTA_DAEMON_PID:-"$data_dir/daemon.pid"}
daemon_log=${GESTA_DAEMON_LOG:-"$data_dir/daemon.log"}
daemon_err_log=${GESTA_DAEMON_ERR_LOG:-"$data_dir/daemon.err.log"}

target_owner_spec() {
  if [ "$run_uid" != "0" ] || [ -z "$target_uid" ] || [ "$target_uid" = "0" ]; then
    printf '%s\n' ""
    return
  fi
  owner_spec=$target_uid
  if [ -n "$target_gid" ]; then
    owner_spec="$target_uid:$target_gid"
  fi
  printf '%s\n' "$owner_spec"
}

chown_target_user_path() {
  owner_spec=$(target_owner_spec)
  if [ -z "$owner_spec" ]; then
    return
  fi
  for path in "$@"; do
    if [ -e "$path" ]; then
      chown "$owner_spec" "$path" 2>/dev/null || true
    fi
  done
}

chown_target_user_files() {
  owner_spec=$(target_owner_spec)
  if [ -z "$owner_spec" ]; then
    return
  fi
  chown -R "$owner_spec" "$data_dir" 2>/dev/null || true
  if [ -d "$HOME/.codex" ]; then
    chown -R "$owner_spec" "$HOME/.codex" 2>/dev/null || true
  fi
  chown_target_user_path "$daemon_plist"
}

check_user_writable_paths() {
  if [ "$run_uid" = "0" ]; then
    return
  fi
  problem=0
  daemon_plist_dir=$(dirname -- "$daemon_plist")
  for path in "$data_dir" "$HOME/.codex" "$HOME/.codex/hooks.json" "$daemon_plist_dir" "$daemon_plist"; do
    if [ -e "$path" ] && [ ! -w "$path" ]; then
      fail "cannot write $path"
      problem=1
    fi
  done
  if [ "$problem" = "0" ]; then
    return
  fi
  warn "these files may be owned by root from a previous sudo install."
  printf '%s\n' "Fix ownership, then rerun the installer:" >&2
  enote "sudo chown -R \"\$(id -u):\$(id -g)\" $HOME/.codex $data_dir $daemon_plist_dir $daemon_plist"
  exit 1
}

prepare_install_permissions() {
  chown_target_user_files
  check_user_writable_paths
}

usage() {
  cat <<'USAGE'
Usage:
  ./scripts/install.sh --control-url http://127.0.0.1:8080 --apikey sk-...

Options:
  --apikey key                Required. API key saved for Gesta policy fetches.
  --control-url url           Required. Control API URL.
  --interval duration         Daemon collection interval shown in the run command. Default: 1m
  --usage-window duration     Token usage window saved in daemon config. Default: 10m
  --agent-bin path            Existing gesta-agent binary. Default: ~/.gesta/bin/gesta-agent
  --install-dir path          Directory for downloaded gesta-agent. Default: ~/.gesta/bin
  --base-url url              Installer asset base URL containing bin/gesta-agent-$os-$arch.
                              Default: GitHub Pages latest. Supported targets:
                              darwin/amd64, darwin/arm64, linux/amd64, linux/arm64.
  --daemon                    Start the background daemon after installing.
  --no-daemon                 Do not start the background daemon.
USAGE
}

download_file() {
  url=$1
  dest=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
    return
  fi
  fail "curl or wget is required to download gesta-agent"
  exit 2
}

normalize_os() {
  case "$1" in
    Darwin|darwin)
      printf '%s\n' darwin
      ;;
    Linux|linux)
      printf '%s\n' linux
      ;;
    *)
      fail "unsupported OS: $1"
      exit 2
      ;;
  esac
}

detect_os() {
  if [ -n "${GESTA_AGENT_OS:-}" ]; then
    normalize_os "$GESTA_AGENT_OS"
    return
  fi
  normalize_os "$(uname -s)"
}

normalize_arch() {
  case "$1" in
    arm64|aarch64)
      printf '%s\n' arm64
      ;;
    x86_64|amd64)
      printf '%s\n' amd64
      ;;
    *)
      fail "unsupported architecture: $1"
      exit 2
      ;;
  esac
}

detect_arch() {
  if [ -n "${GESTA_AGENT_ARCH:-}" ]; then
    normalize_arch "$GESTA_AGENT_ARCH"
    return
  fi
  normalize_arch "$(uname -m)"
}

sha256_of() {
  path=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print $1}'
    return
  fi
  fail "sha256sum or shasum is required to verify gesta-agent"
  exit 2
}

agent_version() {
  path=$1
  if [ -x "$path" ]; then
    "$path" version 2>/dev/null || printf '%s\n' "unknown"
    return
  fi
  printf '%s\n' "not installed"
}

download_agent_binary() {
  os=$(detect_os)
  arch=$(detect_arch)
  binary_name="gesta-agent-$os-$arch"
  base_url=${install_base_url%/}
  binary_url="$base_url/bin/$binary_name"
  checksums_url="$base_url/SHA256SUMS"
  tmp_bin="$agent_bin.tmp.$$"
  tmp_sums="$agent_bin.SHA256SUMS.$$"

  mkdir -p "$(dirname -- "$agent_bin")"
  step "Downloading $binary_name"
  field "platform" "$os/$arch"
  note "$binary_url"
  download_file "$binary_url" "$tmp_bin"
  if download_file "$checksums_url" "$tmp_sums"; then
    expected=$(awk -v name="bin/$binary_name" '$2 == name {print $1}' "$tmp_sums")
    rm -f "$tmp_sums"
    if [ -z "$expected" ]; then
      rm -f "$tmp_bin"
      fail "checksum entry missing for $binary_name in $checksums_url"
      exit 1
    fi
    actual=$(sha256_of "$tmp_bin")
    if [ "$actual" != "$expected" ]; then
      rm -f "$tmp_bin"
      fail "checksum mismatch for $binary_name"
      enote "expected: $expected"
      enote "actual:   $actual"
      exit 1
    fi
  else
    rm -f "$tmp_bin" "$tmp_sums"
    fail "failed to download checksums from $checksums_url"
    exit 1
  fi
  chmod 755 "$tmp_bin"
  mv "$tmp_bin" "$agent_bin"
  ok "Downloaded and verified checksum"
}

xml_escape() {
  printf '%s' "$1" | sed \
    -e 's/&/\&amp;/g' \
    -e 's/</\&lt;/g' \
    -e 's/>/\&gt;/g' \
    -e 's/"/\&quot;/g' \
    -e "s/'/\&apos;/g"
}

write_launchd_plist() {
  plist_dir=$(dirname -- "$daemon_plist")
  mkdir -p "$plist_dir" "$(dirname -- "$daemon_log")"
  {
    printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?>'
    printf '%s\n' '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
    printf '%s\n' '<plist version="1.0">'
    printf '%s\n' '<dict>'
    printf '%s\n' '  <key>Label</key>'
    printf '  <string>%s</string>\n' "$(xml_escape "$daemon_label")"
    printf '%s\n' '  <key>ProgramArguments</key>'
    printf '%s\n' '  <array>'
    printf '    <string>%s</string>\n' "$(xml_escape "$agent_bin")"
    printf '%s\n' '    <string>run</string>'
    printf '%s\n' '    <string>--control-url</string>'
    printf '    <string>%s</string>\n' "$(xml_escape "$control_url")"
    printf '%s\n' '    <string>--interval</string>'
    printf '    <string>%s</string>\n' "$(xml_escape "$interval")"
    printf '%s\n' '    <string>--usage-window</string>'
    printf '    <string>%s</string>\n' "$(xml_escape "$usage_window")"
    printf '%s\n' '  </array>'
    printf '%s\n' '  <key>WorkingDirectory</key>'
    printf '  <string>%s</string>\n' "$(xml_escape "$agent_root")"
    printf '%s\n' '  <key>EnvironmentVariables</key>'
    printf '%s\n' '  <dict>'
    printf '%s\n' '    <key>NO_PROXY</key>'
    printf '%s\n' '    <string>127.0.0.1,localhost</string>'
    printf '%s\n' '    <key>no_proxy</key>'
    printf '%s\n' '    <string>127.0.0.1,localhost</string>'
    printf '%s\n' '    <key>PATH</key>'
    printf '    <string>%s</string>\n' "$(xml_escape "$PATH")"
    printf '%s\n' '  </dict>'
    printf '%s\n' '  <key>RunAtLoad</key>'
    printf '%s\n' '  <true/>'
    printf '%s\n' '  <key>KeepAlive</key>'
    printf '%s\n' '  <true/>'
    printf '%s\n' '  <key>StandardOutPath</key>'
    printf '  <string>%s</string>\n' "$(xml_escape "$daemon_log")"
    printf '%s\n' '  <key>StandardErrorPath</key>'
    printf '  <string>%s</string>\n' "$(xml_escape "$daemon_err_log")"
    printf '%s\n' '</dict>'
    printf '%s\n' '</plist>'
  } >"$daemon_plist"
  chmod 644 "$daemon_plist" 2>/dev/null || true
  chown_target_user_path "$plist_dir" "$daemon_plist"
  if command -v plutil >/dev/null 2>&1; then
    plutil -lint "$daemon_plist" >/dev/null
  fi
}

print_daemon_check_hint() {
  if command -v pgrep >/dev/null 2>&1; then
    local_processes="$(pgrep -fl "gesta-agent run" 2>/dev/null || true)"
    if [ -n "$local_processes" ]; then
      field "running" "pid $(printf '%s' "$local_processes" | awk '{print $1; exit}')"
    fi
  fi
  field "check" 'pgrep -fl "gesta-agent run"'
}

start_daemon_background() {
  if [ "$run_uid" = "0" ] && [ -n "$target_uid" ] && [ "$target_uid" != "0" ]; then
    if command -v sudo >/dev/null 2>&1; then
      nohup sudo -u "#$target_uid" env \
        HOME="$HOME" USER="$target_user" LOGNAME="$target_user" \
        "$agent_bin" run --control-url "$control_url" --interval "$interval" --usage-window "$usage_window" \
        >"$daemon_log" 2>"$daemon_err_log" &
      printf '%s\n' "$!" >"$daemon_pid"
      return
    fi
    if [ -n "$target_user" ] && command -v runuser >/dev/null 2>&1; then
      nohup runuser -u "$target_user" -- env \
        HOME="$HOME" USER="$target_user" LOGNAME="$target_user" \
        "$agent_bin" run --control-url "$control_url" --interval "$interval" --usage-window "$usage_window" \
        >"$daemon_log" 2>"$daemon_err_log" &
      printf '%s\n' "$!" >"$daemon_pid"
      return
    fi
    warn "could not find sudo or runuser; daemon will run as root and may need one more reinstall to normalize ownership"
  fi

  nohup "$agent_bin" run --control-url "$control_url" --interval "$interval" --usage-window "$usage_window" >"$daemon_log" 2>"$daemon_err_log" &
  printf '%s\n' "$!" >"$daemon_pid"
}

start_daemon_service() {
  mkdir -p "$data_dir" "$agent_root" "$(dirname -- "$daemon_log")"
  if [ "$(uname -s)" = "Darwin" ] && command -v launchctl >/dev/null 2>&1; then
    write_launchd_plist
    chown_target_user_files
    launchctl bootout "gui/$target_uid/$daemon_label" >/dev/null 2>&1 || true
    launchctl bootout "gui/$target_uid" "$daemon_plist" >/dev/null 2>&1 || true
    if [ "$legacy_daemon_plist" != "$daemon_plist" ] && [ -f "$legacy_daemon_plist" ]; then
      launchctl bootout "gui/$target_uid" "$legacy_daemon_plist" >/dev/null 2>&1 || true
    fi
    launchctl enable "gui/$target_uid/$daemon_label" >/dev/null 2>&1 || true
    launchctl bootstrap "gui/$target_uid" "$daemon_plist"
    launchctl kickstart -k "gui/$target_uid/$daemon_label" >/dev/null 2>&1 || true
    if [ "$legacy_daemon_plist" != "$daemon_plist" ] && [ -f "$legacy_daemon_plist" ]; then
      rm -f "$legacy_daemon_plist" >/dev/null 2>&1 || true
    fi
    ok "Daemon service started ($daemon_label)"
    field "plist" "$daemon_plist"
    print_daemon_check_hint
    note "launch command uses saved config (no --apikey)"
    return
  fi

  if [ -f "$daemon_pid" ] && kill -0 "$(cat "$daemon_pid")" >/dev/null 2>&1; then
    kill "$(cat "$daemon_pid")" >/dev/null 2>&1 || true
  fi
  : >"$daemon_log"
  : >"$daemon_err_log"
  chown_target_user_files
  start_daemon_background
  ok "Daemon process started (pid $(cat "$daemon_pid"))"
  print_daemon_check_hint
  note "launch command uses saved config (no --apikey)"
}

list_active_ai_clients() {
  client_os=$1
  process_table=$(ps -ax -ww -o pid= -o uid= -o tty= -o args= 2>/dev/null) || return 1
  printf '%s\n' "$process_table" | awk -v uid="$target_uid" -v os="$client_os" '
    $2 == uid {
      tty = $3
      executable = $4
      name = executable
      sub(/^.*\//, "", name)
      if (os == "darwin" && executable ~ /\/ChatGPT\.app\/Contents\/MacOS\/ChatGPT$/)
        print $1, "desktop"
      else if (tty != "?" && tty != "??" && tty != "-" && name == "codex")
        print $1, "codex"
      else if (tty != "?" && tty != "??" && tty != "-" && name == "claude")
        print $1, "claude"
    }
  '
}

client_pids_named() {
  process_name=$1
  printf '%s\n' "$active_ai_clients" | awk -v name="$process_name" '$2 == name {print $1}'
}

show_active_ai_clients() {
  if [ -n "$codex_desktop_pids" ]; then
    field "running" "Codex Desktop"
  fi
  if [ -n "$codex_cli_pids" ]; then
    field "running" "Codex CLI"
  fi
  if [ -n "$claude_cli_pids" ]; then
    field "running" "Claude Code CLI"
  fi
}

client_restart_answer_is_yes() {
  case "$1" in
    [yY]|[yY][eE][sS])
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

confirm_client_restart() {
  restart_tty=${1:-/dev/tty}
  if ! printf '%s' "Restart active AI clients now to load the new Gesta hooks? [y/N] " 2>/dev/null >"$restart_tty"; then
    return 1
  fi
  IFS= read -r answer <"$restart_tty" 2>/dev/null || answer=
  client_restart_answer_is_yes "$answer"
}

schedule_client_restart() {
  codex_desktop_pids=$1
  shift
  if [ -n "$codex_desktop_pids" ] && \
    { ! command -v osascript >/dev/null 2>&1 || ! command -v open >/dev/null 2>&1; }; then
    return 1
  fi
  restart_helper=$(mktemp "$data_dir/.restart-clients.XXXXXX") || return 1
  if ! (umask 077 && cat >"$restart_helper") <<'HELPER'
#!/bin/sh
codex_desktop_pids=$1
shift
sleep 2
for pid in "$@"; do
  process_name=$(ps -p "$pid" -ww -o tty= -o args= 2>/dev/null | awk '
    NR == 1 {
      tty = $1
      executable = $2
      name = executable
      sub(/^.*\//, "", name)
      if (tty != "?" && tty != "??" && tty != "-" && (name == "codex" || name == "claude"))
        print name
    }
  ')
  case "$process_name" in
    codex|claude)
      kill -TERM "$pid" >/dev/null 2>&1 || true
      ;;
  esac
done
if [ -n "$codex_desktop_pids" ]; then
  osascript -e 'tell application "ChatGPT" to quit' >/dev/null 2>&1 || true
  attempts=0
  desktop_running=1
  while [ "$attempts" -lt 10 ]; do
    desktop_running=0
    for pid in $codex_desktop_pids; do
      if kill -0 "$pid" 2>/dev/null; then
        desktop_running=1
        break
      fi
    done
    [ "$desktop_running" -eq 0 ] && break
    attempts=$((attempts + 1))
    sleep 1
  done
  if [ "$desktop_running" -eq 0 ]; then
    open -a "ChatGPT" >/dev/null 2>&1 || true
  fi
fi
rm -f -- "$0"
HELPER
  then
    rm -f "$restart_helper"
    return 1
  fi
  if ! chmod 700 "$restart_helper"; then
    rm -f "$restart_helper"
    return 1
  fi
  chown_target_user_path "$restart_helper"

  if ! command -v nohup >/dev/null 2>&1; then
    rm -f "$restart_helper"
    return 1
  fi
  if [ "$run_uid" = "0" ] && [ -n "$target_uid" ] && [ "$target_uid" != "0" ]; then
    if command -v sudo >/dev/null 2>&1; then
      nohup sudo -u "#$target_uid" env HOME="$HOME" /bin/sh "$restart_helper" "$codex_desktop_pids" "$@" \
        </dev/null >/dev/null 2>&1 &
      return 0
    fi
    if [ -n "$target_user" ] && command -v runuser >/dev/null 2>&1; then
      nohup runuser -u "$target_user" -- env HOME="$HOME" /bin/sh "$restart_helper" "$codex_desktop_pids" "$@" \
        </dev/null >/dev/null 2>&1 &
      return 0
    fi
  else
    nohup /bin/sh "$restart_helper" "$codex_desktop_pids" "$@" </dev/null >/dev/null 2>&1 &
    return 0
  fi

  rm -f "$restart_helper"
  return 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --apikey|--api-key)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        printf '%s requires a value\n' "$1" >&2
        exit 2
      fi
      api_key=$2
      shift 2
      ;;
    --control-url)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        printf '%s requires a value\n' "$1" >&2
        exit 2
      fi
      control_url=$2
      shift 2
      ;;
    --interval)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        printf '%s requires a value\n' "$1" >&2
        exit 2
      fi
      interval=$2
      shift 2
      ;;
    --usage-window)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        printf '%s requires a value\n' "$1" >&2
        exit 2
      fi
      usage_window=$2
      shift 2
      ;;
    --agent-bin)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        printf '%s requires a value\n' "$1" >&2
        exit 2
      fi
      agent_bin=$2
      external_agent_bin=1
      shift 2
      ;;
    --install-dir)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        printf '%s requires a value\n' "$1" >&2
        exit 2
      fi
      install_dir=$2
      if [ "$external_agent_bin" = "0" ]; then
        agent_bin="$install_dir/gesta-agent"
      fi
      shift 2
      ;;
    --base-url)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        printf '%s requires a value\n' "$1" >&2
        exit 2
      fi
      install_base_url=$2
      shift 2
      ;;
    --daemon)
      start_daemon=1
      shift
      ;;
    --no-daemon)
      start_daemon=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      usage >&2
      exit 2
      ;;
  esac
done

if [ -z "$control_url" ]; then
  fail "missing required --control-url"
  usage >&2
  exit 2
fi

if [ -z "$api_key" ]; then
  fail "missing required --apikey"
  usage >&2
  exit 2
fi

printf '\n%s%sGesta%s%s agent installer%s\n' "$c_bold" "$c_cyan" "$c_reset" "$c_bold" "$c_reset"
field "control" "$control_url"
printf '\n'

prepare_install_permissions

if [ "$external_agent_bin" = "1" ] && [ ! -x "$agent_bin" ]; then
  fail "agent binary is not executable: $agent_bin"
  exit 2
fi

previous_agent_version=$(agent_version "$agent_bin")

if [ "$external_agent_bin" = "0" ]; then
  if [ -n "$local_agent_root" ] && command -v go >/dev/null 2>&1; then
    mkdir -p "$(dirname -- "$agent_bin")"
    step "Building agent from source"
    (cd "$local_agent_root" && go build -o "$agent_bin" ./cmd/gesta-agent)
    ok "Built $(agent_version "$agent_bin")"
  elif [ -n "$local_agent_root" ] && [ -x "$agent_bin" ]; then
    warn "go not found; reusing existing local agent binary at $agent_bin"
  else
    download_agent_binary
  fi
fi

"$agent_bin" install --agent-bin "$agent_bin" --control-url "$control_url" --apikey "$api_key" --usage-window "$usage_window"
chown_target_user_files
installed_agent_version=$(agent_version "$agent_bin")

if [ "$start_daemon" = "auto" ]; then
  start_daemon=1
fi

printf '\n'
ok "${c_bold}Agent integration installed${c_reset}"
field "version" "$installed_agent_version"
if [ "$previous_agent_version" != "not installed" ] && [ "$previous_agent_version" != "$installed_agent_version" ]; then
  field "upgraded" "$previous_agent_version → $installed_agent_version"
fi
field "control" "$control_url"
field "api key" "$api_key"
printf '\n'

if [ "$start_daemon" = "1" ]; then
  start_daemon_service
else
  warn "Daemon not started."
fi

printf '\n'
printf '  %sManual foreground run command:%s\n' "$c_dim" "$c_reset"
note "$agent_bin run --control-url $control_url --apikey $api_key --interval $interval --usage-window $usage_window"

printf '\n'
client_os=$(detect_os)
client_detection_ok=1
if ! active_ai_clients=$(list_active_ai_clients "$client_os"); then
  active_ai_clients=
  client_detection_ok=0
fi
codex_desktop_pids=$(client_pids_named desktop)
codex_cli_pids=$(client_pids_named codex)
claude_cli_pids=$(client_pids_named claude)

if [ "$client_detection_ok" = "0" ]; then
  warn "Could not inspect active AI clients; leaving them running."
  step "Restart Codex Desktop or start new Codex CLI / Claude Code sessions later."
elif [ -n "$active_ai_clients" ]; then
  show_active_ai_clients
  if [ -n "$codex_desktop_pids" ] && { [ -n "$codex_cli_pids" ] || [ -n "$claude_cli_pids" ]; }; then
    note "Codex Desktop will reopen; interactive CLI sessions will close and must be started again."
  elif [ -n "$codex_desktop_pids" ]; then
    note "Codex Desktop will reopen after it closes."
  else
    note "Interactive CLI sessions will close and must be started again."
  fi
  if confirm_client_restart; then
    cli_pids=$(printf '%s\n%s\n' "$codex_cli_pids" "$claude_cli_pids" | awk 'NF')
    # Word splitting is intentional: each detected numeric PID is a helper argument.
    # shellcheck disable=SC2086
    if schedule_client_restart "$codex_desktop_pids" $cli_pids; then
      if [ -n "$codex_desktop_pids" ] && [ -n "$cli_pids" ]; then
        ok "Client restart scheduled. Run codex or claude again for new CLI sessions."
      elif [ -n "$codex_desktop_pids" ]; then
        ok "Codex Desktop restart scheduled."
      else
        ok "Interactive CLI shutdown scheduled. Run codex or claude again for new sessions."
      fi
    else
      warn "Could not schedule client restart; restart clients manually."
    fi
  else
    if [ -n "$codex_desktop_pids" ] && { [ -n "$codex_cli_pids" ] || [ -n "$claude_cli_pids" ]; }; then
      step "Restart Codex Desktop and start new Codex CLI / Claude Code sessions later."
    elif [ -n "$codex_desktop_pids" ]; then
      step "Restart Codex Desktop later."
    else
      step "Start new Codex CLI / Claude Code sessions later."
    fi
  fi
else
  ok "No active AI clients need to be restarted."
fi
