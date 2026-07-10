#!/usr/bin/env python3
"""Run and retain a session-linked live compression A/B experiment."""

import argparse
import hashlib
import json
import os
import shutil
import sqlite3
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

try:
    import tomllib
except ModuleNotFoundError:
    import tomli as tomllib


SIDES = ("baseline", "candidate")
ATTEMPT_STATUSES = ("completed", "timeout", "failed", "unlinked", "invalid")
MANIFEST_SCHEMA_VERSION = 1
ATTEMPT_SCHEMA_VERSION = 1
SCORE_EPSILON = 1.0e-9


def utc_now():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def command_output(command, *, env=None, cwd=None):
    result = subprocess.run(
        command,
        capture_output=True,
        cwd=cwd,
        env=env,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(
            f"command failed ({result.returncode}): {' '.join(command)}\n{result.stderr.strip()}"
        )
    return result.stdout


def command_path(name, fallback=None):
    configured = os.environ.get(f"{name.upper()}_BIN")
    if configured:
        return configured
    discovered = shutil.which(name)
    if discovered:
        return discovered
    if fallback and fallback.exists():
        return str(fallback)
    raise RuntimeError(f"required executable not found: {name}")


def positive_integer(value):
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be at least 1")
    return parsed


def nonnegative_integer(value):
    parsed = int(value)
    if parsed < 0:
        raise argparse.ArgumentTypeError("must not be negative")
    return parsed


def parser():
    result = argparse.ArgumentParser(
        description="Run a session-linked live compression A/B experiment"
    )
    result.add_argument("-m", "--model")
    result.add_argument("-p", "--prompt")
    result.add_argument("--task-id")
    result.add_argument("--task-file", help="path to tools/bench/tasks/*.toml task spec")
    result.add_argument("--expected-output")
    result.add_argument(
        "--expected-facts",
        dest="expected_facts_raw",
        help="JSON array of required fact strings for multi-fact fractional scoring",
    )
    result.add_argument("--min-tool-calls", type=nonnegative_integer, default=None)
    result.add_argument(
        "-r",
        "--rounds",
        "--target-valid",
        dest="target_valid",
        type=positive_integer,
        default=3,
    )
    result.add_argument("--max-attempts", type=positive_integer, default=12)
    result.add_argument("--attempt-timeout", default="10m")
    result.add_argument("--termination-grace", default="30s")
    result.add_argument("--experiment-id")
    result.add_argument("--data-dir")
    result.add_argument("--image", default="ktx-bench")
    result.add_argument("--auth")
    result.add_argument("--repo-root")
    result.add_argument(
        "--baseline-config",
        help="ktx.toml for baseline pipeline (default: tools/bench/ktx.toml.baseline)",
    )
    result.add_argument(
        "--candidate-config",
        help="ktx.toml for candidate pipeline (default: tools/bench/ktx.toml.candidate)",
    )
    result.add_argument(
        "--workspace-mode",
        choices=("corpus", "repo"),
        help="attempt workspace contents: corpus fixtures only, or full repo snapshot",
    )
    result.add_argument("--format", choices=("json", "text"), default="json")
    result.add_argument("--build", action="store_true")
    result.add_argument(
        "--summarize",
        metavar="EXPERIMENT_DIR",
        help="regenerate summary.json without invoking Docker",
    )
    return result


def config_metadata(path):
    with path.open("rb") as handle:
        parsed = tomllib.load(handle)
    stages = parsed.get("pipeline", {}).get("default", {}).get("stages", [])
    if not isinstance(stages, list) or not all(isinstance(stage, str) for stage in stages):
        raise RuntimeError(f"invalid pipeline.default.stages in {path}")
    return {
        "path": str(path),
        "sha256": sha256_file(path),
        "stages": stages,
    }


def directory_sha256(path):
    digest = hashlib.sha256()
    file_count = 0
    for entry in sorted(path.rglob("*"), key=lambda candidate: candidate.as_posix()):
        relative = entry.relative_to(path).as_posix().encode()
        if entry.is_symlink():
            digest.update(b"link\0" + relative + b"\0" + os.readlink(entry).encode() + b"\0")
            file_count += 1
        elif entry.is_file():
            digest.update(b"file\0" + relative + b"\0")
            with entry.open("rb") as handle:
                for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                    digest.update(chunk)
            digest.update(b"\0")
            file_count += 1
    return {"sha256": digest.hexdigest(), "file_count": file_count}


REPO_WORKSPACE_EXCLUDES = {
    "MANIFEST.json",
    ".git",
    "__pycache__",
    ".bench-data",
}


def snapshot_repository(source, destination, excluded_path):
    excluded_path = excluded_path.resolve()

    def ignore(directory, names):
        ignored = set()
        for name in names:
            candidate = Path(directory, name).resolve()
            if name in {".git", "__pycache__"}:
                ignored.add(name)
            elif candidate == excluded_path or excluded_path.is_relative_to(candidate):
                ignored.add(name)
        return ignored

    shutil.copytree(source, destination, symlinks=True, ignore=ignore)
    return directory_sha256(destination)


def prepare_corpus_workspace(repository_root, workspace, corpus_files):
    """Mount only planted corpus text files; never answer-key artifacts."""
    workspace.mkdir(parents=True, exist_ok=True)
    if not corpus_files:
        return {
            **directory_sha256(workspace),
            "mode": "corpus",
            "files": [],
        }

    corpus_src = repository_root / "tools" / "bench" / "corpus"
    corpus_dst = workspace / "tools" / "bench" / "corpus"
    corpus_dst.mkdir(parents=True, exist_ok=True)
    copied = []
    for name in corpus_files:
        if Path(name).name != name:
            raise RuntimeError(f"corpus file must be a basename, got {name!r}")
        if name == "MANIFEST.json" or not name.endswith(".txt"):
            raise RuntimeError(f"refusing to mount non-fixture corpus file: {name}")
        source = corpus_src / name
        if not source.is_file():
            raise RuntimeError(f"corpus file missing from repository snapshot: {source}")
        destination = corpus_dst / name
        shutil.copy2(source, destination)
        copied.append(f"tools/bench/corpus/{name}")
    return {
        **directory_sha256(workspace),
        "mode": "corpus",
        "files": copied,
    }


def prepare_repo_workspace(repository_root, workspace):
    """Full repo mount with answer-key paths stripped."""

    def ignore(directory, names):
        ignored = set()
        directory_path = Path(directory)
        for name in names:
            if name in REPO_WORKSPACE_EXCLUDES:
                ignored.add(name)
                continue
            relative = (directory_path / name).relative_to(repository_root).as_posix()
            # Task specs and generator embed planted facts; keep them out of agent view.
            if (
                relative
                in {
                    "tools/bench/generate_corpus.py",
                    "docs/pipelines.md",
                    "tools/bench/tasks",
                }
                or relative.startswith("tools/bench/tasks/")
            ):
                ignored.add(name)
            if name == "MANIFEST.json" and relative.startswith("tools/bench/corpus/"):
                ignored.add(name)
        return ignored

    shutil.copytree(repository_root, workspace, symlinks=True, ignore=ignore)
    # Belt-and-suspenders: never leave the answer key in the workspace.
    manifest = workspace / "tools" / "bench" / "corpus" / "MANIFEST.json"
    if manifest.exists():
        manifest.unlink()
    return {
        **directory_sha256(workspace),
        "mode": "repo",
        "files": None,
    }


def prepare_attempt_workspace(repository_root, workspace, *, mode, corpus_files):
    if workspace.exists():
        shutil.rmtree(workspace)
    if mode == "corpus":
        return prepare_corpus_workspace(repository_root, workspace, corpus_files)
    if mode == "repo":
        return prepare_repo_workspace(repository_root, workspace)
    raise RuntimeError(f"unsupported workspace mode: {mode!r}")


def snapshot_configs(experiment_dir, side_sources):
    configs_dir = experiment_dir / "configs"
    configs_dir.mkdir()
    configs = {}
    for side, source in side_sources.items():
        source = Path(source).resolve()
        if not source.is_file():
            raise RuntimeError(f"pipeline config not found for {side}: {source}")
        destination = configs_dir / f"ktx.toml.{side}"
        shutil.copy2(source, destination)
        configs[side] = {
            **config_metadata(destination),
            "source_path": str(source),
            "pipeline_id": source.stem if source.parent.name == "pipelines" else side,
        }
    return configs


def load_task_file(path):
    path = Path(path).resolve()
    with path.open("rb") as handle:
        parsed = tomllib.load(handle)
    task_id = parsed.get("id")
    prompt = parsed.get("prompt")
    scoring = parsed.get("scoring") or {}
    kind = scoring.get("kind", "exact_match")
    facts = scoring.get("facts") or []
    minimum_tool_calls = parsed.get("minimum_tool_calls", 1)
    if not isinstance(task_id, str) or not task_id:
        raise RuntimeError(f"task file missing id: {path}")
    if not isinstance(prompt, str) or not prompt.strip():
        raise RuntimeError(f"task file missing prompt: {path}")
    if not isinstance(minimum_tool_calls, int) or minimum_tool_calls < 0:
        raise RuntimeError(f"task file has invalid minimum_tool_calls: {path}")
    if kind == "multi_fact_fractional":
        if not isinstance(facts, list) or not facts or not all(isinstance(item, str) for item in facts):
            raise RuntimeError(f"task file needs scoring.facts string list: {path}")
        expected_output = None
        expected_facts = list(facts)
    elif kind == "exact_match":
        expected_output = scoring.get("expected_output")
        if not isinstance(expected_output, str):
            raise RuntimeError(f"exact_match task needs scoring.expected_output: {path}")
        expected_facts = None
    else:
        raise RuntimeError(f"unsupported scoring.kind {kind!r} in {path}")
    corpus_files = parsed.get("corpus_files") or []
    if not isinstance(corpus_files, list) or not all(isinstance(item, str) for item in corpus_files):
        raise RuntimeError(f"task file corpus_files must be a string list: {path}")
    workspace_mode = parsed.get("workspace_mode")
    if workspace_mode is None:
        workspace_mode = "corpus" if corpus_files else "repo"
    if workspace_mode not in {"corpus", "repo"}:
        raise RuntimeError(f"task file workspace_mode must be corpus or repo: {path}")
    if workspace_mode == "corpus":
        for name in corpus_files:
            if name == "MANIFEST.json" or Path(name).suffix != ".txt":
                raise RuntimeError(
                    f"corpus workspace only mounts .txt fixtures, not {name!r}: {path}"
                )
    return {
        "id": task_id,
        "prompt": prompt.strip(),
        "expected_output": expected_output,
        "expected_facts": expected_facts,
        "minimum_tool_calls": minimum_tool_calls,
        "skill": parsed.get("skill"),
        "token_band": parsed.get("token_band"),
        "corpus_files": corpus_files,
        "workspace_mode": workspace_mode,
        "quality_risks": parsed.get("quality_risks") or [],
        "scoring_kind": kind,
        "task_file": str(path),
    }


def score_answer(answer, *, expected_output=None, expected_facts=None):
    text = (answer or "").strip()
    if expected_facts is not None:
        matched = [fact for fact in expected_facts if fact in text]
        missed = [fact for fact in expected_facts if fact not in text]
        total = len(expected_facts)
        score = (len(matched) / total) if total else 0.0
        full_pass = total > 0 and len(missed) == 0
        return {
            "score": score,
            "full_pass": full_pass,
            "answer_matches_expected": full_pass,
            "facts_matched": matched,
            "facts_missed": missed,
            "facts_total": total,
        }
    expected = (expected_output or "").strip()
    full_pass = text == expected
    return {
        "score": 1.0 if full_pass else 0.0,
        "full_pass": full_pass,
        "answer_matches_expected": full_pass,
        "facts_matched": [],
        "facts_missed": [],
        "facts_total": 0,
    }


def repository_metadata(repo_root):
    try:
        commit = command_output(["git", "-C", str(repo_root), "rev-parse", "HEAD"]).strip()
        status = command_output(["git", "-C", str(repo_root), "status", "--porcelain"])
    except RuntimeError:
        return {"commit": None, "dirty": None, "status_sha256": None}
    return {
        "commit": commit,
        "dirty": bool(status.strip()),
        "status_sha256": hashlib.sha256(status.encode()).hexdigest(),
    }


def docker_image_id(docker_bin, image):
    return command_output(
        [docker_bin, "image", "inspect", image, "--format", "{{.Id}}"]
    ).strip()


def docker_identity(docker_bin, image, config_path):
    result = subprocess.run(
        [
            docker_bin,
            "run",
            "--rm",
            "-v",
            f"{config_path}:/home/node/.config/ktx/ktx.toml:ro",
            image,
            "--identity",
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"could not inspect benchmark image identity: {result.stderr.strip()}")
    identities = {}
    for line in result.stdout.splitlines():
        name, separator, version = line.partition("\t")
        if separator:
            identities[name] = version
    return identities


def experiment_id(repo_root):
    try:
        suffix = command_output(
            ["git", "-C", str(repo_root), "rev-parse", "--short", "HEAD"]
        ).strip()
    except RuntimeError:
        suffix = "local"
    return f"{datetime.now(timezone.utc):%Y%m%dT%H%M%SZ}-{suffix}-{os.getpid()}"


def load_attempts(experiment_dir):
    path = experiment_dir / "attempts.jsonl"
    if not path.exists():
        return []
    records = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        if not line:
            continue
        try:
            records.append(json.loads(line))
        except json.JSONDecodeError as error:
            raise RuntimeError(f"invalid attempts.jsonl line {line_number}: {error}") from error
    return records


def attempt_is_evaluated(record, minimum_tool_calls):
    return (
        record.get("status") == "completed"
        and bool(record.get("session_id"))
        and record.get("stream_complete") is True
        and record.get("tool_call_count", 0) >= minimum_tool_calls
    )


def attempt_is_full_pass(record, minimum_tool_calls):
    if not attempt_is_evaluated(record, minimum_tool_calls):
        return False
    if "full_pass" in record:
        return record.get("full_pass") is True
    return record.get("answer_matches_expected") is True


def attempt_is_counted(record, minimum_tool_calls):
    # Backward-compatible alias: counted toward target_valid = full pass.
    return attempt_is_full_pass(record, minimum_tool_calls)


def mean(values):
    return sum(values) / len(values) if values else None


def distribution(records, key):
    values = sorted(record[key] for record in records if record.get(key) is not None)
    return {
        "values": values,
        "min": min(values) if values else None,
        "max": max(values) if values else None,
        "mean": mean(values),
    }


def exclusion_reason(record, minimum_tool_calls):
    reasons = []
    if record.get("status") != "completed":
        reasons.append(f"status={record.get('status')}")
    if not record.get("session_id"):
        reasons.append("missing session_id")
    if record.get("stream_complete") is not True:
        reasons.append("incomplete stream")
    if not attempt_is_full_pass(record, minimum_tool_calls) and attempt_is_evaluated(
        record, minimum_tool_calls
    ):
        reasons.append("partial_or_failed_facts")
    elif record.get("answer_matches_expected") is not True and record.get("full_pass") is not True:
        if "partial_or_failed_facts" not in reasons:
            reasons.append("answer mismatch")
    if record.get("tool_call_count", 0) < minimum_tool_calls:
        reasons.append("too few tool calls")
    return reasons


def build_summary(manifest, attempts):
    minimum_tool_calls = manifest["task"]["minimum_tool_calls"]
    grouped = {side: [record for record in attempts if record.get("side") == side] for side in SIDES}
    pipelines = {}
    exclusions = []
    for side in SIDES:
        records = grouped[side]
        evaluated = [
            record for record in records if attempt_is_evaluated(record, minimum_tool_calls)
        ]
        counted = [
            record for record in records if attempt_is_full_pass(record, minimum_tool_calls)
        ]
        status_counts = {status: 0 for status in ATTEMPT_STATUSES}
        for record in records:
            status = record.get("status")
            if status in status_counts:
                status_counts[status] += 1
            if record not in counted:
                exclusions.append(
                    {
                        "attempt_id": record.get("attempt_id"),
                        "side": side,
                        "reasons": exclusion_reason(record, minimum_tool_calls),
                    }
                )
        mean_fact_score = mean([float(record.get("score") or 0) for record in evaluated])
        full_pass_rate = (len(counted) / len(evaluated)) if evaluated else None
        mean_cost = distribution(evaluated, "cost")["mean"]
        expected_cost = (
            mean_cost / max(full_pass_rate, SCORE_EPSILON)
            if mean_cost is not None and full_pass_rate is not None
            else None
        )
        pipelines[side] = {
            "attempt_count": len(records),
            "status_counts": status_counts,
            "evaluated_count": len(evaluated),
            "valid_count": len(counted),
            "contributing_attempts": [
                {"attempt_id": record["attempt_id"], "session_id": record["session_id"]}
                for record in counted
            ],
            "evaluated_attempts": [
                {
                    "attempt_id": record["attempt_id"],
                    "session_id": record["session_id"],
                    "score": record.get("score"),
                }
                for record in evaluated
            ],
            "tokens_in": distribution(evaluated, "tokens_in"),
            "tokens_out": distribution(evaluated, "tokens_out"),
            "cost": distribution(evaluated, "cost"),
            "tool_call_count": distribution(evaluated, "tool_call_count"),
            "mean_fact_score": mean_fact_score,
            "full_pass_rate": full_pass_rate,
            "pass_rate": full_pass_rate,
            "expected_cost_per_correct": expected_cost,
        }
    deltas = {}
    for field in (
        "tokens_in",
        "tokens_out",
        "cost",
        "tool_call_count",
        "pass_rate",
        "mean_fact_score",
        "expected_cost_per_correct",
    ):
        baseline = (
            pipelines["baseline"][field]["mean"]
            if isinstance(pipelines["baseline"][field], dict)
            else pipelines["baseline"][field]
        )
        candidate = (
            pipelines["candidate"][field]["mean"]
            if isinstance(pipelines["candidate"][field], dict)
            else pipelines["candidate"][field]
        )
        deltas[field] = (
            baseline - candidate
            if baseline is not None and candidate is not None
            else None
        )
    baseline_tools = pipelines["baseline"]["tool_call_count"]["mean"]
    candidate_tools = pipelines["candidate"]["tool_call_count"]["mean"]
    tool_call_inflation = (
        candidate_tools / baseline_tools
        if baseline_tools not in (None, 0) and candidate_tools is not None
        else None
    )
    return {
        "schema_version": 1,
        "methodology_version": "v4-eval-matrix",
        "experiment_id": manifest["experiment_id"],
        "target_valid_per_pipeline": manifest["target_valid_per_pipeline"],
        "minimum_tool_calls": minimum_tool_calls,
        "pipelines": pipelines,
        "baseline_minus_candidate": deltas,
        "tool_call_inflation": tool_call_inflation,
        "exclusions": exclusions,
    }


def write_summary(experiment_dir):
    manifest_path = experiment_dir / "manifest.json"
    if not manifest_path.is_file():
        raise RuntimeError(f"manifest not found: {manifest_path}")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    summary = build_summary(manifest, load_attempts(experiment_dir))
    (experiment_dir / "summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return summary


def render_summary(summary):
    rows = [
        "side       full  eval  attempts  avg tokens in  avg cost    fact score  $/correct"
    ]
    rows.append(
        "---------  ----  ----  --------  -------------  ----------  ----------  ---------"
    )
    for side in SIDES:
        details = summary["pipelines"][side]
        tokens = details["tokens_in"]["mean"]
        cost = details["cost"]["mean"]
        fact_score = details.get("mean_fact_score")
        expected = details.get("expected_cost_per_correct")
        rows.append(
            f"{side:<9}  {details['valid_count']:>4}  {details.get('evaluated_count', 0):>4}  "
            f"{details['attempt_count']:>8}  "
            f"{tokens if tokens is not None else '-':>13}  "
            f"{cost if cost is not None else '-':>10}  "
            f"{fact_score if fact_score is not None else '-':>10}  "
            f"{expected if expected is not None else '-':>9}"
        )
    inflation = summary.get("tool_call_inflation")
    if inflation is not None:
        rows.append(f"tool_call_inflation={inflation}")
    return "\n".join(rows)


def list_sessions(ktx_bin, database):
    environment = os.environ.copy()
    environment["KTX_DB"] = str(database)
    output = command_output([ktx_bin, "list", "--format", "json"], env=environment)
    sessions = json.loads(output)
    if not isinstance(sessions, list):
        raise RuntimeError("ktx list did not return a JSON array")
    return sessions


def recorded_stream_links(database, session_id):
    if not database.exists():
        return []
    uri = database.resolve().as_uri() + "?mode=ro"
    try:
        with sqlite3.connect(uri, uri=True) as connection:
            rows = connection.execute(
                "SELECT id, bench_run_id FROM recorded_streams WHERE session_id = ? ORDER BY id",
                (session_id,),
            ).fetchall()
    except sqlite3.Error:
        return []
    return [{"id": row[0], "bench_run_id": row[1]} for row in rows]


def parse_stream(path, expected_output=None, expected_facts=None):
    metrics = {
        "stream_complete": False,
        "answer_matches_expected": False,
        "full_pass": False,
        "score": 0.0,
        "facts_matched": [],
        "facts_missed": [],
        "facts_total": 0,
        "tool_call_count": 0,
        "tokens_in": None,
        "tokens_out": None,
        "cost": None,
        "answer_excerpt": "",
        "usage_evidence": {
            "step_finish_events": 0,
            "tokens_in_events": 0,
            "tokens_out_events": 0,
            "cost_events": 0,
        },
    }
    tokens_in_total = 0
    tokens_out_total = 0
    cost_total = 0.0
    saw_tokens_in = False
    saw_tokens_out = False
    saw_cost = False
    last_text = ""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except UnicodeDecodeError:
        return metrics
    for line in lines:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        event_type = event.get("type")
        part = event.get("part") or {}
        if event_type == "tool_use":
            metrics["tool_call_count"] += 1
        elif event_type == "step_finish":
            metrics["usage_evidence"]["step_finish_events"] += 1
            tokens = part.get("tokens") or {}
            tokens_in = tokens.get("input")
            tokens_out = tokens.get("output")
            cost = part.get("cost")
            if isinstance(tokens_in, (int, float)):
                tokens_in_total += tokens_in
                saw_tokens_in = True
                metrics["usage_evidence"]["tokens_in_events"] += 1
            if isinstance(tokens_out, (int, float)):
                tokens_out_total += tokens_out
                saw_tokens_out = True
                metrics["usage_evidence"]["tokens_out_events"] += 1
            if isinstance(cost, (int, float)):
                cost_total += cost
                saw_cost = True
                metrics["usage_evidence"]["cost_events"] += 1
            if part.get("reason") == "stop":
                metrics["stream_complete"] = True
        elif event_type == "text":
            value = part.get("text") or part.get("content") or event.get("content")
            if isinstance(value, str):
                last_text = value
    answer = last_text.strip()
    scored = score_answer(
        answer,
        expected_output=expected_output,
        expected_facts=expected_facts,
    )
    metrics.update(scored)
    metrics["answer_excerpt"] = answer[:500]
    step_finish_events = metrics["usage_evidence"]["step_finish_events"]
    metrics["tokens_in"] = (
        tokens_in_total
        if saw_tokens_in and metrics["usage_evidence"]["tokens_in_events"] == step_finish_events
        else None
    )
    metrics["tokens_out"] = (
        tokens_out_total
        if saw_tokens_out and metrics["usage_evidence"]["tokens_out_events"] == step_finish_events
        else None
    )
    metrics["cost"] = (
        cost_total
        if saw_cost and metrics["usage_evidence"]["cost_events"] == step_finish_events
        else None
    )
    return metrics


def append_attempt(path, record):
    with path.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(record, sort_keys=True) + "\n")


def attempt_status(exit_code, new_session_ids, metrics):
    if exit_code in (124, 137):
        return "timeout"
    if exit_code != 0:
        return "failed"
    if len(new_session_ids) != 1:
        return "unlinked"
    if not metrics["stream_complete"]:
        return "invalid"
    return "completed"


def run_attempt(args, manifest, experiment_dir, side, cycle, ordinal, docker_bin, ktx_bin):
    attempt_id = f"{side}-{ordinal:03d}"
    attempt_dir = experiment_dir / "attempts" / attempt_id
    attempt_dir.mkdir()
    database = attempt_dir / "ktx.db"
    stream = attempt_dir / "stream.jsonl"
    stderr = attempt_dir / "stderr.log"
    workspace = attempt_dir / "workspace"
    database.touch()
    repository_root = experiment_dir / manifest["repository"]["snapshot"]["path"]
    workspace_meta = prepare_attempt_workspace(
        repository_root,
        workspace,
        mode=manifest["task"].get("workspace_mode", "repo"),
        corpus_files=manifest["task"].get("corpus_files") or [],
    )

    before = list_sessions(ktx_bin, database)
    (attempt_dir / "sessions-before.json").write_text(
        json.dumps(before, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    expected_stages = json.dumps(manifest["pipelines"][side]["stages"], separators=(",", ":"))
    started_at = utc_now()
    command = [
        "timeout",
        "--foreground",
        f"--kill-after={args.termination_grace}",
        args.attempt_timeout,
        docker_bin,
        "run",
        "--rm",
        "-e",
        f"KTX_EXPECTED_STAGES_JSON={expected_stages}",
        "-v",
        f"{args.auth}:/home/node/.local/share/opencode/auth.json:ro",
        "-v",
        f"{manifest['pipelines'][side]['path']}:/home/node/.config/ktx/ktx.toml:ro",
        "-v",
        f"{database}:/home/node/.local/share/ktx/ktx.db:rw",
        "-v",
        f"{workspace}:/work:rw",
        args.image,
        "-m",
        args.model,
        args.prompt,
    ]
    with stream.open("w", encoding="utf-8") as stdout_handle, stderr.open(
        "w", encoding="utf-8"
    ) as stderr_handle:
        result = subprocess.run(command, stdout=stdout_handle, stderr=stderr_handle)
    ended_at = utc_now()
    workspace_metadata = {
        **directory_sha256(workspace),
        "mode": workspace_meta.get("mode"),
        "files": workspace_meta.get("files"),
    }

    after = list_sessions(ktx_bin, database)
    (attempt_dir / "sessions-after.json").write_text(
        json.dumps(after, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    before_ids = {session.get("id") for session in before if session.get("id")}
    after_ids = {session.get("id") for session in after if session.get("id")}
    new_session_ids = sorted(after_ids - before_ids)
    metrics = parse_stream(
        stream,
        expected_output=args.expected_output,
        expected_facts=getattr(args, "expected_facts", None),
    )
    status = attempt_status(result.returncode, new_session_ids, metrics)
    session_id = new_session_ids[0] if len(new_session_ids) == 1 else None
    recorded_streams = recorded_stream_links(database, session_id) if session_id else []
    recorded_stream = recorded_streams[0] if len(recorded_streams) == 1 else None
    record = {
        "schema_version": ATTEMPT_SCHEMA_VERSION,
        "experiment_id": manifest["experiment_id"],
        "attempt_id": attempt_id,
        "side": side,
        "pipeline_id": manifest["pipelines"][side].get("pipeline_id", side),
        "cycle": cycle,
        "ordinal": ordinal,
        "started_at": started_at,
        "ended_at": ended_at,
        "status": status,
        "process_exit_code": result.returncode,
        "session_link": {
            "state": "linked" if session_id else "unlinked",
            "new_session_count": len(new_session_ids),
            "new_session_ids": new_session_ids,
        },
        "session_id": session_id,
        "recorded_streams": recorded_streams,
        "recorded_stream_id": recorded_stream["id"] if recorded_stream else None,
        "bench_run_id": recorded_stream["bench_run_id"] if recorded_stream else None,
        **metrics,
        "config_sha256": manifest["pipelines"][side]["sha256"],
        "artifacts": {
            "database": str(database.relative_to(experiment_dir)),
            "stream": str(stream.relative_to(experiment_dir)),
            "stderr": str(stderr.relative_to(experiment_dir)),
            "workspace": str(workspace.relative_to(experiment_dir)),
            "sessions_before": str((attempt_dir / "sessions-before.json").relative_to(experiment_dir)),
            "sessions_after": str((attempt_dir / "sessions-after.json").relative_to(experiment_dir)),
        },
        "content_hashes": {
            "stream": sha256_file(stream),
            "stderr": sha256_file(stderr),
            "workspace": workspace_metadata["sha256"],
        },
    }
    append_attempt(experiment_dir / "attempts.jsonl", record)
    return record


def count_valid(attempts, minimum_tool_calls):
    return {
        side: sum(
            attempt_is_counted(record, minimum_tool_calls)
            for record in attempts
            if record.get("side") == side
        )
        for side in SIDES
    }


def create_manifest(args, experiment_dir, docker_bin, configs, snapshot_metadata):
    manifest = {
        "schema_version": MANIFEST_SCHEMA_VERSION,
        "experiment_id": experiment_dir.name,
        "created_at": utc_now(),
        "task": {
            "id": args.task_id,
            "prompt": args.prompt,
            "expected_output": args.expected_output,
            "expected_facts": getattr(args, "expected_facts", None),
            "scoring_kind": (
                "multi_fact_fractional"
                if getattr(args, "expected_facts", None)
                else "exact_match"
            ),
            "minimum_tool_calls": args.min_tool_calls,
            "task_file": getattr(args, "task_file", None),
            "skill": getattr(args, "task_skill", None),
            "token_band": getattr(args, "task_token_band", None),
            "workspace_mode": getattr(args, "workspace_mode", "repo"),
            "corpus_files": getattr(args, "corpus_files", []) or [],
        },
        "model": args.model,
        "target_valid_per_pipeline": args.target_valid,
        "max_attempts": args.max_attempts,
        "attempt_timeout": args.attempt_timeout,
        "termination_grace": args.termination_grace,
        "pipeline_execution_policy": {
            "kind": "alternating_by_cycle",
            "first_cycle": ["baseline", "candidate"],
            "second_cycle": ["candidate", "baseline"],
        },
        "repository": {
            **repository_metadata(args.repo_root),
            "snapshot": {
                "path": "repository",
                **snapshot_metadata,
            },
        },
        "image": {
            "name": args.image,
            "id": docker_image_id(docker_bin, args.image),
        },
        "identities": docker_identity(docker_bin, args.image, configs["baseline"]["path"]),
        "pipelines": configs,
    }
    (experiment_dir / "manifest.json").write_text(
        json.dumps(manifest, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return manifest


def apply_task_arguments(args):
    args.expected_facts = None
    if getattr(args, "expected_facts_raw", None):
        try:
            parsed_facts = json.loads(args.expected_facts_raw)
        except json.JSONDecodeError as error:
            raise RuntimeError(f"--expected-facts must be a JSON array: {error}") from error
        if not isinstance(parsed_facts, list) or not all(
            isinstance(item, str) for item in parsed_facts
        ):
            raise RuntimeError("--expected-facts must be a JSON array of strings")
        args.expected_facts = parsed_facts
    if args.task_file:
        task = load_task_file(args.task_file)
        args.task_id = args.task_id or task["id"]
        args.prompt = args.prompt or task["prompt"]
        if args.min_tool_calls is None:
            args.min_tool_calls = task["minimum_tool_calls"]
        if task["expected_facts"] is not None:
            args.expected_facts = task["expected_facts"]
            args.expected_output = None
        else:
            args.expected_output = args.expected_output or task["expected_output"]
        args.task_skill = task.get("skill")
        args.task_token_band = task.get("token_band")
        args.task_file = task["task_file"]
        args.corpus_files = task.get("corpus_files") or []
        if args.workspace_mode is None:
            args.workspace_mode = task.get("workspace_mode") or "corpus"
    else:
        args.task_skill = None
        args.task_token_band = None
        args.task_file = None
        args.corpus_files = []
        if args.workspace_mode is None:
            args.workspace_mode = "repo"
    if args.min_tool_calls is None:
        args.min_tool_calls = 1


def validate_run_arguments(args):
    apply_task_arguments(args)
    missing = [
        flag
        for flag, value in (
            ("--model", args.model),
            ("--prompt", args.prompt),
            ("--task-id", args.task_id),
        )
        if not value
    ]
    if missing:
        raise RuntimeError(f"missing required arguments: {', '.join(missing)}")
    if args.expected_facts is None and not args.expected_output:
        raise RuntimeError("provide --expected-output, --expected-facts, or --task-file")
    if args.expected_facts is not None and args.expected_output:
        raise RuntimeError("use either multi-fact scoring or --expected-output, not both")
    if not args.model.startswith("neuralwatt/"):
        raise RuntimeError("model must use the neuralwatt/ provider")
    if args.target_valid * len(SIDES) > args.max_attempts:
        raise RuntimeError("--max-attempts must allow the requested completions")
    if not args.auth.is_file():
        raise RuntimeError(f"OpenCode auth file not found: {args.auth}")


def main(argv=None):
    args = parser().parse_args(argv)
    if args.summarize:
        experiment_dir = Path(args.summarize).resolve()
        summary = write_summary(experiment_dir)
        if args.format == "json":
            print(json.dumps(summary, indent=2, sort_keys=True))
        else:
            print(render_summary(summary))
        return 0

    repo_root = Path(args.repo_root).resolve() if args.repo_root else Path(__file__).resolve().parent.parent
    args.repo_root = repo_root
    args.data_dir = (
        Path(args.data_dir).resolve()
        if args.data_dir
        else Path(os.environ.get("KTX_BENCH_DATA", repo_root / ".bench-data")).resolve()
    )
    args.auth = Path(args.auth).expanduser().resolve() if args.auth else Path.home() / ".local/share/opencode/auth.json"
    validate_run_arguments(args)
    docker_bin = command_path("docker")
    ktx_bin = command_path("ktx", repo_root / "ktx")
    if args.build:
        subprocess.run(
            [docker_bin, "build", "-f", str(repo_root / "Dockerfile.benchmark"), "-t", args.image, str(repo_root)],
            check=True,
        )
    docker_image_id(docker_bin, args.image)

    selected_id = args.experiment_id or experiment_id(repo_root)
    experiment_dir = args.data_dir / selected_id
    experiment_dir.parent.mkdir(parents=True, exist_ok=True)
    try:
        experiment_dir.mkdir()
    except FileExistsError as error:
        raise RuntimeError(f"experiment directory already exists: {experiment_dir}") from error
    (experiment_dir / "attempts").mkdir()

    snapshot_metadata = snapshot_repository(
        args.repo_root,
        experiment_dir / "repository",
        args.data_dir,
    )
    baseline_config = (
        Path(args.baseline_config).resolve()
        if args.baseline_config
        else repo_root / "tools" / "bench" / "ktx.toml.baseline"
    )
    candidate_config = (
        Path(args.candidate_config).resolve()
        if args.candidate_config
        else repo_root / "tools" / "bench" / "ktx.toml.candidate"
    )
    configs = snapshot_configs(
        experiment_dir,
        {"baseline": baseline_config, "candidate": candidate_config},
    )
    manifest = create_manifest(args, experiment_dir, docker_bin, configs, snapshot_metadata)
    attempts = []
    ordinals = {side: 0 for side in SIDES}
    cycle = 1
    while len(attempts) < args.max_attempts:
        valid = count_valid(attempts, args.min_tool_calls)
        if all(valid[side] >= args.target_valid for side in SIDES):
            break
        order = SIDES if cycle % 2 else tuple(reversed(SIDES))
        for side in order:
            if len(attempts) >= args.max_attempts:
                break
            if valid[side] >= args.target_valid:
                continue
            ordinals[side] += 1
            record = run_attempt(
                args, manifest, experiment_dir, side, cycle, ordinals[side], docker_bin, ktx_bin
            )
            attempts.append(record)
            valid = count_valid(attempts, args.min_tool_calls)
        cycle += 1

    summary = write_summary(experiment_dir)
    if args.format == "json":
        print(json.dumps(summary, indent=2, sort_keys=True))
    else:
        print(render_summary(summary))
    if all(summary["pipelines"][side]["valid_count"] >= args.target_valid for side in SIDES):
        return 0
    return 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RuntimeError as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(2)
