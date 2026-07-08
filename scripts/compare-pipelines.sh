#!/usr/bin/env bash
#
# scripts/compare-pipelines.sh
#
# Multi-pipeline comparison: runs `ktx bench run --replay <session-id>`
# against N different ktx.toml configurations and tabulates compression
# ratios.
#
# This measures per-tool-call compression ratio only. It does NOT measure
# downstream effects on agent behavior (extra tool calls, wrong answers).
# For that, run the live A/B cost comparison (see TODO.md).
#
# Usage:
#   scripts/compare-pipelines.sh <session-id> [--json | --text]
#
# Requires: jq, ktx binary on PATH

set -euo pipefail

# ── Pipeline configs ────────────────────────────────────────────────
#
# Each config is: name|stages
# Stages use ktx.toml syntax inside the stages array.

CONFIGS=(
  "passthrough|[]"
  "tier1-cleanup|[\"strip-ansi\", \"dedup-blank\", \"normalize\"]"
  "conservative|[\"strip-ansi\", \"dedup-blank\", \"normalize\", \"keep-ends:50:20\", \"token-budget:2000\"]"
  "aggressive-head|[\"strip-ansi\", \"dedup-blank\", \"normalize\", \"head:30\"]"
  "aggressive-keep-ends|[\"strip-ansi\", \"dedup-blank\", \"normalize\", \"keep-ends:30:10\"]"
)

# ── Args ────────────────────────────────────────────────────────────

SESSION_ID="${1:-}"
FORMAT="text"

if [[ -z "$SESSION_ID" || "$SESSION_ID" == "--help" || "$SESSION_ID" == "-h" ]]; then
  echo "Usage: $0 <session-id> [--json | --text]"
  echo ""
  echo "Runs ktx bench run --replay against multiple pipeline configs"
  echo "and tabulates compression ratios."
  echo ""
  echo "Pipelines tested:"
  for cfg in "${CONFIGS[@]}"; do
    name="${cfg%%|*}"
    echo "  $name"
  done
  exit 1
fi

shift
while [[ $# -gt 0 ]]; do
  case "$1" in
    --json)  FORMAT="json";  shift ;;
    --text)  FORMAT="text";  shift ;;
    *)
      echo "Unknown flag: $1" >&2
      exit 1
      ;;
  esac
done

# ── Setup ───────────────────────────────────────────────────────────

KTX_BIN="$(command -v ktx 2>/dev/null || echo "")"
if [[ -z "$KTX_BIN" ]]; then
  if [[ -x "$(pwd)/ktx" ]]; then
    KTX_BIN="$(pwd)/ktx"
  else
    echo "error: ktx binary not found on PATH or in cwd" >&2
    exit 1
  fi
fi
# Resolve to absolute path so cd in the loop doesn't break it
KTX_BIN="$(readlink -f "$KTX_BIN")"

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required" >&2
  exit 1
fi

# Create a temp working directory for config files.
# ktx discovers config by cwd precedence: ./ktx.toml > $XDG_CONFIG_HOME/ktx/config.toml.
# Running from a temp dir with a written ktx.toml controls the pipeline
# without touching the user's real config.
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# ── Run ─────────────────────────────────────────────────────────────

RESULTS="[]"

for cfg in "${CONFIGS[@]}"; do
  name="${cfg%%|*}"
  stages="${cfg#*|}"

  # Write the config file for this pipeline
  cat > "$WORKDIR/ktx.toml" << TOML
[pipeline.default]
stages = $stages
TOML

  # Run replay from the temp dir so ktx discovers our ktx.toml
  output="$(cd "$WORKDIR" && "$KTX_BIN" bench run --replay "$SESSION_ID" 2>/dev/null)" || {
    echo "warning: replay failed for pipeline '$name'" >&2
    continue
  }

  # Extract compression fields from the replay JSON
  entry="$(echo "$output" | jq -c --arg name "$name" '{
    name: $name,
    pipeline: .compression.pipeline,
    baseline_bytes: .compression.baseline_bytes,
    compressed_bytes: .compression.compressed_bytes,
    bytes_saved: .compression.bytes_saved,
    ratio: .compression.ratio,
    consistency: .consistency,
    integrity_pass: .integrity.pass
  }')"

  RESULTS="$(echo "$RESULTS" | jq -c --argjson entry "$entry" '. + [$entry]')"
done

# ── Output ──────────────────────────────────────────────────────────

if [[ "$FORMAT" == "json" ]]; then
  echo "$RESULTS" | jq '.'
else
  # Header
  printf "%-24s %10s %12s %8s %10s %9s\n" \
    "pipeline" "baseline" "compressed" "ratio" "saved" "consistency"
  printf "%-24s %10s %12s %8s %10s %9s\n" \
    "--------" "--------" "----------" "-----" "-----" "-----------"

  echo "$RESULTS" | jq -r '.[] | [
    .name,
    (.baseline_bytes | tostring),
    (.compressed_bytes | tostring),
    (.ratio | . * 1000 | round / 1000 | tostring),
    (.bytes_saved | tostring),
    .consistency
  ] | @tsv' | while IFS=$'\t' read -r name baseline compressed ratio saved consistency; do
    printf "%-24s %10s %12s %8s %10s %9s\n" \
      "$name" "$baseline" "$compressed" "$ratio" "$saved" "$consistency"
  done
fi
