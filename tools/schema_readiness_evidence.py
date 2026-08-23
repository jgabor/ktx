#!/usr/bin/env python3
"""Generate disposable schema-readiness fixtures and raw measurement evidence."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import sqlite3
import string
import subprocess
import threading
import time


PAYLOAD_COUNT = 3200
PAYLOAD_BYTES = 1_000_000
TIMESTAMP_START_NS = 1_704_067_200_000_000_000
TIMESTAMP_STEP_NS = 19_719_224_757_757
SAMPLES = 5
CPU = "2"


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


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        while block := source.read(1024 * 1024):
            value.update(block)
    return value.hexdigest()


def alpha_id(index):
    alphabet = string.ascii_lowercase
    return "p" + "".join(alphabet[(index // (26**power)) % 26] for power in range(14, -1, -1))


def generate_fixtures(binary, parent):
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
    rows = (
        (
            alpha_id(index),
            session,
            "read",
            f"fixture-{index}",
            "0" * 64,
            TIMESTAMP_START_NS + index * TIMESTAMP_STEP_NS,
            TIMESTAMP_START_NS + index * TIMESTAMP_STEP_NS,
        )
        for index in range(PAYLOAD_COUNT)
    )
    database.execute("BEGIN")
    database.executemany(
        """INSERT INTO tool_calls(
            id,session_id,tool,input,output,content_hash,timestamp,
            timestamp_unix_nano,event_type,latency_state,latency_unit,
            latency_authority
        ) VALUES(?,?,?,?,CAST(zeroblob(1000000) AS TEXT),?,
            datetime(?/1000000000,'unixepoch'),?,'tool_call','unknown',
            'milliseconds','unknown')""",
        rows,
    )
    database.commit()
    database.execute("PRAGMA journal_mode=DELETE")
    database.close()

    schema15 = parent / "schema15.db"
    run(["cp", "--reflink=always", str(current), str(schema15)], roots(parent, "fixture-schema15-copy"))
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
    return minimal, current, schema15


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


def measure(command, env, database, lock_kind):
    started_epoch = time.time()
    started_monotonic = time.perf_counter_ns()
    process = subprocess.Popen(
        ["taskset", "-c", CPU, "strace", "-f", "-ttt", "-e", "trace=fcntl,flock", *command],
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
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
    stdout, stderr = process.communicate()
    completion = time.perf_counter_ns() - started_monotonic
    if process.returncode:
        raise RuntimeError(stderr)

    events = []
    for line in stderr.splitlines():
        timestamp = re.search(r"(?:\[pid +\d+\] )?(\d+\.\d+)", line)
        if not timestamp:
            continue
        if lock_kind == "flock":
            lock = re.search(r"flock\([^,]+, (LOCK_EX|LOCK_UN)", line)
            if lock:
                events.append((float(timestamp.group(1)), lock.group(1)))
        else:
            lock = re.search(r"fcntl\(.*F_SETLK(?:W)?, \{l_type=(F_[RW]DLCK|F_UNLCK)", line)
            if lock:
                events.append((float(timestamp.group(1)), lock.group(1)))

    locked = [stamp for stamp, kind in events if kind != "LOCK_UN" and kind != "F_UNLCK"]
    unlocked = [stamp for stamp, kind in events if kind == "LOCK_UN" or kind == "F_UNLCK"]
    lock_hold = round((max(unlocked) - min(locked)) * 1e9) if locked and unlocked else 0
    readiness = (
        max(0, round((min(locked) - started_epoch) * 1e9))
        if lock_kind == "flock" and locked
        else completion
    )
    return {
        "readiness_latency_ns": readiness,
        "exclusive_lock_hold_ns": lock_hold,
        "journal_growth_bytes": peak_journal,
        "peak_rss_bytes": peak_rss,
        "total_completion_ns": completion,
        "stdout": stdout.strip(),
    }


def fixture_record(path, table, timestamp_column):
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
        "sha256": digest(path),
        "schema": version,
        "rows": count,
        "payload_bytes": payload,
        "first_timestamp": first,
        "last_timestamp": last,
        "integrity_check": integrity,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--work-root", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve()
    parent = args.work_root.resolve()
    parent.mkdir(parents=True, exist_ok=False)
    minimal, current, schema15 = generate_fixtures(binary, parent)

    workloads = {}
    for name, database in (
        ("current-minimal", minimal),
        ("current-production", current),
    ):
        samples = []
        for index in range(SAMPLES + 1):
            env = roots(parent, f"{name}-{index}")
            env["KTX_DB"] = str(database)
            sample = measure([str(binary), "list", "--format", "json"], env, database, "fcntl")
            sample["kind"] = "warmup" if index == 0 else "measured"
            samples.append(sample)
        workloads[name] = samples

    migration_samples = []
    for index in range(SAMPLES + 1):
        root = parent / f"migration-{index}"
        env = roots(parent, f"migration-{index}-roots")
        database = root / "schema15.db"
        root.mkdir(parents=True)
        run(["cp", "--reflink=always", str(schema15), str(database)], env)
        sample = measure(
            [str(binary), "db", "--path", str(database), "migrate"], env, database, "flock"
        )
        sample["kind"] = "warmup" if index == 0 else "measured"
        sample["final_schema"] = sqlite3.connect(database).execute("PRAGMA user_version").fetchone()[0]
        migration_samples.append(sample)
    workloads["schema15-to-16-production"] = migration_samples

    metrics = (
        "readiness_latency_ns",
        "exclusive_lock_hold_ns",
        "journal_growth_bytes",
        "peak_rss_bytes",
        "total_completion_ns",
    )
    measured = [sample for samples in workloads.values() for sample in samples if sample["kind"] == "measured"]
    worst = {metric: max(sample[metric] for sample in measured) for metric in metrics}
    limits = {metric: (value * 125 + 99) // 100 for metric, value in worst.items()}
    capabilities = json.loads(run([str(binary), "binary-capabilities"], roots(parent, "capabilities")).stdout)
    report = {
        "schema_version": 1,
        "policy": {
            "cpu": int(CPU),
            "gomaxprocs": 1,
            "warmups_per_workload": 1,
            "measured_samples_per_workload": SAMPLES,
            "poll_interval_seconds": 0.0005,
            "limit": "ceil(worst measured value across workloads * 1.25)",
            "current_readiness_boundary": "process start through command completion",
            "migration_readiness_boundary": "process start through first successful LOCK_EX",
            "lock_hold_boundary": "first acquired measured lock through final unlock",
        },
        "host": {
            "node": platform.node(),
            "platform": platform.platform(),
            "machine": platform.machine(),
            "processor": platform.processor(),
        },
        "binary": capabilities,
        "fixture_inputs": {
            "payload_count": PAYLOAD_COUNT,
            "payload_bytes_each": PAYLOAD_BYTES,
            "timestamp_start_unix_nano": TIMESTAMP_START_NS,
            "timestamp_step_nanoseconds": TIMESTAMP_STEP_NS,
        },
        "fixtures": {
            "minimal": fixture_record(minimal, "tool_calls", "timestamp"),
            "current": fixture_record(current, "tool_calls", "timestamp"),
            "schema15": fixture_record(schema15, "compressed", "timestamp"),
        },
        "workloads": workloads,
        "observed_worst": worst,
        "release_limits": limits,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
