#!/usr/bin/env bash
#
# scripts/validate-bench.sh
#
# Wrapper Capture Validation Gate.
#
# Reads the methodology-pinned inputs from tools/bench/methodology-v1.yaml,
# invokes `ktx run opencode run --format json -m <provider/id> <prompt>`
# against real opencode, then emits five per-layer [PASS]/[FAIL] verdicts
# per the contract:
#
#   L1 session-capture      ktx list shows exactly one new session after the
#                            wrapped invocation.
#                            Evidence source: ktx list --format json.
#
#   L2 stream-token-        on the captured JSONL stream that `ktx run` teed
#       evidence             to the script's stdout, at least one line with
#                            type=step_finish has part.tokens.input > 0 AND
#                            part.tokens.output > 0.
#                            Does NOT assert tokens reached tool_call rows
#                            (NULL-by-design for OpenCode 1.17.8 tool_use
#                            events; see internal/parser/parser.go:150-158
#                            'Surprises').
#                            Evidence source: captured stdout.
#
#   L3 stream-cost-         on the same captured stream, either:
#       evidence              (a) at least one type=step_finish line carries
#                               part.cost as a number (provider-reported
#                                 cost non-NULL in the stream), OR
#                            (b) derived cost is reconstructable from the
#                                 captured step_finish tokens plus the
#                                 methodology-pinned model identifier
#                                 against the ktx prices cache
#                                 ($XDG_DATA_HOME/ktx/prices.json), with
#                                 script-computed derived cost > 0.
#                                 Cache-miss is treated as a real L3 FAIL
#                                 under the environmental-failure policy.
#                            Evidence source: captured stdout + ktx prices cache.
#
#   L4 parser-1-to-1        the count of type=tool_use lines in the captured
#                            JSONL stream equals the session's tool_call_count
#                            field returned by ktx list / ktx get (parser
#                            saw every tool_use event and persisted it; no
#                            silent drops; this layer is the stream <->
#                            persistence bridge).
#                            Evidence source: captured stdout + ktx list.
#
#   L5 opencode-answer       the final type=text event's part.text field,
#       match                 after whitespace trim, equals the methodology
#                            task.expected_output ('4') exactly (score=1.0).
#                            Evidence source: captured stdout.
#
# Exit codes:
#   0   all-PASS (final stderr line: 'validation=PASS')
#   1   any-FAIL  (final stderr line: 'validation=FAIL [<comma-separated failing layers>]')
#   2   usage error
#
# Constraints: must NOT modify ktx source under internal/ or cmd/;
#   only this script file is created. ktx.db is accessed ONLY via the public
#   `ktx list` / `ktx get` CLI surface; no direct SQLite access.
#
# Prerequisites assumed present (the script does NOT validate them): opencode
#   binary on PATH; neuralwatt API credentials for opencode with the pinned
#   model reachable; `ktx prices` cache populated for the pinned model (else
#   L3 will FAIL on cache-miss per the environmental-failure policy, which
#   is the actionable signal the gate is meant to surface — not bypassed).
#
# Usage:
#   scripts/validate-bench.sh [--methodology <path>]
#
# Defaults:
#   --methodology  tools/bench/methodology-v1.yaml
#
# Test mode (no API call):
#   KTX_VALIDATE_DRY_RUN=1 scripts/validate-bench.sh
#   Reads static JSONL from KTX_VALIDATE_STREAM_FILE (if set) or emits all
#   verdicts as [FAIL] with reason 'dry-run'; exits 1. For parser/pipeline
#   integration checks without burning a real API call.

set -uo pipefail

METHODOLOGY_PATH="tools/bench/methodology-v1.yaml"
while [ $# -gt 0 ]; do
    case "$1" in
        --methodology)
            [ $# -ge 2 ] || { printf 'usage: --methodology requires a value\n' >&2; exit 2; }
            METHODOLOGY_PATH="$2"
            shift 2
            ;;
        -h|--help)
            sed -n '1,80p' "$0" | sed 's/^# \{0,1\}//' >&2
            exit 0
            ;;
        *)
            printf 'usage: unknown argument %q\n' "$1" >&2
            exit 2
            ;;
    esac
done

[ -f "$METHODOLOGY_PATH" ] || {
    printf '[validate] fatal: methodology file not found: %s\n' "$METHODOLOGY_PATH" >&2
    exit 2
}

KTX_BIN="${KTX_BIN:-$(command -v ktx 2>/dev/null || true)}"
OPENCODE_BIN="${OPENCODE_BIN:-$(command -v opencode 2>/dev/null || true)}"
[ -n "$KTX_BIN" ] && [ -x "$KTX_BIN" ] || { printf '[validate] fatal: ktx not found\n' >&2; exit 2; }
[ -n "$OPENCODE_BIN" ] && [ -x "$OPENCODE_BIN" ] || { printf '[validate] fatal: opencode not found\n' >&2; exit 2; }
command -v jq         >/dev/null || { printf '[validate] fatal: jq not on PATH\n' >&2; exit 2; }
command -v python3    >/dev/null || { printf '[validate] fatal: python3 not on PATH\n' >&2; exit 2; }

STDOUT_LOG="$(mktemp -t ktx-validate.stdout.XXXXXX)"
STDERR_LOG="$(mktemp -t ktx-validate.stderr.XXXXXX)"
SESSIONS_BEFORE_LOG="$(mktemp -t ktx-validate.sessions-before.XXXXXX)"
SESSIONS_AFTER_LOG="$(mktemp -t ktx-validate.sessions-after.XXXXXX)"
trap 'rm -f "$STDOUT_LOG" "$STDERR_LOG" "$SESSIONS_BEFORE_LOG" "$SESSIONS_AFTER_LOG"' EXIT

log()   { printf '[validate] %s\n' "$*" >&2; }
emit()  { printf '%s %s\n' "$1" "$2" >&2; }   # emit "<verdict>" "<layer>: <reason>"

# ---------------------------------------------------------------------------
# Read the scalar methodology inputs without third-party Python packages.
# ---------------------------------------------------------------------------
mapfile -t _INPUTS < <(python3 - "$METHODOLOGY_PATH" <<'PY'
import sys

values = {}
parents = []
for raw in open(sys.argv[1], encoding="utf-8"):
    raw = raw.split("#", 1)[0].rstrip()
    if not raw.strip() or ":" not in raw:
        continue
    depth = (len(raw) - len(raw.lstrip(" "))) // 2
    parents = parents[:depth]
    key, value = (part.strip() for part in raw.strip().split(":", 1))
    if not value:
        parents.append(key)
        continue
    values[".".join([*parents, key])] = value.strip('"\'')

for key in (
    "inputs.model.provider",
    "inputs.model.id",
    "inputs.task.prompt",
    "inputs.task.expected_output",
):
    print(values[key])
PY
)
MODEL_PROVIDER="${_INPUTS[0]}"
MODEL_ID="${_INPUTS[1]}"
TASK_PROMPT="${_INPUTS[2]}"
EXPECTED_OUTPUT="${_INPUTS[3]}"
MODEL_SPEC="${MODEL_PROVIDER}/${MODEL_ID}"

log "methodology=$METHODOLOGY_PATH"
log "model=$MODEL_SPEC prompt='$TASK_PROMPT' expected_output='$EXPECTED_OUTPUT'"
"$KTX_BIN" list --format json >"$SESSIONS_BEFORE_LOG"

# ---------------------------------------------------------------------------
# Dry-run path: skip the API call. Useful for exercising the parser logic
# against a static JSONL file (KTX_VALIDATE_STREAM_FILE).
# ---------------------------------------------------------------------------
if [ "${KTX_VALIDATE_DRY_RUN:-0}" = "1" ]; then
    log "KTX_VALIDATE_DRY_RUN=1 — skipping API call"
    if [ -n "${KTX_VALIDATE_STREAM_FILE:-}" ] && [ -f "$KTX_VALIDATE_STREAM_FILE" ]; then
        cp "$KTX_VALIDATE_STREAM_FILE" "$STDOUT_LOG"
        log "loaded static JSONL from $KTX_VALIDATE_STREAM_FILE"
    else
        : > "$STDOUT_LOG"
    fi
    # Pretend ktx run exited 0 with no stderr.
    : > "$STDERR_LOG"
    WALL_START_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
else
    WALL_START_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
fi
log "wall_start_utc=$WALL_START_ISO"

# ---------------------------------------------------------------------------
# Invoke ktx run, capturing stdout and stderr. The wrapper's run summary
# also lands on stderr (ktx run convention) so we don't interfere.
# Allow up to five minutes for the benchmark command.
# ---------------------------------------------------------------------------
if [ "${KTX_VALIDATE_DRY_RUN:-0}" != "1" ]; then
    log "invoking: ktx run opencode run --format json -m $MODEL_SPEC '$TASK_PROMPT'"
    if timeout 300 "$KTX_BIN" run "$OPENCODE_BIN" run --format json -m "$MODEL_SPEC" "$TASK_PROMPT" \
        >"$STDOUT_LOG" 2>"$STDERR_LOG"; then
        RC=0
    else
        RC=$?
    fi
    if [ "$RC" -ne 0 ]; then
        log "ktx run exited non-zero (rc=$RC); stderr head:"
        head -5 "$STDERR_LOG" >&2 || true
        # The run failed; any environmental failure is reported as a
        # [FAIL] verdict for affected layers, then the same VALIDATION=FAIL
        # envelope and exit 1 — no silent success on partial data.
        emit "[FAIL]" "L1 session-capture: ktx run failed (rc=$RC) before a session row could be queried"
        emit "[FAIL]" "L2 stream-token-evidence: blocked — ktx run failed"
        emit "[FAIL]" "L3 stream-cost-evidence: blocked — ktx run failed"
        emit "[FAIL]" "L4 parser-1-to-1: blocked — ktx run failed"
        emit "[FAIL]" "L5 opencode-answer match: blocked — ktx run failed"
        printf 'validation=FAIL [L1,L2,L3,L4,L5]\n' >&2
        exit 1
    fi
fi
"$KTX_BIN" list --format json >"$SESSIONS_AFTER_LOG"

# ---------------------------------------------------------------------------
# Layer 1: session-capture
#   Exactly one session appears after the wrapped invocation.
# ---------------------------------------------------------------------------
SESSION_JSON="$(python3 - "$SESSIONS_BEFORE_LOG" "$SESSIONS_AFTER_LOG" <<'PY'
import json
import sys

before = json.load(open(sys.argv[1]))
after = json.load(open(sys.argv[2]))
before_ids = {session.get("id") for session in before if session.get("id")}
new_sessions = [session for session in after if session.get("id") not in before_ids]
if len(new_sessions) == 1:
    print(json.dumps(new_sessions[0]))
PY
)"
if [ -z "$SESSION_JSON" ]; then
    NEW_SESSION_COUNT="$(python3 - "$SESSIONS_BEFORE_LOG" "$SESSIONS_AFTER_LOG" <<'PY'
import json
import sys
before = json.load(open(sys.argv[1]))
after = json.load(open(sys.argv[2]))
before_ids = {session.get("id") for session in before if session.get("id")}
print(sum(session.get("id") not in before_ids for session in after))
PY
)"
    emit "[FAIL]" "L1 session-capture: expected exactly one new ktx list session, found $NEW_SESSION_COUNT"
else
    SESSION_ID=$(printf '%s' "$SESSION_JSON" | jq -r '.id')
    SESSION_AGENT=$(printf '%s' "$SESSION_JSON" | jq -r '.agent')
    SESSION_STARTED=$(printf '%s' "$SESSION_JSON" | jq -r '.started_at')
    SESSION_TC_COUNT=$(printf '%s' "$SESSION_JSON" | jq -r '.tool_call_count // 0')
    emit "[PASS]" "L1 session-capture: id=$SESSION_ID agent=$SESSION_AGENT started_at=$SESSION_STARTED tool_call_count=$SESSION_TC_COUNT"
fi

# ---------------------------------------------------------------------------
# Layer 4: parser 1-to-1 with persisted tool_calls
#   count of type=tool_use lines on captured stdout == session's
#   tool_call_count field returned by ktx list (stream<->persistence bridge)
# ---------------------------------------------------------------------------
JSONL_TOOL_USE_COUNT=$(grep -cE '"type"[[:space:]]*:[[:space:]]*"tool_use"' "$STDOUT_LOG" 2>/dev/null)
JSONL_TOOL_USE_COUNT="${JSONL_TOOL_USE_COUNT:-0}"
if [ -z "$SESSION_JSON" ]; then
    emit "[FAIL]" "L4 parser-1-to-1: blocked — no matching session row (L1 FAIL upstream)"
elif [ "$JSONL_TOOL_USE_COUNT" = "$SESSION_TC_COUNT" ]; then
    emit "[PASS]" "L4 parser-1-to-1: JSONL tool_use=$JSONL_TOOL_USE_COUNT == persisted tool_call_count=$SESSION_TC_COUNT"
else
    emit "[FAIL]" "L4 parser-1-to-1: JSONL tool_use=$JSONL_TOOL_USE_COUNT != persisted tool_call_count=$SESSION_TC_COUNT"
fi

# ---------------------------------------------------------------------------
# Layer 2: stream-token-evidence
#   At least one type=step_finish line has part.tokens.input > 0 AND
#   part.tokens.output > 0.
# ---------------------------------------------------------------------------
L2_INFO="$(python3 - "$STDOUT_LOG" <<'PY'
import sys, json
ok = 0
total_sf = 0
max_in = 0
max_out = 0
with open(sys.argv[1]) as f:
    for raw in f:
        line = raw.strip()
        if not line:
            continue
        try:
            o = json.loads(line)
        except Exception:
            continue
        if o.get('type') != 'step_finish':
            continue
        total_sf += 1
        part = o.get('part') or {}
        tk = part.get('tokens') or {}
        ti = tk.get('input')
        to = tk.get('output')
        ti_n = ti if isinstance(ti, (int, float)) else 0
        to_n = to if isinstance(to, (int, float)) else 0
        if ti_n > 0 and to_n > 0:
            ok += 1
        max_in = max(max_in, ti_n)
        max_out = max(max_out, to_n)
print(ok, total_sf, max_in, max_out)
PY
)"
read -r L2_OK L2_TOTAL L2_MAXIN L2_MAXOUT <<<"$L2_INFO"
L2_OK=${L2_OK:-0}; L2_TOTAL=${L2_TOTAL:-0}; L2_MAXIN=${L2_MAXIN:-0}; L2_MAXOUT=${L2_MAXOUT:-0}
if [ "$L2_OK" -ge 1 ]; then
    emit "[PASS]" "L2 stream-token-evidence: $L2_OK/$L2_TOTAL step_finish lines have tokens.input>0 AND tokens.output>0 (max_in=$L2_MAXIN, max_out=$L2_MAXOUT)"
else
    if [ "$L2_TOTAL" -eq 0 ]; then
        emit "[FAIL]" "L2 stream-token-evidence: no type=step_finish lines on captured stdout (expected >=1)"
    else
        emit "[FAIL]" "L2 stream-token-evidence: $L2_TOTAL step_finish lines but $L2_OK have both tokens.input>0 and tokens.output>0 (max_in=$L2_MAXIN, max_out=$L2_MAXOUT)"
    fi
fi

# ---------------------------------------------------------------------------
# Layer 3: stream-cost-evidence
#   Either:
#     (a) at least one type=step_finish line carries part.cost as a number,
#   OR
#     (b) derived cost reconstructable: derived_cost =
#         (sum_tokens_in * in_price + sum_tokens_out * out_price) / 1e6 > 0
#   against the ktx prices cache. Cache-miss treated as a real L3 FAIL
#   under the environmental-failure policy.
# ---------------------------------------------------------------------------
L3_INFO="$(python3 - "$STDOUT_LOG" "$MODEL_PROVIDER" "$MODEL_ID" <<'PY'
import sys, json, os
stream_file = sys.argv[1]
provider = sys.argv[2]
model = sys.argv[3]

stream_cost_present = False
stream_cost_value = None
sum_in = 0
sum_out = 0
n_step_finish = 0
with open(stream_file) as f:
    for raw in f:
        line = raw.strip()
        if not line:
            continue
        try:
            o = json.loads(line)
        except Exception:
            continue
        if o.get('type') != 'step_finish':
            continue
        n_step_finish += 1
        part = o.get('part') or {}
        c = part.get('cost')
        if isinstance(c, (int, float)):
            stream_cost_present = True
            if stream_cost_value is None or c > stream_cost_value:
                stream_cost_value = c
        tk = part.get('tokens') or {}
        ti = tk.get('input')
        to = tk.get('output')
        if isinstance(ti, (int, float)):
            sum_in += ti
        if isinstance(to, (int, float)):
            sum_out += to

xdg = os.environ.get('XDG_DATA_HOME') or os.path.expanduser('~/.local/share')
cache_path = os.path.join(xdg, 'ktx', 'prices.json')
cache_status = 'cache-absent'
derived_value = None
if os.path.exists(cache_path):
    cache_status = 'present'
    try:
        prices = json.load(open(cache_path))
    except Exception as e:
        cache_status = f'cache-corrupt: {e}'
    else:
        pv = prices.get(provider)
        if not isinstance(pv, dict):
            cache_status = f'cache-miss: provider {provider!r} not in cache'
        elif model not in pv:
            avail = '/'.join(sorted(pv.keys())[:8])
            cache_status = f'cache-miss: model {model!r} not in cache for provider {provider!r} (available: {avail}...)'
        else:
            mp = pv[model]
            in_price = mp.get('input') or 0
            out_price = mp.get('output') or 0
            if not isinstance(in_price, (int, float)) or not isinstance(out_price, (int, float)):
                cache_status = f'cache-corrupt: prices for {provider}/{model} not numeric'
            else:
                cache_status = 'hit'
                derived_value = (sum_in * in_price + sum_out * out_price) / 1_000_000.0
else:
    cache_status = f'cache-absent: {cache_path} missing'

# Emit as JSON object to avoid bash positional-read fragility on
# values that may contain spaces, commas, or quotes.
def js(v):
    return 'null' if v is None else v
print(json.dumps({
    'present': stream_cost_present,
    'stream_cost_value': js(stream_cost_value),
    'derived_value': js(derived_value),
    'cache_status': cache_status,
    'sum_in': sum_in,
    'sum_out': sum_out,
    'n_step_finish': n_step_finish,
}))
PY
)"
[ -z "$L3_INFO" ] && L3_INFO='{}'
L3A_PRESENT=$(printf '%s' "$L3_INFO" | jq -r '.present // false')
L3A_VAL=$(printf   '%s' "$L3_INFO" | jq -r '.stream_cost_value // "none"')
L3B_VAL=$(printf   '%s' "$L3_INFO" | jq -r '.derived_value    // "none"')
L3_CACHE=$(printf  '%s' "$L3_INFO" | jq -r '.cache_status     // "cache-absent"')
L3_SUMIN=$(printf  '%s' "$L3_INFO" | jq -r '.sum_in           // 0')
L3_SUMOUT=$(printf '%s' "$L3_INFO" | jq -r '.sum_out          // 0')
L3_NSF=$(printf    '%s' "$L3_INFO" | jq -r '.n_step_finish    // 0')
if [ "$L3A_PRESENT" = "true" ]; then
    emit "[PASS]" "L3 stream-cost-evidence: branch (a) — step_finish.part.cost present as number (value=$L3A_VAL) on captured stream; $L3_NSF step_finish lines; sum_tokens_in=$L3_SUMIN sum_tokens_out=$L3_SUMOUT; cache=$L3_CACHE"
elif [ "$L3B_VAL" != "null" ] && [ "$L3B_VAL" != "none" ] && python3 -c 'import sys; sys.exit(0 if float(sys.argv[1])>0 else 1)' "$L3B_VAL" 2>/dev/null; then
    emit "[PASS]" "L3 stream-cost-evidence: branch (b) — derived cost=$L3B_VAL from stream tokens_in=$L3_SUMIN tokens_out=$L3_SUMOUT model=$MODEL_SPEC (cache=$L3_CACHE)"
else
    emit "[FAIL]" "L3 stream-cost-evidence: branch (a) absent (no step_finish.part.cost as number) AND branch (b) failed (derived=$L3B_VAL, $L3_NSF step_finish lines, sum_tokens_in=$L3_SUMIN sum_tokens_out=$L3_SUMOUT, cache=$L3_CACHE) — cache-miss is treated as a real L3 FAIL under the environmental-failure policy"
fi

# ---------------------------------------------------------------------------
# Layer 5: opencode-answer exact-match
#   Final type=text event's part.text field, after whitespace trim, equals
#   the methodology task.expected_output ('4') exactly (score=1.0).
# ---------------------------------------------------------------------------
L5_TEXT="$(python3 - "$STDOUT_LOG" <<'PY'
import sys, json
last_text = None
with open(sys.argv[1]) as f:
    for raw in f:
        line = raw.strip()
        if not line:
            continue
        try:
            o = json.loads(line)
        except Exception:
            continue
        if o.get('type') == 'text':
            # OpenCode nests the answer under part.text (see
            # tools/bench/fixtures/opencode-tool-use.jsonl line 5). The
            # flat-fallback path also accepts a top-level 'content' field.
            part = o.get('part') or {}
            txt = (part.get('text')
                   or part.get('content')
                   or o.get('content')
                   or '')
            last_text = txt.strip()
print(last_text if last_text is not None else '')
PY
)"
if [ "$L5_TEXT" = "$EXPECTED_OUTPUT" ]; then
    emit "[PASS]" "L5 opencode-answer match: final type=text part.text='$L5_TEXT' == expected='$EXPECTED_OUTPUT' (score=1.0)"
else
    emit "[FAIL]" "L5 opencode-answer match: final type=text part.text='$L5_TEXT' != expected='$EXPECTED_OUTPUT' (score=0.0)"
fi

# ---------------------------------------------------------------------------
# Collect failing layers and emit the final envelope.
# ---------------------------------------------------------------------------
FAILING=""
[ -z "$SESSION_JSON" ]                                       && FAILING="${FAILING}L1,"
[ "$L2_OK" -lt 1 ]                                           && FAILING="${FAILING}L2,"
# L3 fail: branch (a) absent AND branch (b) not positive.
L3_FAIL=1
if [ "$L3A_PRESENT" = "true" ]; then
    L3_FAIL=0
elif [ "$L3B_VAL" != "null" ] && [ "$L3B_VAL" != "none" ] && python3 -c 'import sys; sys.exit(0 if float(sys.argv[1])>0 else 1)' "$L3B_VAL" 2>/dev/null; then
    L3_FAIL=0
fi
[ "$L3_FAIL" = "1" ]                                         && FAILING="${FAILING}L3,"
if [ -z "$SESSION_JSON" ] || [ "$JSONL_TOOL_USE_COUNT" != "$SESSION_TC_COUNT" ]; then
    FAILING="${FAILING}L4,"
fi
[ "$L5_TEXT" != "$EXPECTED_OUTPUT" ]                        && FAILING="${FAILING}L5,"

if [ -z "$FAILING" ]; then
    printf 'validation=PASS\n' >&2
    exit 0
fi
# strip trailing comma
FAILING="${FAILING%,}"
printf 'validation=FAIL [%s]\n' "$FAILING" >&2
exit 1
