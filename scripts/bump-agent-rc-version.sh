#!/usr/bin/env bash
set -euo pipefail

repo_root="${1:-$(git rev-parse --show-toplevel)}"
contract="$repo_root/contracts/daemon.json"

current="$(python3 - "$contract" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
print(json.loads(path.read_text())["daemon_version"])
PY
)"

if [[ ! "$current" =~ ^([0-9]+\.[0-9]+\.[0-9]+-rc)([0-9]+)$ ]]; then
  echo "daemon_version must use rc format, got: $current" >&2
  exit 1
fi

next="${BASH_REMATCH[1]}$((BASH_REMATCH[2] + 1))"

python3 - "$contract" "$next" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
next_version = sys.argv[2]
payload = json.loads(path.read_text())
payload["daemon_version"] = next_version
path.write_text(json.dumps(payload, indent=2) + "\n")
PY

"$repo_root/scripts/generate-contracts.py"

printf '%s\n' "$next"
