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


ARMS = ("baseline", "treatment")
ATTEMPT_STATUSES = ("completed", "timeout", "failed", "unlinked", "invalid")
MANIFEST_SCHEMA_VERSION = 1
ATTEMPT_SCHEMA_VERSION = 1


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
    result.add_argument("--expected-output")
    result.add_argument("--min-tool-calls", type=nonnegative_integer, default=1)
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


def snapshot_configs(repo_root, experiment_dir):
    configs_dir = experiment_dir / "configs"
    configs_dir.mkdir()
    configs = {}
    for arm in ARMS:
        source = repo_root / "tools" / "bench" / f"ktx.toml.{arm}"
        destination = configs_dir / source.name
        shutil.copy2(source, destination)
        configs[arm] = {
            **config_metadata(destination),
            "source_path": str(source),
        }
    return configs


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


def attempt_is_counted(record, minimum_tool_calls):
    return (
        record.get("status") == "completed"
        and bool(record.get("session_id"))
        and record.get("stream_complete") is True
        and record.get("answer_matches_expected") is True
        and record.get("tool_call_count", 0) >= minimum_tool_calls
    )


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
    if record.get("answer_matches_expected") is not True:
        reasons.append("answer mismatch")
    if record.get("tool_call_count", 0) < minimum_tool_calls:
        reasons.append("too few tool calls")
    return reasons


def build_summary(manifest, attempts):
    minimum_tool_calls = manifest["task"]["minimum_tool_calls"]
    grouped = {arm: [record for record in attempts if record.get("arm") == arm] for arm in ARMS}
    arms = {}
    exclusions = []
    for arm in ARMS:
        records = grouped[arm]
        counted = [record for record in records if attempt_is_counted(record, minimum_tool_calls)]
        status_counts = {status: 0 for status in ATTEMPT_STATUSES}
        for record in records:
            status = record.get("status")
            if status in status_counts:
                status_counts[status] += 1
            if record not in counted:
                exclusions.append(
                    {
                        "attempt_id": record.get("attempt_id"),
                        "arm": arm,
                        "reasons": exclusion_reason(record, minimum_tool_calls),
                    }
                )
        arms[arm] = {
            "attempt_count": len(records),
            "status_counts": status_counts,
            "valid_count": len(counted),
            "contributing_attempts": [
                {"attempt_id": record["attempt_id"], "session_id": record["session_id"]}
                for record in counted
            ],
            "tokens_in": distribution(counted, "tokens_in"),
            "tokens_out": distribution(counted, "tokens_out"),
            "cost": distribution(counted, "cost"),
            "tool_call_count": distribution(counted, "tool_call_count"),
            "pass_rate": (
                sum(record.get("score", 0) for record in counted) / len(counted)
                if counted
                else None
            ),
        }
    deltas = {}
    for field in ("tokens_in", "tokens_out", "cost", "tool_call_count", "pass_rate"):
        baseline = (
            arms["baseline"][field]["mean"]
            if isinstance(arms["baseline"][field], dict)
            else arms["baseline"][field]
        )
        treatment = (
            arms["treatment"][field]["mean"]
            if isinstance(arms["treatment"][field], dict)
            else arms["treatment"][field]
        )
        deltas[field] = (
            baseline - treatment
            if baseline is not None and treatment is not None
            else None
        )
    return {
        "schema_version": 1,
        "experiment_id": manifest["experiment_id"],
        "target_valid_per_arm": manifest["target_valid_per_arm"],
        "minimum_tool_calls": minimum_tool_calls,
        "arms": arms,
        "baseline_minus_treatment": deltas,
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
    rows = ["arm        valid  attempts  avg tokens in  avg cost       pass rate"]
    rows.append("---------  -----  --------  -------------  -------------  ---------")
    for arm in ARMS:
        details = summary["arms"][arm]
        tokens = details["tokens_in"]["mean"]
        cost = details["cost"]["mean"]
        pass_rate = details["pass_rate"]
        rows.append(
            f"{arm:<9}  {details['valid_count']:>5}  {details['attempt_count']:>8}  "
            f"{tokens if tokens is not None else '-':>13}  "
            f"{cost if cost is not None else '-':>13}  "
            f"{pass_rate if pass_rate is not None else '-':>9}"
        )
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


def parse_stream(path, expected_output):
    metrics = {
        "stream_complete": False,
        "answer_matches_expected": False,
        "score": 0,
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
    metrics["answer_matches_expected"] = answer == expected_output
    metrics["score"] = int(metrics["answer_matches_expected"])
    metrics["answer_excerpt"] = answer[:200]
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


def run_attempt(args, manifest, experiment_dir, arm, cycle, ordinal, docker_bin, ktx_bin):
    attempt_id = f"{arm}-{ordinal:03d}"
    attempt_dir = experiment_dir / "attempts" / attempt_id
    attempt_dir.mkdir()
    database = attempt_dir / "ktx.db"
    stream = attempt_dir / "stream.jsonl"
    stderr = attempt_dir / "stderr.log"
    workspace = attempt_dir / "workspace"
    database.touch()
    snapshot_repository(
        experiment_dir / manifest["repository"]["snapshot"]["path"],
        workspace,
        experiment_dir / manifest["repository"]["snapshot"]["path"],
    )

    before = list_sessions(ktx_bin, database)
    (attempt_dir / "sessions-before.json").write_text(
        json.dumps(before, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    expected_stages = json.dumps(manifest["arms"][arm]["stages"], separators=(",", ":"))
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
        f"{manifest['arms'][arm]['path']}:/home/node/.config/ktx/ktx.toml:ro",
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
    workspace_metadata = directory_sha256(workspace)

    after = list_sessions(ktx_bin, database)
    (attempt_dir / "sessions-after.json").write_text(
        json.dumps(after, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    before_ids = {session.get("id") for session in before if session.get("id")}
    after_ids = {session.get("id") for session in after if session.get("id")}
    new_session_ids = sorted(after_ids - before_ids)
    metrics = parse_stream(stream, args.expected_output)
    status = attempt_status(result.returncode, new_session_ids, metrics)
    session_id = new_session_ids[0] if len(new_session_ids) == 1 else None
    recorded_streams = recorded_stream_links(database, session_id) if session_id else []
    recorded_stream = recorded_streams[0] if len(recorded_streams) == 1 else None
    record = {
        "schema_version": ATTEMPT_SCHEMA_VERSION,
        "experiment_id": manifest["experiment_id"],
        "attempt_id": attempt_id,
        "arm": arm,
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
        "config_sha256": manifest["arms"][arm]["sha256"],
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
        arm: sum(
            attempt_is_counted(record, minimum_tool_calls)
            for record in attempts
            if record.get("arm") == arm
        )
        for arm in ARMS
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
            "minimum_tool_calls": args.min_tool_calls,
        },
        "model": args.model,
        "target_valid_per_arm": args.target_valid,
        "max_attempts": args.max_attempts,
        "attempt_timeout": args.attempt_timeout,
        "termination_grace": args.termination_grace,
        "arm_execution_policy": {
            "kind": "alternating_by_cycle",
            "first_cycle": ["baseline", "treatment"],
            "second_cycle": ["treatment", "baseline"],
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
        "arms": configs,
    }
    (experiment_dir / "manifest.json").write_text(
        json.dumps(manifest, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return manifest


def validate_run_arguments(args):
    missing = [
        flag
        for flag, value in (
            ("--model", args.model),
            ("--prompt", args.prompt),
            ("--task-id", args.task_id),
            ("--expected-output", args.expected_output),
        )
        if not value
    ]
    if missing:
        raise RuntimeError(f"missing required arguments: {', '.join(missing)}")
    if not args.model.startswith("neuralwatt/"):
        raise RuntimeError("model must use the neuralwatt/ provider")
    if args.target_valid * len(ARMS) > args.max_attempts:
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
    configs = snapshot_configs(repo_root, experiment_dir)
    manifest = create_manifest(args, experiment_dir, docker_bin, configs, snapshot_metadata)
    attempts = []
    ordinals = {arm: 0 for arm in ARMS}
    cycle = 1
    while len(attempts) < args.max_attempts:
        valid = count_valid(attempts, args.min_tool_calls)
        if all(valid[arm] >= args.target_valid for arm in ARMS):
            break
        order = ARMS if cycle % 2 else tuple(reversed(ARMS))
        for arm in order:
            if len(attempts) >= args.max_attempts:
                break
            if valid[arm] >= args.target_valid:
                continue
            ordinals[arm] += 1
            record = run_attempt(
                args, manifest, experiment_dir, arm, cycle, ordinals[arm], docker_bin, ktx_bin
            )
            attempts.append(record)
            valid = count_valid(attempts, args.min_tool_calls)
        cycle += 1

    summary = write_summary(experiment_dir)
    if args.format == "json":
        print(json.dumps(summary, indent=2, sort_keys=True))
    else:
        print(render_summary(summary))
    if all(summary["arms"][arm]["valid_count"] >= args.target_valid for arm in ARMS):
        return 0
    return 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RuntimeError as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(2)
