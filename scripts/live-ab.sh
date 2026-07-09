#!/usr/bin/env bash
#
# scripts/live-ab.sh
#
# Live A/B compression comparison: runs the same prompt through opencode
# in a Docker container with two different ktx.toml configs (baseline
# passthrough vs treatment compression) and tabulates cost/score deltas.
#
# Requires: Docker, ktx-bench image (build with: docker build -f
# Dockerfile.benchmark -t ktx-bench .)
#
# Usage:
#   scripts/live-ab.sh -m <model> -p "<prompt>" [-r <rounds>] [--build]
#
# Example:
#   scripts/live-ab.sh \
#     -m neuralwatt/glm-5.2-short-fast-flex \
#     -p "Read /work/README.md and /work/ARCHITECTURE.md, then respond: done" \
#     -r 3

set -euo pipefail

MODEL=""
PROMPT=""
ROUNDS=1
BUILD=false
IMAGE="ktx-bench"
AUTH="$HOME/.local/share/opencode/auth.json"

while [ $# -gt 0 ]; do
  case "$1" in
    -m|--model)  MODEL="$2";      shift 2 ;;
    -p|--prompt) PROMPT="$2";     shift 2 ;;
    -r|--rounds) ROUNDS="$2";     shift 2 ;;
    --build)     BUILD=true;      shift ;;
    -h|--help)
      echo "Usage: $0 -m <model> -p \"<prompt>\" [-r <rounds>] [--build]"
      exit 0
      ;;
    *) echo "Unknown flag: $1" >&2; exit 1 ;;
  esac
done

if [ -z "$MODEL" ] || [ -z "$PROMPT" ]; then
  echo "error: -m <model> and -p \"<prompt>\" are required" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BENCH_DIR="$REPO_ROOT/tools/bench"
DATA_DIR="${KTX_BENCH_DATA:-$REPO_ROOT/.bench-data}"

if [ "$BUILD" = true ]; then
  echo "Building image $IMAGE..." >&2
  docker build -f "$REPO_ROOT/Dockerfile.benchmark" -t "$IMAGE" "$REPO_ROOT"
fi

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "error: image $IMAGE not found. Build with: $0 --build" >&2
  exit 1
fi

if [ ! -f "$AUTH" ]; then
  echo "error: opencode auth not found at $AUTH" >&2
  exit 1
fi

mkdir -p "$DATA_DIR/baseline" "$DATA_DIR/treatment"

run_arm() {
  local arm="$1"  # baseline or treatment
  local config="$BENCH_DIR/ktx.toml.$arm"
  local arm_dir="$DATA_DIR/$arm"
  local results="[]"

  echo "=== $arm (rounds=$ROUNDS) ===" >&2

  for round in $(seq 1 "$ROUNDS"); do
    local outfile="$arm_dir/round-${round}.jsonl"
    echo "  round $round..." >&2

    docker run --rm \
      -v "$AUTH:/home/node/.local/share/opencode/auth.json:ro" \
      -v "$config:/home/node/.config/ktx/config.toml:ro" \
      -v "$arm_dir:/home/node/.local/share/ktx:rw" \
      -v "$REPO_ROOT:/work:rw" \
      "$IMAGE" \
      -m "$MODEL" "$PROMPT" > "$outfile" 2>/dev/null || {
      echo "  warning: round $round failed" >&2
      continue
    }

    # Parse the JSONL for cost/score
    local parsed
    parsed=$(parse_jsonl "$outfile" "$arm" "$round")

    if [ -n "$parsed" ]; then
      results=$(echo "$results" | jq -c --argjson entry "$parsed" '. + [$entry]')
    fi
  done

  echo "$results"
}

parse_jsonl() {
  local file="$1" arm="$2" round="$3"
  python3 -c "
import json, sys
total_in, total_out, total_cost = 0, 0, 0.0
answer = ''
tools = 0
complete = False
with open('$file') as f:
    for line in f:
        line = line.strip()
        if not line: continue
        d = json.loads(line)
        t = d.get('type', '')
        if t == 'step_finish':
            p = d.get('part', {})
            total_in += p['tokens']['input']
            total_out += p['tokens']['output']
            total_cost += p['cost']
            if p.get('reason') == 'stop':
                complete = True
        elif t == 'text':
            answer += d.get('part', {}).get('text', '')
        elif t == 'tool_use':
            tools += 1
score = 1 if answer.strip() == 'done' else 0
entry = {
    'arm': '$arm',
    'round': $round,
    'tokens_in': total_in,
    'tokens_out': total_out,
    'cost': total_cost,
    'score': score,
    'tools': tools,
    'complete': complete,
    'answer': answer.strip()[:50],
}
print(json.dumps(entry))
" 2>/dev/null
}

print_table() {
  local results="$1"

  printf '%-10s %6s %10s %10s %12s %6s %6s %8s\n' \
    "arm" "round" "tokens_in" "tokens_out" "cost" "score" "tools" "complete"
  printf '%-10s %6s %10s %10s %12s %6s %6s %8s\n' \
    "---" "-----" "---------" "----------" "----" "-----" "-----" "--------"

  echo "$results" | jq -r '.[] | [
    .arm,
    (.round | tostring),
    (.tokens_in | tostring),
    (.tokens_out | tostring),
    (.cost | . * 1000000 | round / 1000000 | tostring),
    (.score | tostring),
    (.tools | tostring),
    (if .complete then "yes" else "no" end)
  ] | @tsv' | while IFS=$'\t' read -r arm round tin tout cost score tools complete; do
    printf '%-10s %6s %10s %10s %12s %6s %6s %8s\n' \
      "$arm" "$round" "$tin" "$tout" "\$$cost" "$score" "$tools" "$complete"
  done

  # Summary per arm
  echo ""
  echo "$results" | jq -r '
    group_by(.arm) | .[] |
    .[0].arm as $arm |
    [ .[] | select(.complete == true) ] as $complete |
    (if ($complete | length) > 0 then
      {
        arm: $arm,
        n: ($complete | length),
        avg_tokens_in: (($complete | map(.tokens_in) | add) / ($complete | length) | floor),
        avg_cost: (($complete | map(.cost) | add) / ($complete | length)),
        pass_rate: (($complete | map(select(.score == 1)) | length) / ($complete | length))
      }
    else
      {arm: $arm, n: 0, avg_tokens_in: 0, avg_cost: 0, pass_rate: 0}
    end)
  ' | while IFS= read -r summary; do
    echo "$summary" | jq -r '"\(.arm): N=\(.n) avg_tokens_in=\(.avg_tokens_in) avg_cost=\(.avg_cost*1000000|round/1000000) pass_rate=\(.pass_rate*100|floor)%"'
  done
}

BASELINE_RESULTS=$(run_arm "baseline")
TREATMENT_RESULTS=$(run_arm "treatment")

ALL_RESULTS=$(echo "[$BASELINE_RESULTS, $TREATMENT_RESULTS]" | jq -c 'flatten')

echo ""
print_table "$ALL_RESULTS"
