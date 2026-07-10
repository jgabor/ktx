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
# For that, run the live A/B cost comparison (scripts/live-ab.sh).
#
# Usage:
#   scripts/compare-pipelines.sh <session-id> [--json | --text]
#
# Requires: jq, ktx binary on PATH

set -euo pipefail

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(dirname -- "$SCRIPT_DIR")"
PIPELINE_DIR="$REPO_ROOT/tools/bench/pipelines"
shopt -s nullglob
CONFIG_FILES=("$PIPELINE_DIR"/*.toml)
if [[ ${#CONFIG_FILES[@]} -eq 0 ]]; then
  echo "error: no pipeline configs found in $PIPELINE_DIR" >&2
  exit 1
fi

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
  for config_source in "${CONFIG_FILES[@]}"; do
    printf '  %s\n' "$(basename "${config_source%.toml}")"
  done
  if [[ "$SESSION_ID" == "--help" || "$SESSION_ID" == "-h" ]]; then
    exit 0
  fi
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

KTX_BIN="${KTX_BIN:-}"
if [[ -z "$KTX_BIN" ]]; then
  KTX_BIN="$(command -v ktx 2>/dev/null || echo "")"
fi
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

# Create a temporary XDG config root for each pipeline without touching the
# user's real config.
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# ── Run ─────────────────────────────────────────────────────────────

RESULTS="[]"

for config_source in "${CONFIG_FILES[@]}"; do
  name="$(basename "${config_source%.toml}")"

  config_root="$WORKDIR/$name"
  config_path="$config_root/ktx/ktx.toml"
  mkdir -p "$config_root/ktx"
  cp "$config_source" "$config_path"
  config_sha256="$(sha256sum "$config_path" | awk '{print $1}')"
  stages="$(XDG_CONFIG_HOME="$config_root" "$KTX_BIN" config show --format json | jq -c '.profiles.default.stages')"

  output="$(XDG_CONFIG_HOME="$config_root" "$KTX_BIN" bench run --replay "$SESSION_ID" 2>/dev/null)" || {
    echo "warning: replay failed for pipeline '$name'" >&2
    continue
  }

  # Extract compression fields from the replay JSON
  entry="$(echo "$output" | jq -c \
    --arg name "$name" \
    --arg session_id "$SESSION_ID" \
    --arg config_sha256 "$config_sha256" \
    --argjson stages "$stages" '{
    name: $name,
    session_id: $session_id,
    config_sha256: $config_sha256,
    stages: $stages,
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
