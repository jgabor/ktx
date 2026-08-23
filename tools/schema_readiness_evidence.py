#!/usr/bin/env python3
"""Generate disposable schema-readiness fixtures and raw measurement evidence."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import sqlite3
import string
import subprocess
import sys
import tempfile
import time


PAYLOAD_COUNT = 3200
PAYLOAD_BYTES = 1_000_000
TIMESTAMP_START_NS = 1_704_067_200_000_000_000
TIMESTAMP_STEP_NS = 19_719_224_757_757
SAMPLES = 5
CPU = "2"
PREPARED_MANIFEST = "prepared-fixtures.json"
APPROVED_LIMITS = {
    "readiness_latency_ns": 35_322_974,
    "exclusive_lock_hold_ns": 101_037_622,
    "journal_growth_bytes": 10_900,
    "peak_rss_bytes": 26_557_440,
    "total_completion_ns": 113_236_942,
}


def progress(phase, message):
    print(f"[{phase}] {message}", file=sys.stderr, flush=True)


def roots(parent, name):
    root = parent / name
    env = os.environ.copy()
    env.pop("KTX_DB", None)
    for key, directory in {
        "HOME": "home",
        "XDG_DATA_HOME": "data",
        "XDG_CONFIG_HOME": "config",
        "XDG_CACHE_HOME": "cache",
        "XDG_STATE_HOME": "state",
        "TMPDIR": "tmp",
    }.items():
        path = root / directory
        path.mkdir(parents=True, exist_ok=True)
        env[key] = str(path)
    env["KTX_VERIFY_ROOT"] = str(parent)
    env["GOMAXPROCS"] = "1"
    return env


def run(command, env, input_text=None):
    return subprocess.run(
        command, env=env, input=input_text, text=True, capture_output=True, check=True
    )


def copy_fixture(source, destination, env, phase):
    progress(phase, f"copying {source.name} to {destination}")
    run(
        [
            "cp",
            "--reflink=auto",
            "--sparse=always",
            "--",
            str(source),
            str(destination),
        ],
        env,
    )
    progress(phase, f"copied {source.name}")


def digest(path, label):
    value = hashlib.sha256()
    size = path.stat().st_size
    processed = 0
    next_progress = 256 * 1024 * 1024
    with path.open("rb") as source:
        while block := source.read(1024 * 1024):
            value.update(block)
            processed += len(block)
            if processed >= next_progress:
                progress("prepare", f"{label} hash {processed}/{size} bytes")
                next_progress += 256 * 1024 * 1024
    return value.hexdigest()


def alpha_id(index):
    alphabet = string.ascii_lowercase
    return "p" + "".join(alphabet[(index // (26**power)) % 26] for power in range(14, -1, -1))


def generate_fixtures(binary, parent, payload_count, payload_bytes):
    progress("prepare", "creating current-schema seed")
    current = parent / "current.db"
    env = roots(parent, "fixture-current")
    env["KTX_DB"] = str(current)
    seed = json.dumps(
        {
            "id": "aaaaaaaaaaaaaaaa",
            "tool": "read",
            "timestamp": "2026-01-01T00:00:00Z",
            "input": "fixture-seed",
            "output": "seed",
        }
    )
    run([str(binary), "write"], env, seed)

    database = sqlite3.connect(current)
    database.execute("PRAGMA journal_mode=OFF")
    database.execute("PRAGMA synchronous=OFF")
    session = database.execute("SELECT id FROM sessions LIMIT 1").fetchone()[0]
    database.execute(
        "UPDATE tool_calls SET session_id='ssssssssssssssss' WHERE session_id=?", (session,)
    )
    database.execute(
        """UPDATE sessions SET id='ssssssssssssssss',agent='unknown',
        started_at='2026-01-01T00:00:00Z',ended_at='',
        last_seen_at='2026-01-01T00:00:00Z',tool_call_count=1 WHERE id=?""",
        (session,),
    )
    session = "ssssssssssssssss"
    database.commit()
    minimal = parent / "minimal.db"
    minimal_database = sqlite3.connect(minimal)
    database.backup(minimal_database)
    minimal_database.close()
    database.execute("BEGIN")
    for start in range(0, payload_count, 32):
        rows = []
        for index in range(start, min(start + 32, payload_count)):
            timestamp = TIMESTAMP_START_NS + index * TIMESTAMP_STEP_NS
            rows.append(
                (
                    alpha_id(index),
                    session,
                    "read",
                    f"fixture-{index}",
                    payload_bytes,
                    "0" * 64,
                    timestamp,
                    timestamp,
                )
            )
        database.executemany(
            """INSERT INTO tool_calls(
                id,session_id,tool,input,output,content_hash,timestamp,
                timestamp_unix_nano,event_type,latency_state,latency_unit,
                latency_authority
            ) VALUES(?,?,?,?,CAST(zeroblob(?) AS TEXT),?,
                datetime(?/1000000000,'unixepoch'),?,'tool_call','unknown',
                'milliseconds','unknown')""",
            rows,
        )
        progress("prepare", f"inserted {min(start + 32, payload_count)}/{payload_count} payload rows")
    database.commit()
    database.execute("PRAGMA journal_mode=DELETE")
    database.close()

    schema15 = parent / "schema15.db"
    copy_fixture(current, schema15, roots(parent, "fixture-schema15-copy"), "prepare")
    progress("prepare", "creating physical schema-15 fixture")
    database = sqlite3.connect(schema15)
    database.executescript(
        """BEGIN;
        INSERT INTO compressed(
            id,source_id,output,content_hash,pipeline,timestamp,
            timestamp_unix_nano,producer
        )
        SELECT substr(id,2,15)||char(97+(rowid%26)),id,
            CAST(zeroblob(10) AS TEXT),printf('%064d',0),'{}',timestamp,
            timestamp_unix_nano,'hook'
        FROM tool_calls WHERE substr(id,1,1)='p';
        ALTER TABLE compressed DROP COLUMN timestamp_unix_nano;
        PRAGMA user_version=15;
        COMMIT;"""
    )
    database.close()
    fixtures = {
        "minimal": fixture_record(minimal, "tool_calls", "timestamp", "minimal"),
        "current": fixture_record(current, "tool_calls", "timestamp", "current"),
        "schema15": fixture_record(schema15, "compressed", "timestamp", "schema15"),
    }
    manifest = {
        "schema_version": 1,
        "fixture_inputs": {
            "payload_count": payload_count,
            "payload_bytes_each": payload_bytes,
            "timestamp_start_unix_nano": TIMESTAMP_START_NS,
            "timestamp_step_nanoseconds": TIMESTAMP_STEP_NS,
        },
        "fixtures": fixtures,
    }
    manifest_path = parent / PREPARED_MANIFEST
    temporary = manifest_path.with_suffix(".tmp")
    temporary.write_text(json.dumps(manifest, indent=2) + "\n")
    os.replace(temporary, manifest_path)
    progress("prepare", f"prepared fixtures recorded in {manifest_path}")


def rss_tree(pid):
    pending = [pid]
    seen = set()
    total = 0
    while pending:
        current = pending.pop()
        if current in seen:
            continue
        seen.add(current)
        try:
            status = Path(f"/proc/{current}/status").read_text()
            match = re.search(r"^VmRSS:\s+(\d+) kB$", status, re.MULTILINE)
            if match:
                total += int(match.group(1)) * 1024
            children = Path(f"/proc/{current}/task/{current}/children").read_text()
            pending.extend(int(child) for child in children.split())
        except (FileNotFoundError, ProcessLookupError):
            pass
    return total


def exclusive_lock_observations(stderr, lock_kind):
    active = {}
    starts = []
    intervals = []
    for line in stderr.splitlines():
        timestamp = re.search(r"(?:\[pid +(\d+)\] )?(\d+\.\d+)", line)
        if not timestamp:
            continue
        pid = timestamp.group(1) or "main"
        stamp = float(timestamp.group(2))
        if lock_kind == "flock":
            lock = re.search(
                r"flock\((\d+), (LOCK_EX|LOCK_SH|LOCK_UN)(?:\|LOCK_NB)?\)\s+= 0$",
                line,
            )
            if not lock:
                continue
            key = (pid, lock.group(1))
            exclusive = lock.group(2) == "LOCK_EX"
        else:
            lock = re.search(
                r"fcntl\((\d+), F_SETLKW?, \{l_type=(F_WRLCK|F_RDLCK|F_UNLCK).*\}\)\s+= 0$",
                line,
            )
            if not lock:
                continue
            key = (pid, lock.group(1))
            exclusive = lock.group(2) == "F_WRLCK"
        if exclusive:
            if key not in active:
                active[key] = stamp
                starts.append(stamp)
        elif key in active:
            intervals.append(round((stamp - active.pop(key)) * 1e9))
    return starts, intervals


def measure(command, env, database, lock_kind):
    started_epoch = time.time()
    started_monotonic = time.perf_counter_ns()
    with tempfile.TemporaryFile(mode="w+", encoding="utf-8") as stdout_capture, tempfile.TemporaryFile(
        mode="w+", encoding="utf-8"
    ) as stderr_capture:
        process = subprocess.Popen(
            ["taskset", "-c", CPU, "strace", "-f", "-ttt", "-e", "trace=fcntl,flock", *command],
            env=env,
            stdout=stdout_capture,
            stderr=stderr_capture,
        )
        peak_rss = 0
        peak_journal = 0
        sidecars = [Path(str(database) + suffix) for suffix in ("-journal", "-wal", "-shm")]
        while process.poll() is None:
            peak_rss = max(peak_rss, rss_tree(process.pid))
            peak_journal = max(
                peak_journal, sum(path.stat().st_size for path in sidecars if path.exists())
            )
            time.sleep(0.0005)
        process.wait()
        stdout_capture.seek(0)
        stderr_capture.seek(0)
        stdout = stdout_capture.read()
        stderr = stderr_capture.read()
    completion = time.perf_counter_ns() - started_monotonic
    if process.returncode:
        raise RuntimeError(stderr)

    lock_starts, lock_intervals = exclusive_lock_observations(stderr, lock_kind)
    lock_hold = max(lock_intervals, default=0)
    readiness = (
        max(0, round((min(lock_starts) - started_epoch) * 1e9))
        if lock_kind == "flock" and lock_starts
        else completion
    )
    return {
        "readiness_latency_ns": readiness,
        "exclusive_lock_hold_ns": lock_hold,
        "journal_growth_bytes": peak_journal,
        "peak_rss_bytes": peak_rss,
        "total_completion_ns": completion,
        "exclusive_lock_intervals_ns": lock_intervals,
        "stdout": stdout.strip(),
    }


def fixture_record(path, table, timestamp_column, label):
    progress("prepare", f"checking {label} fixture integrity")
    database = sqlite3.connect(path)
    integrity = database.execute("PRAGMA integrity_check").fetchone()[0]
    version = database.execute("PRAGMA user_version").fetchone()[0]
    count, payload, first, last = database.execute(
        f"SELECT count(*),sum(length(CAST(output AS BLOB))),min({timestamp_column}),max({timestamp_column}) FROM {table}"
    ).fetchone()
    database.close()
    return {
        "path_basename": path.name,
        "bytes": path.stat().st_size,
        "sha256": digest(path, label),
        "schema": version,
        "rows": count,
        "payload_bytes": payload,
        "first_timestamp": first,
        "last_timestamp": last,
        "integrity_check": integrity,
    }


def load_prepared(parent):
    manifest_path = parent / PREPARED_MANIFEST
    manifest = json.loads(manifest_path.read_text())
    if manifest.get("schema_version") != 1:
        raise SystemExit(f"unsupported prepared-fixture manifest: {manifest_path}")
    paths = {
        name: parent / record["path_basename"]
        for name, record in manifest["fixtures"].items()
    }
    missing = [str(path) for path in paths.values() if not path.is_file()]
    if missing:
        raise SystemExit("prepared fixture missing: " + ", ".join(missing))
    return manifest, paths


def run_workloads(binary, parent, fixtures, phase, samples):
    run_name = f"{phase}-{os.getpid()}-{time.time_ns()}"
    workloads = {}
    for name, database in (
        ("current-minimal", fixtures["minimal"]),
        ("current-production", fixtures["current"]),
    ):
        observations = []
        for index in range(samples):
            progress(phase, f"{name} sample {index + 1}/{samples}")
            env = roots(parent, f"{run_name}/{name}-{index}")
            env["KTX_DB"] = str(database)
            sample = measure([str(binary), "list", "--format", "json"], env, database, "fcntl")
            sample["kind"] = "smoke" if phase == "smoke" else ("warmup" if index == 0 else "measured")
            observations.append(sample)
            progress(phase, f"{name} sample {index + 1} complete; max lock {sample['exclusive_lock_hold_ns']} ns")
        workloads[name] = observations

    migration_samples = []
    for index in range(samples):
        progress(phase, f"schema15-to-16-production sample {index + 1}/{samples}")
        root = parent / run_name / f"migration-{index}"
        env = roots(parent, f"{run_name}/migration-{index}-roots")
        database = root / "schema15.db"
        root.mkdir(parents=True)
        copy_fixture(fixtures["schema15"], database, env, phase)
        sample = measure(
            [str(binary), "db", "--path", str(database), "migrate"], env, database, "flock"
        )
        sample["kind"] = "smoke" if phase == "smoke" else ("warmup" if index == 0 else "measured")
        with sqlite3.connect(database) as migrated:
            sample["final_schema"] = migrated.execute("PRAGMA user_version").fetchone()[0]
        migration_samples.append(sample)
        progress(phase, f"schema15-to-16-production sample {index + 1} complete; max lock {sample['exclusive_lock_hold_ns']} ns")
    workloads["schema15-to-16-production"] = migration_samples
    return workloads


def measure_prepared(binary, parent, output):
    manifest, fixtures = load_prepared(parent)
    workloads = run_workloads(binary, parent, fixtures, "measure", SAMPLES + 1)

    metrics = (
        "readiness_latency_ns",
        "exclusive_lock_hold_ns",
        "journal_growth_bytes",
        "peak_rss_bytes",
        "total_completion_ns",
    )
    measured = [sample for samples in workloads.values() for sample in samples if sample["kind"] == "measured"]
    worst = {metric: max(sample[metric] for sample in measured) for metric in metrics}
    limit_checks = {
        metric: {
            "observed": worst[metric],
            "limit": APPROVED_LIMITS[metric],
            "passed": worst[metric] <= APPROVED_LIMITS[metric],
        }
        for metric in metrics
    }
    capabilities = json.loads(run([str(binary), "binary-capabilities"], roots(parent, "measure-capabilities")).stdout)
    report = {
        "schema_version": 2,
        "policy": {
            "cpu": int(CPU),
            "gomaxprocs": 1,
            "warmups_per_workload": 1,
            "measured_samples_per_workload": SAMPLES,
            "poll_interval_seconds": 0.0005,
            "limit": "approved fixed limits from the retained controlled-host baseline",
            "current_readiness_boundary": "process start through command completion",
            "migration_readiness_boundary": "process start through first successful LOCK_EX",
            "lock_hold_boundary": "maximum individual successful exclusive-lock interval",
            "copy_strategy": "cp --reflink=auto --sparse=always",
        },
        "host": {
            "node": platform.node(),
            "platform": platform.platform(),
            "machine": platform.machine(),
            "processor": platform.processor(),
        },
        "binary": capabilities,
        "fixture_inputs": manifest["fixture_inputs"],
        "fixtures": manifest["fixtures"],
        "workloads": workloads,
        "observed_worst": worst,
        "release_limits": APPROVED_LIMITS,
        "limit_checks": limit_checks,
        "release_gate_passed": all(check["passed"] for check in limit_checks.values()),
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + "\n")
    progress("measure", f"wrote {output}")
    if not report["release_gate_passed"]:
        failed = ", ".join(metric for metric, check in limit_checks.items() if not check["passed"])
        raise SystemExit(f"schema readiness release limits exceeded: {failed}")


def positive_int(value):
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def main():
    parser = argparse.ArgumentParser()
    phases = parser.add_subparsers(dest="phase", required=True)
    for name in ("prepare", "smoke", "measure"):
        phase = phases.add_parser(name)
        phase.add_argument("--binary", required=True, type=Path)
        phase.add_argument("--work-root", required=True, type=Path)
        if name == "prepare":
            phase.add_argument("--payload-count", type=positive_int, default=PAYLOAD_COUNT)
            phase.add_argument("--payload-bytes", type=positive_int, default=PAYLOAD_BYTES)
        if name == "measure":
            phase.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve()
    parent = args.work_root.resolve()

    if args.phase == "prepare":
        parent.mkdir(parents=True, exist_ok=False)
        generate_fixtures(binary, parent, args.payload_count, args.payload_bytes)
        return
    if not parent.is_dir():
        raise SystemExit(f"prepared work root does not exist: {parent}")
    if args.phase == "smoke":
        _, fixtures = load_prepared(parent)
        workloads = run_workloads(binary, parent, fixtures, "smoke", 1)
        if any(sample.get("final_schema") != 16 for sample in workloads["schema15-to-16-production"]):
            raise SystemExit("schema readiness smoke migration did not publish schema 16")
        print(json.dumps({"smoke": "passed", "workloads": workloads}, indent=2))
        return
    measure_prepared(binary, parent, args.output.resolve())


if __name__ == "__main__":
    main()
