#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)
IMAGE="ktx-bench:opencode-1.18.4"
DEST="${DEST:-$ROOT/.bench-data/cache-evidence/opencode-runtime-controls-changed-20260724}"
STATE_ROOT="${OPENCODE_STATE_ROOT:-$DEST/state}"
CONFIG="$ROOT/tools/bench/evidence/opencode-runtime-cache-controls/probe-config.json"
MAX_INVOCATIONS=2
MAX_WALL_SECONDS=120

prompt() {
  local suffix="$1"
  python3 - "$suffix" <<'PY'
import sys
common = " ".join(f"synthetic-cache-control-token-{i:04d}" for i in range(700))
print(common)
print(f"Return exactly ACK-{sys.argv[1]}.")
PY
}

case "${1:-}" in
  --preflight)
    docker image inspect "$IMAGE" >/dev/null
    test -r "$CONFIG"
    test "${OPENCODE_API_KEY+x}" = x
    printf 'ready: network=none model_calls=0 max_invocations=%d max_model_steps=%d max_retries=0 max_spend_usd=0 max_wall_seconds=%d\n' \
      "$MAX_INVOCATIONS" "$MAX_INVOCATIONS" "$MAX_WALL_SECONDS"
    ;;
  --prompt-hashes)
    for suffix in A B; do
      printf 'prompt-%s ' "${suffix,,}"
      prompt "$suffix" | sha256sum | cut -d' ' -f1
    done
    ;;
  --live-changed)
    test "${KTX_CACHE_PROBE_LIVE:-}" = 1
    test "${OPENCODE_API_KEY+x}" = x
    test -n "${OPENCODE_SESSION_ID:-}"
    mkdir -p "$DEST/raw" "$STATE_ROOT/opencode" "$STATE_ROOT/runtime"
    started=$(date +%s)
    changed_prompt=$(prompt B)
    set +e
    timeout --signal=TERM "${MAX_WALL_SECONDS}s" docker run --rm --network bridge --read-only \
      --tmpfs /tmp:rw,noexec,nosuid,size=64m,uid=1000,gid=1000,mode=0700 \
      --tmpfs /home/node/.cache:rw,nosuid,size=256m,uid=1000,gid=1000,mode=0700 \
      --tmpfs /home/node/.config/opencode:rw,nosuid,size=64m,uid=1000,gid=1000,mode=0700 \
      -e OPENCODE_API_KEY -e OPENCODE_CONFIG_CONTENT="$(<"$CONFIG")" \
      -v "$STATE_ROOT/opencode:/home/node/.local/share/opencode:rw" \
      -v "$STATE_ROOT/runtime:/home/node/.local/state:rw" \
      -v "$ROOT:/work:ro" \
      --entrypoint /usr/local/bin/opencode "$IMAGE" run --format json \
      --model opencode/big-pickle --agent cache-probe --title runtime-cache-control-changed \
      --session "$OPENCODE_SESSION_ID" "$changed_prompt" \
      >"$DEST/raw/changed.jsonl" 2>"$DEST/raw/changed.stderr"
    status=$?
    set -e
    elapsed=$(($(date +%s) - started))
    printf 'invocations=1 status=%d elapsed_seconds=%d intentional_retries=0\n' "$status" "$elapsed" \
      >"$DEST/raw/accounting.txt"
    exit "$status"
    ;;
  *)
    printf 'usage: %s --preflight|--prompt-hashes|--live-changed\n' "$0" >&2
    exit 2
    ;;
esac
