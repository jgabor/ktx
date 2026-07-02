#!/usr/bin/env bash
#
# scripts/validate-hook.sh
#
# In-flight compression hook validation for a real OpenCode session.
#
# Spawns `opencode run` with the ktx plugin installed, a ktx.toml pipeline
# configured, and a pinned model. After the session, queries the ktx DB for
# the A/B compression numbers (original vs compressed bytes per tool_call).
#
# Prerequisites (the script checks these at startup):
#   - ktx binary on PATH (go build -o ktx ./cmd/ktx && go install ./cmd/ktx)
#   - opencode binary on PATH
#   - jq on PATH
#   - sqlite3 on PATH
#   - opencode credentials for the pinned model
#
# Usage:
#   scripts/validate-hook.sh [options]
#
# Options:
#   --model <spec>         opencode model spec (default: neuralwatt/glm-5.2-short-flex)
#   --pipeline <stages>   comma-separated stage list (default: strip-ansi,grep-v:^debug:,keep-ends:30:10,token-budget:1500)
#   --prompt <text>        prompt to send to the agent (default: built-in 3-command prompt)
#   --ktx-toml <path>     use a pre-written ktx.toml instead of generating one from --pipeline
#   --workdir <dir>        working directory for the opencode session (default: temp dir)
#   --keep-workdir         do not delete the workdir on exit (for inspection)
#   -h, --help             show this help
#
# Exit codes:
#   0  session ran, at least one tool_call was compressed, A/B numbers printed
#   1  session ran but no tool_calls were captured or no compression occurred
#   2  usage error / missing prerequisite

set -uo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────
MODEL="neuralwatt/glm-5.2-short-flex"
PIPELINE_STAGES="strip-ansi,grep-v:^debug:,keep-ends:30:10,token-budget:1500"
PROMPT="Run these bash commands one at a time and tell me what you see:
1. seq 1 100
2. find /usr/share -name '*.txt' 2>/dev/null | head -50
3. echo 'debug: starting up' && echo 'info: processing' && echo 'error: something failed' && echo 'debug: cleanup' && echo 'warning: low memory'"
KTX_TOML_PATH=""
WORKDIR=""
KEEP_WORKDIR=0

# ── Parse args ────────────────────────────────────────────────────────────
while [ $# -gt 0 ]; do
    case "$1" in
        --model)       [ $# -ge 2 ] || { printf 'usage: --model requires a value\n' >&2; exit 2; }
                       MODEL="$2"; shift 2 ;;
        --pipeline)    [ $# -ge 2 ] || { printf 'usage: --pipeline requires a value\n' >&2; exit 2; }
                       PIPELINE_STAGES="$2"; shift 2 ;;
        --prompt)      [ $# -ge 2 ] || { printf 'usage: --prompt requires a value\n' >&2; exit 2; }
                       PROMPT="$2"; shift 2 ;;
        --ktx-toml)    [ $# -ge 2 ] || { printf 'usage: --ktx-toml requires a value\n' >&2; exit 2; }
                       KTX_TOML_PATH="$2"; shift 2 ;;
        --workdir)     [ $# -ge 2 ] || { printf 'usage: --workdir requires a value\n' >&2; exit 2; }
                       WORKDIR="$2"; shift 2 ;;
        --keep-workdir) KEEP_WORKDIR=1; shift ;;
        -h|--help)     sed -n '1,55p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 0 ;;
        *)             printf 'usage: unknown argument %q\n' "$1" >&2; exit 2 ;;
    esac
done

# ── Prerequisites ────────────────────────────────────────────────────────
command -v ktx      >/dev/null || { printf '[validate-hook] fatal: ktx not on PATH\n' >&2; exit 2; }
command -v opencode >/dev/null || { printf '[validate-hook] fatal: opencode not on PATH\n' >&2; exit 2; }
command -v jq       >/dev/null || { printf '[validate-hook] fatal: jq not on PATH\n' >&2; exit 2; }
command -v sqlite3  >/dev/null || { printf '[validate-hook] fatal: sqlite3 not on PATH\n' >&2; exit 2; }

log()  { printf '[validate-hook] %s\n' "$*" >&2; }

# ── Set up workdir ───────────────────────────────────────────────────────
CLEANUP_WORKDIR=0
if [ -z "$WORKDIR" ]; then
    WORKDIR="$(mktemp -d -t ktx-hook-validate.XXXXXX)"
    CLEANUP_WORKDIR=1
fi
[ "$KEEP_WORKDIR" = "1" ] && CLEANUP_WORKDIR=0

cleanup() {
    if [ "$CLEANUP_WORKDIR" = "1" ]; then
        rm -rf "$WORKDIR"
    fi
}
trap cleanup EXIT

DB_PATH="$WORKDIR/ktx.db"

# ── Write ktx.toml ───────────────────────────────────────────────────────
if [ -n "$KTX_TOML_PATH" ]; then
    cp "$KTX_TOML_PATH" "$WORKDIR/ktx.toml"
else
    # Convert comma-separated stages to TOML array
    TOML_STAGES=$(printf '%s' "$PIPELINE_STAGES" | python3 -c "
import sys
stages = [s.strip() for s in sys.stdin.read().split(',')]
items = ', '.join('\"' + s.replace('\"', '\\\\\"') + '\"' for s in stages)
print(f'stages = [{items}]')
")
    cat > "$WORKDIR/ktx.toml" <<TOML
[pipeline.default]
$TOML_STAGES
TOML
fi

log "workdir=$WORKDIR"
log "model=$MODEL"
log "ktx.toml pipeline stages=$(cat "$WORKDIR/ktx.toml")"
log "db=$DB_PATH"

# ── Ensure plugin is installed ────────────────────────────────────────────
log "ensuring ktx opencode plugin is installed..."
ktx init opencode --uninstall 2>/dev/null || true
ktx init opencode >/dev/null 2>&1 || {
    log "fatal: ktx init opencode failed"
    exit 2
}

# ── Run the session ──────────────────────────────────────────────────────
# Run from $WORKDIR so config.Load() finds the ktx.toml we wrote there
# (ktx hook inherits CWD from the opencode process, which inherits from us).
log "launching opencode run (cwd=$WORKDIR)..."

# Use a subshell so the CWD change is local to the opencode invocation.
(
    cd "$WORKDIR"
    KTX_DB="$DB_PATH" exec opencode run -m "$MODEL" "$PROMPT"
) >/dev/null 2>&1
SESSION_RC=$?
log "opencode run exited (rc=$SESSION_RC)"

if [ ! -f "$DB_PATH" ]; then
    log "fatal: DB not created at $DB_PATH — ktx hook did not fire"
    exit 1
fi

# ── Query A/B numbers ────────────────────────────────────────────────────
log "querying DB for compression A/B numbers..."
RESULT=$(sqlite3 -json "$DB_PATH" "
SELECT
    tc.id       AS source_id,
    tc.tool     AS tool,
    length(tc.output)  AS original_bytes,
    c.id        AS compressed_id,
    length(c.output)    AS compressed_bytes,
    CASE WHEN length(tc.output) > 0
         THEN ROUND(length(c.output) * 1.0 / length(tc.output), 4)
         ELSE NULL
    END         AS ratio,
    (length(tc.output) - length(c.output)) AS bytes_saved,
    c.pipeline  AS pipeline
FROM tool_calls tc
LEFT JOIN compressed c ON c.source_id = tc.id
ORDER BY tc.id;
" 2>&1)

if [ -z "$RESULT" ] || [ "$RESULT" = "[]" ]; then
    log "no tool_calls found in DB"
    exit 1
fi

# ── Print results table ───────────────────────────────────────────────────
printf '\n'
printf '%-20s %-8s %10s %10s %8s %10s  %s\n' \
    "SOURCE_ID" "TOOL" "ORIG_BYTES" "COMP_BYTES" "RATIO" "SAVED" "PIPELINE"
printf '%s\n' "───────────────────────────────────────────────────────────────────────────────────────────────────────────"

TOTAL_ORIG=0
TOTAL_COMP=0
HAS_COMPRESSION=0

echo "$RESULT" | jq -r '.[] | "\(.source_id)\t\(.tool)\t\(.original_bytes // 0)\t\(.compressed_bytes // "null")\t\(.ratio // "null")\t\(.bytes_saved // "null")\t\(.pipeline // "null")"' | \
while IFS=$'\t' read -r sid tool orig comp ratio saved pipeline; do
    if [ "$comp" = "null" ] || [ -z "$comp" ]; then
        comp_str="passthrough"
        ratio_str="1.0000"
        saved_str="0"
        pipeline_str="(none)"
    else
        comp_str="$comp"
        ratio_str="$ratio"
        saved_str="$saved"
        pipeline_str="$pipeline"
    fi
    printf '%-20s %-8s %10s %10s %8s %10s  %s\n' \
        "$sid" "$tool" "$orig" "$comp_str" "$ratio_str" "$saved_str" "$pipeline_str"
done

# ── Totals ────────────────────────────────────────────────────────────────
TOTALS=$(echo "$RESULT" | python3 -c "
import sys, json
rows = json.load(sys.stdin)
total_orig = sum(r.get('original_bytes') or 0 for r in rows)
comp_rows = [r for r in rows if r.get('compressed_bytes') is not None]
total_comp = sum(r['compressed_bytes'] for r in comp_rows) + sum(r['original_bytes'] for r in rows if r.get('compressed_bytes') is None)
ratio = (total_comp / total_orig) if total_orig > 0 else 1.0
saved = total_orig - total_comp
print(json.dumps({'total_original': total_orig, 'total_compressed': total_comp, 'ratio': round(ratio, 4), 'bytes_saved': saved, 'compressed_calls': len(comp_rows), 'total_calls': len(rows)}))
")

TOTAL_ORIG=$(echo "$TOTALS" | jq -r '.total_original')
TOTAL_COMP=$(echo "$TOTALS" | jq -r '.total_compressed')
TOTAL_RATIO=$(echo "$TOTALS" | jq -r '.ratio')
TOTAL_SAVED=$(echo "$TOTALS" | jq -r '.bytes_saved')
COMP_CALLS=$(echo "$TOTALS" | jq -r '.compressed_calls')
TOTAL_CALLS=$(echo "$TOTALS" | jq -r '.total_calls')

printf '%s\n' "───────────────────────────────────────────────────────────────────────────────────────────────────────────"
printf '%-20s %-8s %10s %10s %8s %10s\n' \
    "TOTAL" "" "$TOTAL_ORIG" "$TOTAL_COMP" "$TOTAL_RATIO" "$TOTAL_SAVED"
printf '\n'
log "tool_calls: $TOTAL_CALLS total, $COMP_CALLS compressed"
log "bytes: $TOTAL_ORIG original → $TOTAL_COMP compressed ($TOTAL_SAVED bytes saved, ratio=$TOTAL_RATIO)"
printf '\n'
printf 'validation=%s\n' "$([ "$COMP_CALLS" -gt 0 ] && [ "$TOTAL_SAVED" -gt 0 ] && echo 'PASS' || echo 'FAIL')"
