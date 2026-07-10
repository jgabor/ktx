#!/usr/bin/env python3
"""Rank named pipelines via repeated live-ab pairwise runs against a baseline."""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LIVE_AB = ROOT / "scripts" / "live_ab.py"
PIPELINES = ROOT / "tools" / "bench" / "pipelines"


def utc_now():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def parser():
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--model", required=True)
    result.add_argument("--task-file", required=True)
    result.add_argument(
        "--pipelines",
        default="sanitize,keep-ends-mid,conservative-a,conservative-a-recover,aggressive-head",
        help="comma-separated candidate pipeline ids under tools/bench/pipelines/",
    )
    result.add_argument("--rounds", type=int, default=5)
    result.add_argument("--max-attempts", type=int, default=24)
    result.add_argument("--attempt-timeout", default="15m")
    result.add_argument("--image", default="ktx-bench:session-linked-verify")
    result.add_argument("--data-dir")
    result.add_argument("--auth")
    result.add_argument("--campaign-id")
    result.add_argument(
        "--baseline-pipeline",
        default="passthrough",
        help="baseline pipeline id (default passthrough)",
    )
    return result


def pipeline_config(pipeline_id: str) -> Path:
    path = PIPELINES / f"{pipeline_id}.toml"
    if not path.is_file():
        raise SystemExit(f"missing pipeline config: {path}")
    return path


def run_pair(args, campaign_dir: Path, candidate_pipeline: str) -> dict:
    baseline = pipeline_config(args.baseline_pipeline)
    candidate = pipeline_config(candidate_pipeline)
    experiment_id = f"{campaign_dir.name}-{candidate_pipeline}"
    command = [
        sys.executable,
        str(LIVE_AB),
        "--model",
        args.model,
        "--task-file",
        str(Path(args.task_file).resolve()),
        "--baseline-config",
        str(baseline),
        "--candidate-config",
        str(candidate),
        "--workspace-mode",
        "corpus",
        "--rounds",
        str(args.rounds),
        "--max-attempts",
        str(args.max_attempts),
        "--attempt-timeout",
        args.attempt_timeout,
        "--image",
        args.image,
        "--experiment-id",
        experiment_id,
        "--data-dir",
        str(campaign_dir.parent),
        "--format",
        "json",
    ]
    if args.auth:
        command.extend(["--auth", args.auth])
    log_path = campaign_dir / f"pair-{candidate_pipeline}.log"
    print(
        f"==> ranking pair baseline={args.baseline_pipeline} "
        f"candidate={candidate_pipeline} log={log_path}",
        flush=True,
    )
    with log_path.open("w", encoding="utf-8") as log_handle:
        completed = subprocess.run(
            command,
            cwd=str(ROOT),
            text=True,
            stdout=log_handle,
            stderr=subprocess.STDOUT,
        )
    exp_dir = campaign_dir.parent / experiment_id
    summary_path = exp_dir / "summary.json"
    summary = None
    if summary_path.is_file():
        summary = json.loads(summary_path.read_text(encoding="utf-8"))
    log_tail = ""
    if log_path.is_file():
        log_tail = log_path.read_text(encoding="utf-8", errors="replace")[-2000:]
    record = {
        "candidate_pipeline": candidate_pipeline,
        "experiment_id": experiment_id,
        "experiment_dir": str(exp_dir),
        "exit_code": completed.returncode,
        "log_path": str(log_path),
        "stderr_tail": log_tail,
        "summary": summary,
    }
    (campaign_dir / f"pair-{candidate_pipeline}.json").write_text(
        json.dumps(record, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    if completed.returncode not in (0, 1):
        print(completed.stderr, file=sys.stderr)
        print(
            f"warning: pair {candidate_pipeline} exited {completed.returncode}",
            file=sys.stderr,
        )
    return record


def extract_side_metrics(summary: dict | None, side: str) -> dict:
    if not summary:
        return {
            "valid_count": 0,
            "evaluated_count": 0,
            "mean_tokens_in": None,
            "mean_cost": None,
            "mean_fact_score": None,
            "full_pass_rate": None,
            "expected_cost_per_correct": None,
            "mean_tool_calls": None,
        }
    details = summary["pipelines"][side]
    return {
        "valid_count": details.get("valid_count"),
        "evaluated_count": details.get("evaluated_count"),
        "mean_tokens_in": (details.get("tokens_in") or {}).get("mean"),
        "mean_cost": (details.get("cost") or {}).get("mean"),
        "mean_fact_score": details.get("mean_fact_score"),
        "full_pass_rate": details.get("full_pass_rate"),
        "expected_cost_per_correct": details.get("expected_cost_per_correct"),
        "mean_tool_calls": (details.get("tool_call_count") or {}).get("mean"),
    }


def build_ranking(args, pairs: list[dict]) -> dict:
    rows = []
    baseline_metrics = None
    for pair in pairs:
        summary = pair.get("summary")
        if summary:
            baseline_metrics = extract_side_metrics(summary, "baseline")
            break
    if baseline_metrics:
        rows.append(
            {
                "pipeline": args.baseline_pipeline,
                "side": "baseline",
                "experiment_id": None,
                **baseline_metrics,
                "tool_call_inflation_vs_baseline": 1.0,
                "tokens_saved_vs_baseline": 0.0,
                "cost_saved_vs_baseline": 0.0,
            }
        )
    for pair in pairs:
        summary = pair.get("summary")
        candidate = extract_side_metrics(summary, "candidate")
        inflation = None
        tokens_saved = None
        cost_saved = None
        if baseline_metrics and candidate["mean_tool_calls"] is not None:
            base_tools = baseline_metrics.get("mean_tool_calls")
            if base_tools not in (None, 0):
                inflation = candidate["mean_tool_calls"] / base_tools
        if baseline_metrics and candidate["mean_tokens_in"] is not None:
            if baseline_metrics["mean_tokens_in"] is not None:
                tokens_saved = baseline_metrics["mean_tokens_in"] - candidate["mean_tokens_in"]
        if baseline_metrics and candidate["mean_cost"] is not None:
            if baseline_metrics["mean_cost"] is not None:
                cost_saved = baseline_metrics["mean_cost"] - candidate["mean_cost"]
        rows.append(
            {
                "pipeline": pair["candidate_pipeline"],
                "side": "candidate",
                "experiment_id": pair.get("experiment_id"),
                "exit_code": pair.get("exit_code"),
                **candidate,
                "tool_call_inflation_vs_baseline": inflation,
                "tokens_saved_vs_baseline": tokens_saved,
                "cost_saved_vs_baseline": cost_saved,
                "pair_tool_call_inflation": (summary or {}).get("tool_call_inflation"),
            }
        )

    def sort_key(row):
        pass_rate = row.get("full_pass_rate")
        expected = row.get("expected_cost_per_correct")
        mean_cost = row.get("mean_cost")
        return (
            0 if pass_rate == 1.0 else 1 if pass_rate is not None else 2,
            expected if expected is not None else 1e9,
            mean_cost if mean_cost is not None else 1e9,
            row.get("pipeline") or "",
        )

    ranked = sorted(rows, key=sort_key)
    for index, row in enumerate(ranked, start=1):
        row["rank"] = index
    return {
        "schema_version": 1,
        "created_at": utc_now(),
        "task_file": str(Path(args.task_file).resolve()),
        "model": args.model,
        "rounds": args.rounds,
        "baseline_pipeline": args.baseline_pipeline,
        "candidate_pipelines": [pair["candidate_pipeline"] for pair in pairs],
        "ranking": ranked,
        "pairs": [
            {
                "candidate_pipeline": pair["candidate_pipeline"],
                "experiment_id": pair.get("experiment_id"),
                "exit_code": pair.get("exit_code"),
            }
            for pair in pairs
        ],
    }


def render_ranking(ranking: dict) -> str:
    lines = [
        f"task={Path(ranking['task_file']).name} model={ranking['model']} rounds={ranking['rounds']}",
        "",
        f"{'rank':>4}  {'pipeline':<24}  {'pass':>6}  {'fact':>6}  {'tokens':>10}  {'cost':>10}  {'$/correct':>10}  {'tools':>6}  {'infl':>6}",
        f"{'----':>4}  {'--------':<24}  {'----':>6}  {'----':>6}  {'------':>10}  {'----':>10}  {'---------':>10}  {'-----':>6}  {'----':>6}",
    ]
    for row in ranking["ranking"]:
        def fmt(value, digits=4):
            if value is None:
                return "-"
            if isinstance(value, float):
                return f"{value:.{digits}g}"
            return str(value)

        lines.append(
            f"{row['rank']:>4}  {row['pipeline']:<24}  {fmt(row.get('full_pass_rate'), 3):>6}  "
            f"{fmt(row.get('mean_fact_score'), 3):>6}  {fmt(row.get('mean_tokens_in'), 6):>10}  "
            f"{fmt(row.get('mean_cost'), 4):>10}  {fmt(row.get('expected_cost_per_correct'), 4):>10}  "
            f"{fmt(row.get('mean_tool_calls'), 3):>6}  "
            f"{fmt(row.get('tool_call_inflation_vs_baseline'), 3):>6}"
        )
    return "\n".join(lines)


def main(argv=None):
    args = parser().parse_args(argv)
    if not args.model.startswith("neuralwatt/"):
        raise SystemExit("model must use the neuralwatt/ provider")
    data_dir = (
        Path(args.data_dir).resolve()
        if args.data_dir
        else Path(ROOT / ".bench-data").resolve()
    )
    campaign_id = args.campaign_id or f"rank-{datetime.now(timezone.utc):%Y%m%dT%H%M%SZ}"
    campaign_dir = data_dir / campaign_id
    campaign_dir.mkdir(parents=True, exist_ok=False)
    pipelines = [item.strip() for item in args.pipelines.split(",") if item.strip()]
    if not pipelines:
        raise SystemExit("no candidate pipelines specified")
    pairs = []
    for pipeline_id in pipelines:
        pairs.append(run_pair(args, campaign_dir, pipeline_id))
    ranking = build_ranking(args, pairs)
    (campaign_dir / "ranking.json").write_text(
        json.dumps(ranking, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    text = render_ranking(ranking)
    (campaign_dir / "ranking.txt").write_text(text + "\n", encoding="utf-8")
    print(text)
    print(f"\nwrote {campaign_dir / 'ranking.json'}")
    if any(pair.get("exit_code", 1) != 0 for pair in pairs):
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
