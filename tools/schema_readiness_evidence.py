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
APPROVED_LIMITS = {
    "readiness_latency_ns": 35_322_974,
    "exclusive_lock_hold_ns": 101_037_622,
    "journal_growth_bytes": 50_000,
    "peak_rss_bytes": 26_557_440,
    "total_completion_ns": 400_000_000,
}
LIMIT_BASIS = (
    "user-approved override dated 2026-08-24 after production-shaped bounded migration evidence; "
    "readiness latency, maximum individual lock, and peak RSS limits are unchanged"
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
    result = {
        "readiness_latency_ns": readiness,
        "exclusive_lock_hold_ns": lock_hold,
        "journal_growth_bytes": peak_journal,
        "peak_rss_bytes": peak_rss,
        "total_completion_ns": completion,
        "exclusive_lock_intervals_ns": lock_intervals,
        "stdout": stdout.strip(),
    }
    return result


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
            "limit": "approved fixed limits, including user-approved journal and completion overrides",
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


def criterion(name, passed, evidence):
    return {"criterion": name, "status": "PASS" if passed else "FAIL", "evidence": evidence}


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
    schema_digest = physical_schema_digest(reference)
    reference.unlink()
    return {
        "schema": 15,
        "source_revision": record["source_revision"],
        "database_sha256": actual,
        "metadata_sha256": record["metadata_sha256"],
        "manifest_sha256": digest(manifest_path, "historical manifest"),
        "physical_schema_sha256": schema_digest,
    }


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
            if row and row[0]:
                os.kill(process.pid, signal.SIGSTOP)
                if process.poll() is not None:
                    raise RuntimeError("migration completed before it could be stopped")
                return row[0]
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
    cursor = wait_for_durable_cursor(database, first)
    query_env = env.copy()
    query_env["KTX_DB"] = str(database)
    query = subprocess.Popen(
        [str(binary), "list", "--format", "json"], env=query_env,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    first.kill()
    first.communicate(timeout=10)
    query_stdout, query_stderr = query.communicate(timeout=30)
    with sqlite3.connect(database) as connection:
        durable_rows = connection.execute(
            "SELECT count(*) FROM compressed WHERE id <= ?", (cursor,)
        ).fetchone()[0]
    restarted = run(command, env)
    result = validate_migrated_copy(database, expected_rows, expected_evidence_digest)
    result.update({
        "durable_cursor": cursor,
        "durable_rows_before_restart": durable_rows,
        "schema_before_restart": 15,
        "first_exit": first.returncode,
        "restart_exit": restarted.returncode,
        "concurrent_query_exit": query.returncode,
        "concurrent_query_output_sha256": bytes_digest(query_stdout.encode()),
        "concurrent_query_raw_lock_error": "database is locked" in query_stderr.lower(),
    })
    result["passed"] = (result["passed"] and durable_rows > 0
                        and query.returncode == 0 and "database is locked" not in query_stderr.lower())
    return result


def generated_adapter_gate(binary, repository, parent):
    env = roots(parent, "release-generated-adapter")
    env["KTX_TEST_REQUIRE_GENERATED_ADAPTER"] = "1"
    env["KTX_TEST_BUILT_BINARY"] = str(binary)
    started = time.monotonic_ns()
    result = subprocess.run(
        ["go", "test", "-race", "./internal/db", "-run", "^TestGeneratedOpenCodeAdapterReadinessOverlap$", "-count=1", "-timeout=30s"],
        cwd=repository, env=env, text=True, capture_output=True,
    )
    return {
        "passed": result.returncode == 0,
        "exit_code": result.returncode,
        "duration_ns": time.monotonic_ns() - started,
        "command": "go test -race ./internal/db -run ^TestGeneratedOpenCodeAdapterReadinessOverlap$ -count=1 -timeout=30s",
        "stderr": result.stderr.strip(),
    }


def release_prepared(binary, parent, output, repository):
    manifest, fixtures = load_prepared(parent)
    expected = {"payload_count": PAYLOAD_COUNT, "payload_bytes_each": PAYLOAD_BYTES,
                "timestamp_start_unix_nano": TIMESTAMP_START_NS,
                "timestamp_step_nanoseconds": TIMESTAMP_STEP_NS}
    scale_ok = manifest["fixture_inputs"] == expected and manifest["fixtures"]["schema15"]["schema"] == 15
    identity = historical_identity(repository, parent)
    generated_schema_digest = physical_schema_digest(fixtures["schema15"])
    identity["generated_physical_schema_sha256"] = generated_schema_digest
    identity_ok = generated_schema_digest == identity["physical_schema_sha256"]
    binary_hash = digest(binary, "release binary")
    adapter = generated_adapter_gate(binary, repository, parent)
    interruption = interruption_rehearsal(
        binary, parent, fixtures["schema15"], manifest["fixtures"]["schema15"]["rows"]
    )
    raw_path = parent / "release-measurements.json"
    try:
        measure_prepared(binary, parent, raw_path)
    except SystemExit:
        pass
    raw = json.loads(raw_path.read_text())
    criteria = [
        criterion("prior_schema_identity", identity_ok, identity),
        criterion("representative_scale", scale_ok, {"fixture_inputs": manifest["fixture_inputs"], "fixture": manifest["fixtures"]["schema15"]}),
        criterion("concurrent_generated_traffic", adapter["passed"], adapter),
        criterion("interruption_restart", interruption["passed"], interruption),
    ]
    for name, check in raw["limit_checks"].items():
        criteria.append(criterion(name, check["passed"], check))
    capabilities = raw["binary"]
    report = {
        "schema_version": 1,
        "release_gate": "PASS" if all(item["status"] == "PASS" for item in criteria) else "FAIL",
        "binary": {"sha256": binary_hash, "capabilities": capabilities},
        "policy": raw["policy"],
        "host": raw["host"],
        "criteria": criteria,
        "raw_measurements_sha256": digest(raw_path, "raw release measurements"),
    }
    if report["release_gate"] == "PASS":
        validate_release_report(report)
        destination = output
    else:
        destination = output.with_name(output.name + ".failed.json")
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(destination.name + ".tmp")
    temporary.write_text(json.dumps(report, indent=2) + "\n")
    os.replace(temporary, destination)
    progress("release", f"wrote {destination}")
    if report["release_gate"] != "PASS":
        failed = ", ".join(item["criterion"] for item in criteria if item["status"] == "FAIL")
        raise SystemExit("schema release gate failed: " + failed)


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
