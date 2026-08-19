#!/bin/sh
# shellcheck disable=SC2034,SC2154
set -eu

repo_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
temporary_dir=$(mktemp -d)
test_desktop_pid=
test_background_pid=

cleanup() {
  if [ -n "$test_desktop_pid" ]; then
    kill "$test_desktop_pid" >/dev/null 2>&1 || true
    wait "$test_desktop_pid" 2>/dev/null || true
  fi
  if [ -n "$test_background_pid" ]; then
    kill "$test_background_pid" >/dev/null 2>&1 || true
    wait "$test_background_pid" 2>/dev/null || true
  fi
  rm -rf "$temporary_dir"
}

trap cleanup EXIT HUP INT TERM

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

functions_file="$temporary_dir/install-functions.sh"
awk '
  /^list_active_ai_clients\(\)/ { capture = 1 }
  capture && /^while \[ "\$#" -gt 0 \]; do/ { exit }
  capture { print }
' "$repo_root/scripts/install.sh" >"$functions_file"

target_uid=501
target_user=tester
run_uid=501
data_dir="$temporary_dir/data"
mkdir -p "$data_dir"

field() { :; }
note() { :; }
warn() { :; }
chown_target_user_path() { :; }

# shellcheck source=/dev/null
. "$functions_file"

fake_bin="$temporary_dir/bin"
mkdir -p "$fake_bin"
process_list="$temporary_dir/processes"
restart_log="$temporary_dir/restart.log"

cat >"$process_list" <<'EOF'
100 501 ?? /Applications/ChatGPT.app/Contents/MacOS/ChatGPT
101 501 ?? /Applications/ChatGPT.app/Contents/Resources/codex app-server
102 501 ttys001 /Applications/ChatGPT.app/Contents/Resources/codex
103 501 pts/2 /usr/local/bin/claude
104 501 ? /usr/local/bin/codex exec --json
105 502 pts/3 /usr/local/bin/codex
EOF

cat >"$fake_bin/ps" <<'EOF'
#!/bin/sh
if [ "$1" = "-ax" ]; then
  [ "${FAKE_PS_FAIL:-0}" = "0" ] || exit 1
  cat "$FAKE_PS_LIST"
elif [ "$1" = "-p" ] && [ "$2" = "424242" ]; then
  echo "pts/1 /usr/local/bin/claude"
elif [ "$1" = "-p" ] && [ -n "${FAKE_BACKGROUND_PID:-}" ] && [ "$2" = "$FAKE_BACKGROUND_PID" ]; then
  echo "? /usr/local/bin/claude"
else
  exit 1
fi
EOF

cat >"$fake_bin/nohup" <<'EOF'
#!/bin/sh
exec "$@"
EOF

cat >"$fake_bin/osascript" <<'EOF'
#!/bin/sh
echo "osascript:$*" >>"$RESTART_LOG"
EOF

cat >"$fake_bin/open" <<'EOF'
#!/bin/sh
echo "open:$*" >>"$RESTART_LOG"
EOF

cat >"$fake_bin/sleep" <<'EOF'
#!/bin/sh
exit 0
EOF

chmod +x "$fake_bin/ps" "$fake_bin/nohup" "$fake_bin/osascript" "$fake_bin/open" "$fake_bin/sleep"
PATH="$fake_bin:$PATH"
export PATH
FAKE_PS_LIST="$process_list"
RESTART_LOG="$restart_log"
export FAKE_PS_LIST RESTART_LOG

actual_clients=$(list_active_ai_clients darwin)
expected_clients=$(printf '%s\n' '100 desktop' '102 codex' '103 claude')
[ "$actual_clients" = "$expected_clients" ] || fail "unexpected client detection: $actual_clients"

actual_linux_clients=$(list_active_ai_clients linux)
expected_linux_clients=$(printf '%s\n' '102 codex' '103 claude')
[ "$actual_linux_clients" = "$expected_linux_clients" ] || fail "unexpected Linux client detection: $actual_linux_clients"

FAKE_PS_FAIL=1
export FAKE_PS_FAIL
if list_active_ai_clients darwin >/dev/null; then
  fail "process inspection failure was not reported"
fi
FAKE_PS_FAIL=0
export FAKE_PS_FAIL

if confirm_client_restart "$temporary_dir/missing/tty"; then
  fail "missing control terminal was accepted"
fi
client_restart_answer_is_yes y || fail "lowercase confirmation was rejected"
client_restart_answer_is_yes YES || fail "uppercase confirmation was rejected"
client_restart_answer_is_yes Yes || fail "mixed-case confirmation was rejected"
if client_restart_answer_is_yes no; then
  fail "negative confirmation was accepted"
fi

schedule_client_restart "99999999" 424242 || fail "restart scheduling failed"
restart_helper_path=$restart_helper

attempt=0
while ! grep -F 'open:-a ChatGPT' "$restart_log" >/dev/null 2>&1 && [ "$attempt" -lt 100 ]; do
  attempt=$((attempt + 1))
  /bin/sleep 0.01
done

[ -f "$restart_log" ] || fail "restart helper did not run"
grep -F 'tell application "ChatGPT" to quit' "$restart_log" >/dev/null || fail "ChatGPT quit was not requested"
grep -F 'open:-a ChatGPT' "$restart_log" >/dev/null || fail "ChatGPT was not reopened"

attempt=0
while [ -e "$restart_helper_path" ] && [ "$attempt" -lt 100 ]; do
  attempt=$((attempt + 1))
  /bin/sleep 0.01
done
[ ! -e "$restart_helper_path" ] || fail "restart helper did not exit"

timeout_log="$temporary_dir/restart-timeout.log"
RESTART_LOG="$timeout_log"
export RESTART_LOG
/bin/sleep 30 &
test_desktop_pid=$!
schedule_client_restart "$test_desktop_pid" || fail "timeout restart scheduling failed"
restart_helper_path=$restart_helper

attempt=0
while [ -e "$restart_helper_path" ] && [ "$attempt" -lt 100 ]; do
  attempt=$((attempt + 1))
  /bin/sleep 0.01
done
[ ! -e "$restart_helper_path" ] || fail "timeout restart helper did not exit"
grep -F 'tell application "ChatGPT" to quit' "$timeout_log" >/dev/null || fail "timeout quit was not requested"
if grep -F 'open:-a ChatGPT' "$timeout_log" >/dev/null; then
  fail "ChatGPT reopened before the original process exited"
fi

kill "$test_desktop_pid" >/dev/null 2>&1 || true
wait "$test_desktop_pid" 2>/dev/null || true
test_desktop_pid=

/bin/sleep 30 &
test_background_pid=$!
FAKE_BACKGROUND_PID=$test_background_pid
export FAKE_BACKGROUND_PID
schedule_client_restart "" "$test_background_pid" || fail "background-process scheduling failed"
restart_helper_path=$restart_helper

attempt=0
while [ -e "$restart_helper_path" ] && [ "$attempt" -lt 100 ]; do
  attempt=$((attempt + 1))
  /bin/sleep 0.01
done
[ ! -e "$restart_helper_path" ] || fail "background-process helper did not exit"
kill -0 "$test_background_pid" 2>/dev/null || fail "background CLI process was terminated"

kill "$test_background_pid" >/dev/null 2>&1 || true
wait "$test_background_pid" 2>/dev/null || true
test_background_pid=

echo "Installer client restart tests passed."
