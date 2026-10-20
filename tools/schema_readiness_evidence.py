#!/usr/bin/env python3
"""Generate disposable schema-readiness fixtures and raw measurement evidence."""

import argparse
import hashlib
import gzip
import json
import os
from pathlib import Path
import platform
import re
import signal
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
HISTORICAL_MANIFEST = Path("internal/db/testdata/historical/manifest.json")
HISTORICAL_FIXTURE = Path("internal/db/testdata/historical/schema-15.db.gz")
HISTORICAL_FAILURE_REPORT = Path(
    "docs/evidence/schema-readiness/schema-readiness-release.json.failed.json"
)
HISTORICAL_FAILURE_SHA256 = (
    "b6a9d72bf7e7d794923b5c1474704c83dbf84731d6da55762012bd6ad1dbd007"
)
APPROVED_LIMITS = {
    "readiness_latency_ns": 35_322_974,
    "exclusive_lock_hold_ns": 560_264_885,
    "journal_growth_bytes": 50_000,
    "peak_rss_bytes": 59_130_880,
    "total_completion_ns": 893_936_754,
}
LIMIT_BASIS = (
    "user-approved combined-workload revision dated 2026-08-24; readiness latency and "
    "journal growth are unchanged, and memory is aggregate concurrent KTX RSS"
)


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


def digest(path, label, phase="prepare"):
    value = hashlib.sha256()
    size = path.stat().st_size
    processed = 0
    next_progress = 256 * 1024 * 1024
    with path.open("rb") as source:
        while block := source.read(1024 * 1024):
            value.update(block)
            processed += len(block)
            if processed >= next_progress:
                progress(phase, f"{label} hash {processed}/{size} bytes")
                next_progress += 256 * 1024 * 1024
    return value.hexdigest()


def bytes_digest(data):
    return hashlib.sha256(data).hexdigest()


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
    historical = Path(__file__).resolve().parent.parent / HISTORICAL_FIXTURE
    progress("prepare", "expanding exact historical schema-15 fixture")
    schema15.write_bytes(gzip.decompress(historical.read_bytes()))
    database = sqlite3.connect(schema15)
    database.execute("PRAGMA journal_mode=OFF")
    database.execute("PRAGMA synchronous=OFF")
    for table in ("compression_dispositions", "compressed", "tool_calls", "sessions"):
        database.execute(f"DELETE FROM {table}")
    database.execute(
        "INSERT INTO sessions(id,agent,started_at,last_seen_at,tool_call_count) VALUES(?,?,?,?,?)",
        (session, "unknown", "2024-01-01T00:00:00Z", "2026-01-01T00:00:00Z", payload_count),
    )
    for start in range(0, payload_count, 32):
        rows = []
        artifacts = []
        for index in range(start, min(start + 32, payload_count)):
            source_id = alpha_id(index)
            timestamp = time.strftime(
                "%Y-%m-%dT%H:%M:%SZ", time.gmtime((TIMESTAMP_START_NS + index * TIMESTAMP_STEP_NS) // 1_000_000_000)
            )
            rows.append((source_id, session, "read", f"fixture-{index}", payload_bytes, "0" * 64, timestamp))
            artifacts.append((source_id[1:] + chr(97 + (index % 26)), source_id, 10, "0" * 64, "{}", timestamp, "hook"))
        database.executemany(
            "INSERT INTO tool_calls(id,session_id,tool,input,output,content_hash,timestamp) VALUES(?,?,?,?,CAST(zeroblob(?) AS TEXT),?,?)",
            rows,
        )
        database.executemany(
            "INSERT INTO compressed(id,source_id,output,content_hash,pipeline,timestamp,producer) VALUES(?,?,CAST(zeroblob(?) AS TEXT),?,?,?,?)",
            artifacts,
        )
        progress("prepare", f"inserted {min(start + 32, payload_count)}/{payload_count} exact schema-15 rows")
    database.commit()
    database.execute("PRAGMA journal_mode=DELETE")
    database.close()
    fixtures = {
        "minimal": fixture_record(minimal, "tool_calls", "timestamp", "minimal"),
        "current": fixture_record(current, "tool_calls", "timestamp", "current"),
        "schema15": fixture_record(
            schema15,
            "compressed",
            "timestamp",
            "schema15",
            timestamp_inputs={
                "timestamp_start_unix_nano": TIMESTAMP_START_NS,
                "timestamp_step_nanoseconds": TIMESTAMP_STEP_NS,
            },
            source_table="tool_calls",
        ),
    }
    manifest = {
        "schema_version": 2,
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
    return sum(process_rss(current) for current in process_tree(pid))


def process_tree(pid):
    pending = [pid]
    seen = set()
    while pending:
        current = pending.pop()
        if current in seen:
            continue
        seen.add(current)
        try:
            children = Path(f"/proc/{current}/task/{current}/children").read_text()
            pending.extend(int(child) for child in children.split())
        except (FileNotFoundError, ProcessLookupError):
            pass
    return seen


def process_rss(pid):
    try:
        status = Path(f"/proc/{pid}/status").read_text()
    except (FileNotFoundError, ProcessLookupError):
        return 0
    match = re.search(r"^VmRSS:\s+(\d+) kB$", status, re.MULTILINE)
    return int(match.group(1)) * 1024 if match else 0


def binary_database_rss(binary, database):
    resident = {}
    target = ("KTX_DB=" + str(database)).encode()
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            if os.path.samefile(entry / "exe", binary):
                environment = (entry / "environ").read_bytes().split(b"\0")
                if target in environment:
                    value = process_rss(int(entry.name))
                    if value:
                        resident[int(entry.name)] = value
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            pass
    return resident


def exclusive_lock_observations(stderr, lock_kind):
    active = {}
    starts = []
    intervals = []
    observations = []
    unlocks = []
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
        else:
            if (lock_kind == "flock" and "LOCK_UN" in line) or (
                lock_kind == "fcntl" and "F_UNLCK" in line
            ):
                unlocks.append(stamp)
            if key in active:
                started = active.pop(key)
                duration = round((stamp - started) * 1e9)
                intervals.append(duration)
                observations.append({"pid": pid, "started_epoch": started, "duration_ns": duration})
    return starts, intervals, observations, unlocks


def measure(command, env, database, lock_kind, measured_binary=None, combined_observation=None):
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
        observations = []
        sidecars = [Path(str(database) + suffix) for suffix in ("-journal", "-wal", "-shm")]
        while process.poll() is None:
            if measured_binary:
                resident = binary_database_rss(measured_binary, database)
            else:
                resident = {process.pid: rss_tree(process.pid)}
            observations.append(
                (
                    time.time(),
                    resident,
                    sum(path.stat().st_size for path in sidecars if path.exists()),
                )
            )
            time.sleep(0.0005)
        process.wait()
        finished_epoch = time.time()
        stdout_capture.seek(0)
        stderr_capture.seek(0)
        stdout = stdout_capture.read()
        stderr = stderr_capture.read()
    completion = time.perf_counter_ns() - started_monotonic
    if process.returncode:
        raise RuntimeError(stderr)

    lock_starts, lock_intervals, lock_observations, unlocks = exclusive_lock_observations(stderr, lock_kind)
    combined = combined_observation is not None
    lifecycle = json.loads(combined_observation.read_text()) if combined else None
    participants = (
        [{"role": role} for role in lifecycle["roles"]] if lifecycle else []
    )
    window_start = (
        lifecycle["query_started_at_unix_nano"] / 1_000_000_000
        if lifecycle
        else started_epoch
    )
    window_observations = [item for item in observations if item[0] >= window_start]
    per_process_peaks = {}
    for _, resident, _ in window_observations:
        for pid, value in resident.items():
            per_process_peaks[pid] = max(per_process_peaks.get(pid, 0), value)
    individual_peak_rss = max(per_process_peaks.values(), default=0)
    aggregate_peak_rss = max(
        (sum(item[1].values()) for item in window_observations), default=0
    )
    peak_journal = max((item[2] for item in window_observations), default=0)
    if combined:
        completion = max(0, round((finished_epoch - window_start) * 1e9))
        query_locks = [
            item for item in lock_observations if item["started_epoch"] >= window_start
        ]
        synchronization_release = lifecycle["released_at_unix_nano"] / 1_000_000_000
        locks_after_release = [
            item for item in query_locks if item["started_epoch"] >= synchronization_release
        ]
        readiness = (
            max(
                0,
                round(
                    (
                        min(item["started_epoch"] for item in locks_after_release)
                        - synchronization_release
                    )
                    * 1e9
                ),
            )
            if locks_after_release
            else completion
        )
    else:
        readiness = (
            max(0, round((min(lock_starts) - started_epoch) * 1e9))
            if lock_kind == "flock" and lock_starts
            else completion
        )
    lock_hold = max(lock_intervals, default=0)
    criterion_peak_rss = aggregate_peak_rss
    result = {
        "readiness_latency_ns": readiness,
        "exclusive_lock_hold_ns": lock_hold,
        "journal_growth_bytes": peak_journal,
        "peak_rss_bytes": criterion_peak_rss,
        "total_completion_ns": completion,
        "exclusive_lock_intervals_ns": lock_intervals,
        "stdout": stdout.strip(),
    }
    if combined:
        roles = {item["role"] for item in participants}
        process_peaks = [
            {"pid": pid, "peak_rss_bytes": value}
            for pid, value in sorted(per_process_peaks.items())
        ]
        result["per_process_peak_rss_bytes"] = process_peaks
        result["combined_workload"] = {
            "attributed": {"query", "hook", "telemetry"}.issubset(roles),
            "participants": participants,
            "rss_criterion": "aggregate concurrent KTX RSS",
            "aggregate_peak_rss_bytes": aggregate_peak_rss,
            "maximum_individual_peak_rss_bytes": individual_peak_rss,
            "per_process_peak_rss_bytes": process_peaks,
            "synchronization_release_epoch": synchronization_release,
            "window_started_epoch": window_start,
            "window_finished_epoch": finished_epoch,
        }
    return result


def fixture_record(
    path,
    table,
    timestamp_column,
    label,
    timestamp_inputs=None,
    source_table=None,
    phase="prepare",
):
    progress(phase, f"checking {label} fixture integrity")
    database = sqlite3.connect(path)
    integrity = database.execute("PRAGMA integrity_check").fetchone()[0]
    version = database.execute("PRAGMA user_version").fetchone()[0]
    count, payload, first, last = database.execute(
        f"SELECT count(*),sum(length(CAST(output AS BLOB))),min({timestamp_column}),max({timestamp_column}) FROM {table}"
    ).fetchone()
    record = {
        "path_basename": path.name,
        "bytes": path.stat().st_size,
        "sha256": digest(path, label, phase),
        "schema": version,
        "rows": count,
        "payload_bytes": payload,
        "first_timestamp": first,
        "last_timestamp": last,
        "integrity_check": integrity,
        "physical_schema_sha256": physical_schema_digest(path),
    }
    if source_table:
        source_rows, source_payload = database.execute(
            f"SELECT count(*),sum(length(CAST(output AS BLOB))) FROM {source_table}"
        ).fetchone()
        record["source_rows"] = source_rows
        record["source_payload_bytes"] = source_payload
    if timestamp_inputs:
        timestamps = [
            row[0]
            for row in database.execute(
                f"SELECT {timestamp_column} FROM {table} ORDER BY source_id"
            )
        ]
        sequence = hashlib.sha256()
        policy_matches = len(timestamps) == count
        for index, timestamp in enumerate(timestamps):
            sequence.update(timestamp.encode() + b"\n")
            expected = time.strftime(
                "%Y-%m-%dT%H:%M:%SZ",
                time.gmtime(
                    (
                        timestamp_inputs["timestamp_start_unix_nano"]
                        + index * timestamp_inputs["timestamp_step_nanoseconds"]
                    )
                    // 1_000_000_000
                ),
            )
            policy_matches = policy_matches and timestamp == expected
        record["timestamp_distribution"] = {
            **timestamp_inputs,
            "distinct_timestamps": len(set(timestamps)),
            "sequence_sha256": sequence.hexdigest(),
            "matches_inputs": policy_matches,
        }
    database.close()
    return record


def load_prepared(parent):
    manifest_path = parent / PREPARED_MANIFEST
    manifest = json.loads(manifest_path.read_text())
    if manifest.get("schema_version") != 2:
        raise SystemExit(f"unsupported prepared-fixture manifest: {manifest_path}")
    paths = {
        name: parent / record["path_basename"]
        for name, record in manifest["fixtures"].items()
    }
    missing = [str(path) for path in paths.values() if not path.is_file()]
    if missing:
        raise SystemExit("prepared fixture missing: " + ", ".join(missing))
    return manifest, paths


def compile_generated_adapter_driver(repository, parent):
    driver = parent / "generated-adapter-readiness.test"
    env = roots(parent, "release-driver-build")
    progress("release", "building generated-adapter lifecycle driver")
    subprocess.run(
        ["go", "test", "-c", "-o", str(driver), "./internal/db"],
        cwd=repository,
        env=env,
        text=True,
        capture_output=True,
        check=True,
    )
    return driver


def combined_copy_evidence(database, expected_rows, expected_evidence_digest, sample):
    result = validate_migrated_copy(database, expected_rows, expected_evidence_digest)
    with sqlite3.connect(database) as connection:
        sessions = connection.execute("SELECT count(*) FROM sessions").fetchone()[0]
        sources = connection.execute("SELECT count(*) FROM tool_calls").fetchone()[0]
        generated = connection.execute(
            "SELECT id,tool,input,output,producer FROM tool_calls WHERE session_id='adapterownersess'"
        ).fetchall()
        telemetry = connection.execute(
            """SELECT count(*) FROM session_telemetry
            WHERE source_session_id='session-overlap' AND message_id='message-overlap'
            AND part_id='part-overlap' AND ordinal=1 AND cost=1.25
            AND tokens_input=11 AND tokens_output=7 AND tokens_reasoning=3
            AND cache_read=5 AND cache_write=2"""
        ).fetchone()[0]
        dispositions = (
            connection.execute(
                "SELECT count(*) FROM compression_dispositions WHERE source_id=?",
                (generated[0][0],),
            ).fetchone()[0]
            if len(generated) == 1
            else 0
        )
    roles = [item["role"] for item in sample.get("combined_workload", {}).get("participants", [])]
    exact_generated = (
        len(generated) == 1
        and generated[0][1:] == ("bash", '{"command":"true"}', "adapter overlap output", "hook")
    )
    result.update(
        {
            "sessions": sessions,
            "tool_calls": sources,
            "generated_source_id": generated[0][0] if len(generated) == 1 else None,
            "generated_source_exact": exact_generated,
            "generated_dispositions": dispositions,
            "generated_telemetry_rows": telemetry,
            "participant_roles": roles,
            "manual_migration_command": False,
            "raw_lock_or_manual_guidance": False,
        }
    )
    result["passed"] = (
        result["passed"]
        and sessions == 2
        and sources == expected_rows + 1
        and exact_generated
        and dispositions == 1
        and telemetry == 1
        and {"query", "hook", "telemetry"}.issubset(roles)
    )
    return result


def run_workloads(binary, parent, fixtures, phase, samples, adapter_driver=None):
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
        if adapter_driver:
            with sqlite3.connect(database) as connection:
                expected_rows = connection.execute("SELECT count(*) FROM compressed").fetchone()[0]
            observation = root / "combined-observation.json"
            env["KTX_TEST_BUILT_BINARY"] = str(binary)
            env["KTX_TEST_SCHEMA15_DATABASE"] = str(database)
            env["KTX_TEST_SCHEMA15_EXPECTED_ROWS"] = str(expected_rows)
            env["KTX_TEST_COMBINED_OBSERVATION"] = str(observation)
            baseline_digest = compressed_evidence_digest(database)
            sample = measure(
                [
                    str(adapter_driver),
                    "-test.run",
                    "^TestGeneratedOpenCodeAdapterProductionReadiness$",
                    "-test.count=1",
                    "-test.timeout=30s",
                ],
                env,
                database,
                "flock",
                measured_binary=binary,
                combined_observation=observation,
            )
            sample["combined_evidence"] = combined_copy_evidence(
                database,
                expected_rows,
                baseline_digest,
                sample,
            )
        else:
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


def measure_prepared(binary, parent, output, adapter_driver=None):
    manifest, fixtures = load_prepared(parent)
    workloads = run_workloads(binary, parent, fixtures, "measure", SAMPLES + 1, adapter_driver)

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
    binary_hash = digest(binary, "measured binary", "measure")
    supported_schema = supported_schema_evidence(workloads)
    report = {
        "schema_version": 4,
        "policy": {
            "cpu": int(CPU),
            "gomaxprocs": 1,
            "warmups_per_workload": 1,
            "measured_samples_per_workload": SAMPLES,
            "poll_interval_seconds": 0.0005,
            "limit": "approved fixed combined-workload limits",
            "limit_basis": LIMIT_BASIS,
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
        "binary": {"sha256": binary_hash},
        "supported_database_schema": supported_schema,
        "fixture_inputs": manifest["fixture_inputs"],
        "fixtures": manifest["fixtures"],
        "workloads": workloads,
        "observed_worst": worst,
        "release_limits": APPROVED_LIMITS,
        "limit_checks": limit_checks,
        "release_gate_passed": all(check["passed"] for check in limit_checks.values()),
    }
    if adapter_driver:
        report["policy"].update(
            {
                "migration_readiness_boundary": "deterministic combined-workload release through the built KTX query's first successful exclusive migration lock",
                "lock_hold_boundary": "maximum individual successful exclusive-lock interval across combined KTX traffic",
                "rss_boundary": "aggregate concurrent resident bytes of actual built KTX participants; per-process peaks retained separately",
                "completion_boundary": "built KTX query process start through completion of generated hook, telemetry, and query traffic",
            }
        )
        production_samples = [
            sample
            for sample in workloads["schema15-to-16-production"]
            if sample["kind"] == "measured"
        ]
        report["combined_production_samples_passed"] = all(
            sample.get("combined_evidence", {}).get("passed")
            and sample.get("combined_workload", {}).get("attributed")
            for sample in production_samples
        )
        report["combined_production_observed_worst"] = {
            metric: max(sample[metric] for sample in production_samples)
            for metric in metrics
        }
        report["combined_production_limit_checks"] = {
            metric: {
                "observed": report["combined_production_observed_worst"][metric],
                "limit": APPROVED_LIMITS[metric],
                "passed": report["combined_production_observed_worst"][metric]
                <= APPROVED_LIMITS[metric],
            }
            for metric in metrics
        }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + "\n")
    progress("measure", f"wrote {output}")
    if not report["release_gate_passed"]:
        failed = ", ".join(metric for metric, check in limit_checks.items() if not check["passed"])
        raise SystemExit(f"schema readiness release limits exceeded: {failed}")


def criterion(name, passed, evidence):
    status = "NOT_RUN" if passed is None else ("PASS" if passed else "FAIL")
    return {"criterion": name, "status": status, "evidence": evidence}


def validate_release_report(report):
    required = {
        "prior_schema_identity",
        "representative_scale",
        "concurrent_generated_traffic",
        "interruption_restart",
        *APPROVED_LIMITS,
    }
    checks = {item.get("criterion"): item for item in report.get("criteria", [])}
    missing = sorted(required - checks.keys())
    if missing:
        raise ValueError("missing release criteria: " + ", ".join(missing))
    failed = sorted(name for name in required if checks[name].get("status") != "PASS")
    if failed:
        raise ValueError("failed release criteria: " + ", ".join(failed))
    if report.get("release_gate") != "PASS":
        raise ValueError("release_gate must be PASS")
    if report.get("schema_version") != 4:
        raise ValueError("release report schema_version must be 4")
    if report.get("report_role") != "current_passing_authority":
        raise ValueError("passing report role must be current_passing_authority")
    historical = report.get("historical_failure_evidence", {})
    if historical != {
        "report_role": "historical_failure_evidence",
        "path": str(HISTORICAL_FAILURE_REPORT),
        "sha256": HISTORICAL_FAILURE_SHA256,
    }:
        raise ValueError("release report does not bind the historical failed authority")
    if not re.fullmatch(r"[0-9a-f]{64}", report.get("binary", {}).get("sha256", "")):
        raise ValueError("release report lacks the artifact-bytes SHA-256")
    supported = report.get("supported_database_schema", {})
    if supported.get("maximum") != 16 or supported.get("evidence") != "measured workload final_schema":
        raise ValueError("release report lacks readiness-derived supported schema evidence")
    if not re.fullmatch(r"[0-9a-f]{40}", report.get("repository", {}).get("commit", "")):
        raise ValueError("release report does not retain the repository commit identity")
    samples = report.get("raw_samples", {}).get("schema15_to_16_production", [])
    measured = [sample for sample in samples if sample.get("kind") == "measured"]
    if len(measured) != SAMPLES:
        raise ValueError(f"raw production samples = {len(measured)}, want {SAMPLES}")
    if not all(
        sample.get("combined_workload", {}).get("attributed")
        and sample.get("combined_evidence", {}).get("passed")
        for sample in measured
    ):
        raise ValueError("raw production samples lack combined-workload attribution or exact evidence")
    if not all(
        sample.get("peak_rss_bytes")
        == sample.get("combined_workload", {}).get("aggregate_peak_rss_bytes")
        and sample.get("combined_workload", {}).get("rss_criterion")
        == "aggregate concurrent KTX RSS"
        and sample.get("per_process_peak_rss_bytes")
        == sample.get("combined_workload", {}).get("per_process_peak_rss_bytes")
        for sample in measured
    ):
        raise ValueError("raw production samples do not use aggregate concurrent KTX RSS")
    for name, limit in APPROVED_LIMITS.items():
        evidence = checks[name].get("evidence", {})
        if evidence.get("limit") != limit or evidence.get("observed", limit + 1) > limit:
            raise ValueError(f"release criterion {name} does not satisfy its approved limit")


def physical_schema_digest(database):
    with sqlite3.connect(database) as connection:
        rows = connection.execute(
            "SELECT type,name,tbl_name,sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type,name"
        ).fetchall()
    return bytes_digest(json.dumps(rows, separators=(",", ":")).encode())


def historical_identity(repository, parent):
    manifest_path = repository / HISTORICAL_MANIFEST
    fixture_path = repository / HISTORICAL_FIXTURE
    manifest = json.loads(manifest_path.read_text())
    record = next((item for item in manifest["fixtures"] if item["version"] == 15), None)
    if record is None or record["file"] != fixture_path.name:
        raise ValueError("historical schema-15 fixture manifest entry is missing")
    data = gzip.decompress(fixture_path.read_bytes())
    actual = bytes_digest(data)
    if actual != record["database_sha256"]:
        raise ValueError("historical schema-15 fixture hash does not match its revision manifest")
    reference = parent / "historical-schema15-reference.db"
    reference.write_bytes(data)
    try:
        schema_digest = physical_schema_digest(reference)
    finally:
        reference.unlink()
    return {
        "schema": 15,
        "source_revision": record["source_revision"],
        "database_sha256": actual,
        "metadata_sha256": record["metadata_sha256"],
        "manifest_sha256": digest(manifest_path, "historical manifest", "release"),
        "physical_schema_sha256": schema_digest,
    }


def validate_release_fixture(repository, parent):
    manifest, fixtures = load_prepared(parent)
    expected_inputs = {
        "payload_count": PAYLOAD_COUNT,
        "payload_bytes_each": PAYLOAD_BYTES,
        "timestamp_start_unix_nano": TIMESTAMP_START_NS,
        "timestamp_step_nanoseconds": TIMESTAMP_STEP_NS,
    }
    actual = fixture_record(
        fixtures["schema15"],
        "compressed",
        "timestamp",
        "consumed schema15",
        timestamp_inputs={
            "timestamp_start_unix_nano": TIMESTAMP_START_NS,
            "timestamp_step_nanoseconds": TIMESTAMP_STEP_NS,
        },
        source_table="tool_calls",
        phase="release",
    )
    prepared = manifest.get("fixtures", {}).get("schema15")
    if manifest.get("fixture_inputs") != expected_inputs:
        raise ValueError("prepared fixture inputs do not match the approved production-scale policy")
    compare_consumed_fixture(actual, prepared)
    identity = historical_identity(repository, parent)
    if actual["physical_schema_sha256"] != identity["physical_schema_sha256"]:
        raise ValueError("consumed schema15 physical schema differs from exact historical authority")
    scale_ok = (
        actual["schema"] == 15
        and actual["rows"] == PAYLOAD_COUNT
        and actual["payload_bytes"] == PAYLOAD_COUNT * 10
        and actual["source_rows"] == PAYLOAD_COUNT
        and actual["source_payload_bytes"] == PAYLOAD_COUNT * PAYLOAD_BYTES
        and actual["bytes"] >= PAYLOAD_COUNT * PAYLOAD_BYTES
        and actual["integrity_check"] == "ok"
        and actual["timestamp_distribution"]["matches_inputs"]
    )
    if not scale_ok:
        raise ValueError("consumed schema15 fixture does not satisfy the approved scale and timestamp policy")
    identity["consumed_physical_schema_sha256"] = actual["physical_schema_sha256"]
    return manifest, fixtures, actual, identity


def compare_consumed_fixture(actual, prepared):
    if actual == prepared:
        return
    fields = sorted(
        key
        for key in set(actual) | set(prepared or {})
        if actual.get(key) != (prepared or {}).get(key)
    )
    raise ValueError("consumed schema15 fixture differs from prepared manifest: " + ", ".join(fields))


def wait_for_durable_cursor(database, process, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("migration completed before interruption observed a durable cursor")
        try:
            with sqlite3.connect(database, timeout=0.01) as connection:
                row = connection.execute(
                    "SELECT last_id FROM compressed_timestamp_migration WHERE singleton=1"
                ).fetchone()
                version = connection.execute("PRAGMA user_version").fetchone()[0]
                if row and row[0]:
                    os.kill(process.pid, signal.SIGSTOP)
                    if process.poll() is not None:
                        raise RuntimeError("migration completed before it could be stopped")
                    return row[0], version
        except sqlite3.Error:
            pass
        time.sleep(0.001)
    raise RuntimeError("timed out waiting for a durable migration cursor")


def compressed_evidence_digest(database):
    value = hashlib.sha256()
    with sqlite3.connect(database) as connection:
        rows = connection.execute(
            "SELECT id,source_id,output,content_hash,pipeline,timestamp,producer FROM compressed ORDER BY id"
        )
        for row in rows:
            value.update(json.dumps(row, separators=(",", ":")).encode() + b"\n")
    return value.hexdigest()


def validate_migrated_copy(database, expected_rows, expected_evidence_digest):
    with sqlite3.connect(database) as connection:
        version = connection.execute("PRAGMA user_version").fetchone()[0]
        integrity = connection.execute("PRAGMA integrity_check").fetchone()[0]
        rows, distinct_ids, missing = connection.execute(
            "SELECT count(*),count(DISTINCT id),sum(timestamp_unix_nano IS NULL) FROM compressed"
        ).fetchone()
        cursor = connection.execute(
            "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='compressed_timestamp_migration'"
        ).fetchone()[0]
        hot_journal = Path(str(database) + "-journal").exists()
    result = {
        "schema": version,
        "integrity_check": integrity,
        "rows": rows,
        "distinct_ids": distinct_ids,
        "missing_sortable_timestamps": missing,
        "cursor_tables": cursor,
        "hot_journal": hot_journal,
        "evidence_sha256": compressed_evidence_digest(database),
        "passed": version == 16 and integrity == "ok" and rows == expected_rows
        and distinct_ids == expected_rows and missing == 0 and cursor == 0
        and not hot_journal,
    }
    result["passed"] = result["passed"] and result["evidence_sha256"] == expected_evidence_digest
    return result


def interruption_rehearsal(binary, parent, fixture, expected_rows):
    root = parent / "release-interruption"
    root.mkdir()
    database = root / "schema15.db"
    env = roots(parent, "release-interruption-roots")
    copy_fixture(fixture, database, env, "release")
    expected_evidence_digest = compressed_evidence_digest(database)
    command = [str(binary), "db", "--path", str(database), "migrate"]
    first = subprocess.Popen(command, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    cursor, schema_before_recovery = wait_for_durable_cursor(database, first)
    query_env = env.copy()
    query_env["KTX_DB"] = str(database)
    query = subprocess.Popen(
        [str(binary), "list", "--format", "json"], env=query_env,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    first.kill()
    first.communicate(timeout=10)
    query_stdout, query_stderr = query.communicate(timeout=30)
    result = validate_migrated_copy(database, expected_rows, expected_evidence_digest)
    with sqlite3.connect(database) as connection:
        durable_rows = connection.execute(
            "SELECT count(*) FROM compressed WHERE id <= ?", (cursor,)
        ).fetchone()[0]
    migrated_copy_passed = result["passed"]
    result.update({
        "durable_cursor": cursor,
        "durable_rows_before_restart": durable_rows,
        "schema_before_restart": schema_before_recovery,
        "first_exit": first.returncode,
        "concurrent_query_exit": query.returncode,
        "concurrent_query_output_sha256": bytes_digest(query_stdout.encode()),
        "concurrent_query_raw_lock_error": "database is locked" in query_stderr.lower(),
        "automatic_recovery_command": "ktx list --format json",
        "post_recovery_migrate_invoked": False,
    })
    result["passed"] = automatic_interruption_passed(result, migrated_copy_passed)
    return result


def automatic_interruption_passed(result, migrated_copy_passed=True):
    return (
        migrated_copy_passed
        and result.get("durable_rows_before_restart", 0) > 0
        and result.get("schema_before_restart") == 15
        and result.get("concurrent_query_exit") == 0
        and not result.get("concurrent_query_raw_lock_error", True)
        and result.get("post_recovery_migrate_invoked") is False
    )


def repository_identity(repository):
    commit = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=repository, text=True, capture_output=True, check=True
    ).stdout.strip()
    dirty = bool(
        subprocess.run(
            ["git", "status", "--porcelain"],
            cwd=repository,
            text=True,
            capture_output=True,
            check=True,
        ).stdout
    )
    return {"commit": commit, "worktree_dirty": dirty}


def historical_failure_evidence(repository):
    path = repository / HISTORICAL_FAILURE_REPORT
    actual = bytes_digest(path.read_bytes())
    if actual != HISTORICAL_FAILURE_SHA256:
        raise ValueError(
            "historical failed release report hash does not match its retained authority"
        )
    return {
        "report_role": "historical_failure_evidence",
        "path": str(HISTORICAL_FAILURE_REPORT),
        "sha256": actual,
    }


def retained_workload_samples(raw):
    retained = {}
    for name, samples in raw["workloads"].items():
        retained[name.replace("-", "_")] = []
        for sample in samples:
            item = dict(sample)
            stdout = item.pop("stdout", "")
            item["stdout_sha256"] = bytes_digest(stdout.encode())
            retained[name.replace("-", "_")].append(item)
    return retained


def supported_schema_evidence(workloads):
    final_schemas = {
        sample.get("final_schema")
        for samples in workloads.values()
        for sample in samples
        if sample.get("final_schema") is not None
    }
    if final_schemas != {16}:
        raise ValueError("readiness workloads do not establish one supported database schema")
    return {"maximum": 16, "evidence": "measured workload final_schema"}


def release_report(
    binary_hash,
    supported_schema,
    repository,
    historical_failure,
    policy,
    host,
    criteria,
    raw=None,
    raw_path=None,
):
    passed = all(item["status"] == "PASS" for item in criteria)
    report = {
        "schema_version": 4,
        "report_role": "current_passing_authority" if passed else "failed_release_attempt",
        "release_gate": "PASS" if passed else "FAIL",
        "binary": {"sha256": binary_hash},
        "supported_database_schema": supported_schema,
        "repository": repository,
        "historical_failure_evidence": historical_failure,
        "policy": policy,
        "host": host,
        "criteria": criteria,
        "raw_samples": retained_workload_samples(raw) if raw else {},
    }
    if raw_path and raw_path.is_file():
        report["raw_measurements_sha256"] = digest(
            raw_path, "raw release measurements", "release"
        )
    return report


def publish_release_report(report, output):
    expected_role = (
        "current_passing_authority"
        if report["release_gate"] == "PASS"
        else "failed_release_attempt"
    )
    if report.get("report_role") != expected_role:
        raise ValueError(f"report_role must be {expected_role}")
    if report["release_gate"] == "PASS":
        validate_release_report(report)
        destination = output
    else:
        destination = output.with_name(output.name + ".attempt.failed.json")
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(destination.name + ".tmp")
    try:
        temporary.write_text(json.dumps(report, indent=2) + "\n")
        os.replace(temporary, destination)
    finally:
        if temporary.exists():
            temporary.unlink()
    progress("release", f"wrote {destination}")
    if report["release_gate"] != "PASS":
        failed = ", ".join(
            item["criterion"] for item in report["criteria"] if item["status"] == "FAIL"
        )
        raise SystemExit("schema release gate failed: " + failed)


def stage_failure(error):
    return {"error_type": type(error).__name__, "error": str(error)}


def release_prepared(binary, parent, output, repository):
    binary_hash = digest(binary, "release binary", "release")
    repository_record = repository_identity(repository)
    historical_failure = historical_failure_evidence(repository)
    policy = {
        "cpu": int(CPU),
        "gomaxprocs": 1,
        "warmups_per_workload": 1,
        "measured_samples_per_workload": SAMPLES,
        "poll_interval_seconds": 0.0005,
        "limit": "approved fixed combined-workload limits",
        "limit_basis": LIMIT_BASIS,
        "migration_readiness_boundary": "deterministic combined-workload release through the built KTX query's first successful exclusive migration lock",
        "lock_hold_boundary": "maximum individual successful exclusive-lock interval across combined KTX traffic",
        "rss_boundary": "aggregate concurrent resident bytes of actual built KTX participants; per-process peaks retained separately",
        "completion_boundary": "built KTX query process start through completion of generated hook, telemetry, and query traffic",
        "copy_strategy": "cp --reflink=auto --sparse=always",
    }
    host = {
        "node": platform.node(),
        "platform": platform.platform(),
        "machine": platform.machine(),
        "processor": platform.processor(),
    }
    criteria = []
    try:
        manifest, fixtures, actual_fixture, identity = validate_release_fixture(repository, parent)
    except Exception as error:
        name = "prior_schema_identity" if "historical" in str(error) else "representative_scale"
        criteria = [
            criterion("prior_schema_identity", False if name == "prior_schema_identity" else None,
                      stage_failure(error) if name == "prior_schema_identity" else {}),
            criterion("representative_scale", False if name == "representative_scale" else None,
                      stage_failure(error) if name == "representative_scale" else {}),
            criterion("concurrent_generated_traffic", None, {}),
            criterion("interruption_restart", None, {}),
            *(criterion(metric, None, {}) for metric in APPROVED_LIMITS),
        ]
        publish_release_report(
            release_report(
                binary_hash,
                None,
                repository_record,
                historical_failure,
                policy,
                host,
                criteria,
            ),
            output,
        )
        return

    criteria.extend(
        [
            criterion("prior_schema_identity", True, identity),
            criterion(
                "representative_scale",
                True,
                {"fixture_inputs": manifest["fixture_inputs"], "fixture": actual_fixture},
            ),
        ]
    )

    try:
        interruption = interruption_rehearsal(
            binary, parent, fixtures["schema15"], actual_fixture["rows"]
        )
        interruption_item = criterion(
            "interruption_restart", interruption["passed"], interruption
        )
    except Exception as error:
        interruption_item = criterion("interruption_restart", False, stage_failure(error))

    raw_path = parent / "release-measurements.json"
    raw = None
    measurement_error = None
    try:
        driver = compile_generated_adapter_driver(repository, parent)
        try:
            measure_prepared(binary, parent, raw_path, driver)
        except SystemExit as error:
            measurement_error = error
        if raw_path.is_file():
            raw = json.loads(raw_path.read_text())
    except Exception as error:
        measurement_error = error

    if raw:
        supported_schema = supported_schema_evidence(raw["workloads"])
        production_samples = [
            sample
            for sample in raw["workloads"]["schema15-to-16-production"]
            if sample["kind"] == "measured"
        ]
        combined_ok = (
            len(production_samples) == SAMPLES
            and raw.get("combined_production_samples_passed") is True
        )
        combined_evidence = {
            "measured_samples": len(production_samples),
            "all_samples_attributed": all(
                sample.get("combined_workload", {}).get("attributed")
                for sample in production_samples
            ),
            "all_samples_exact": all(
                sample.get("combined_evidence", {}).get("passed")
                for sample in production_samples
            ),
        }
        if measurement_error and not combined_ok:
            combined_evidence.update(stage_failure(measurement_error))
        criteria.append(criterion("concurrent_generated_traffic", combined_ok, combined_evidence))
        criteria.append(interruption_item)
        for name, check in raw["limit_checks"].items():
            evidence = dict(check)
            evidence["combined_production_observed"] = raw[
                "combined_production_limit_checks"
            ][name]["observed"]
            if name == "peak_rss_bytes":
                evidence["measurement_role"] = "aggregate concurrent KTX RSS"
                evidence["maximum_individual_combined_observed"] = max(
                    sample["combined_workload"]["maximum_individual_peak_rss_bytes"]
                    for sample in production_samples
                )
            criteria.append(criterion(name, check["passed"], evidence))
    else:
        criteria.append(
            criterion(
                "concurrent_generated_traffic",
                False,
                stage_failure(measurement_error or RuntimeError("combined workload produced no report")),
            )
        )
        criteria.append(interruption_item)
        criteria.extend(criterion(metric, None, {}) for metric in APPROVED_LIMITS)

    report = release_report(
        binary_hash,
        supported_schema if raw else None,
        repository_record,
        historical_failure,
        policy,
        raw["host"] if raw else host,
        criteria,
        raw,
        raw_path,
    )
    publish_release_report(report, output)


def positive_int(value):
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def main():
    parser = argparse.ArgumentParser()
    phases = parser.add_subparsers(dest="phase", required=True)
    for name in ("prepare", "smoke", "measure", "release"):
        phase = phases.add_parser(name)
        phase.add_argument("--binary", required=True, type=Path)
        phase.add_argument("--work-root", required=True, type=Path)
        if name == "prepare":
            phase.add_argument("--payload-count", type=positive_int, default=PAYLOAD_COUNT)
            phase.add_argument("--payload-bytes", type=positive_int, default=PAYLOAD_BYTES)
        if name in ("measure", "release"):
            phase.add_argument("--output", required=True, type=Path)
        if name == "release":
            phase.add_argument("--repository", type=Path, default=Path.cwd())
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
    if args.phase == "measure":
        measure_prepared(binary, parent, args.output.resolve())
    else:
        release_prepared(binary, parent, args.output.resolve(), args.repository.resolve())


if __name__ == "__main__":
    main()
