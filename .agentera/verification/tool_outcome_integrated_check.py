#!/usr/bin/env python3
"""Build an aggregate-safe disposable fixture and verify outcome reports."""

import argparse
import datetime
import hashlib
import json
import math
import os
from pathlib import Path
import re
import sqlite3
import subprocess


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./build/ktx")
    parser.add_argument("--root", required=True)
    args = parser.parse_args()

    binary = Path(args.binary).resolve()
    root = Path(args.root).resolve()
    if binary != (Path.cwd() / "build/ktx").resolve():
        raise SystemExit("--binary must resolve to this checkout's ./build/ktx")
    if root.exists() and any(root.iterdir()):
        raise SystemExit("--root must be absent or empty")
    roots = {name: root / name for name in ("home", "data", "config", "cache", "state", "runtime", "db")}
    for path in roots.values():
        path.mkdir(parents=True, exist_ok=True)

    base_env = os.environ.copy()
    base_env.pop("KTX_DB", None)
    base_env.update({
        "HOME": str(roots["home"]),
        "XDG_DATA_HOME": str(roots["data"]),
        "XDG_CONFIG_HOME": str(roots["config"]),
        "XDG_CACHE_HOME": str(roots["cache"]),
        "XDG_STATE_HOME": str(roots["state"]),
        "XDG_RUNTIME_DIR": str(roots["runtime"]),
    })
    now = datetime.datetime.now(datetime.timezone.utc)

    def run(database, *command, stdin=None):
        env = base_env.copy()
        env["KTX_DB"] = str(database)
        result = subprocess.run(
            [str(binary), *command], input=stdin, text=True, capture_output=True, env=env, check=False
        )
        if result.returncode:
            raise AssertionError(f"{command} exited {result.returncode}: {result.stderr}")
        return result.stdout

    def write(database, ident, output, minutes, is_error=None, exit_code=None, include_error=False):
        record = {
            "id": ident,
            "tool": "bash",
            "input": '{"command":"aggregate-safe-fixture"}',
            "timestamp": (now - datetime.timedelta(minutes=minutes)).isoformat().replace("+00:00", "Z"),
        }
        if output is not None:
            record["output"] = output
            record["content_hash"] = hashlib.sha256(output.encode()).hexdigest()
        if include_error:
            record["is_error"] = is_error
        if exit_code is not None:
            record["exit_code"] = exit_code
        run(database, "write", stdin=json.dumps(record))

    mixed = roots["db"] / "mixed.db"
    records = [
        ("aaaaaaaaaaaaaaab", "success-alpha", 5, False, 0, True),
        ("aaaaaaaaaaaaaaac", "success-beta", 10, False, 0, True),
        ("aaaaaaaaaaaaaaad", "failure-gamma", 15, True, 7, True),
        ("aaaaaaaaaaaaaaae", "integer-malformed", 20, None, None, False),
        ("aaaaaaaaaaaaaaaf", "real-malformed", 25, None, None, False),
        ("aaaaaaaaaaaaaaag", "text-malformed", 30, None, None, False),
        ("aaaaaaaaaaaaaaah", "text-zero", 35, None, None, False),
        ("aaaaaaaaaaaaaaai", "text-one", 40, None, None, False),
        ("aaaaaaaaaaaaaaaj", "blob-malformed", 45, None, None, False),
        ("aaaaaaaaaaaaaaak", "null-unknown", 50, None, None, False),
        ("aaaaaaaaaaaaaaal", "exit-only", 55, None, 9, False),
        ("aaaaaaaaaaaaaaam", "outside-window", 180, None, None, False),
        ("aaaaaaaaaaaaaaan", None, 5, None, None, False),
    ]
    for record in records:
        write(mixed, *record)

    # Remove only is_error's declared affinity so exact text "0" and "1" can
    # represent malformed storage classes in this deliberately corrupt fixture.
    with sqlite3.connect(mixed) as connection:
        schema = connection.execute("SELECT sql FROM sqlite_schema WHERE type='table' AND name='tool_calls'").fetchone()[0]
        changed = re.sub(r"(\bis_error\s+)INTEGER\b", r"\1", schema, count=1, flags=re.IGNORECASE)
        assert changed != schema
        connection.execute("PRAGMA writable_schema=ON")
        connection.execute("UPDATE sqlite_schema SET sql=? WHERE type='table' AND name='tool_calls'", (changed,))
        version = connection.execute("PRAGMA schema_version").fetchone()[0]
        connection.execute(f"PRAGMA schema_version={version + 1}")
    with sqlite3.connect(mixed) as connection:
        updates = [
            (7, "aaaaaaaaaaaaaaae"),
            (0.5, "aaaaaaaaaaaaaaaf"),
            ("malformed", "aaaaaaaaaaaaaaag"),
            ("0", "aaaaaaaaaaaaaaah"),
            ("1", "aaaaaaaaaaaaaaai"),
            (sqlite3.Binary(b"\x00"), "aaaaaaaaaaaaaaaj"),
            (None, "aaaaaaaaaaaaaaak"),
        ]
        connection.executemany("UPDATE tool_calls SET is_error=? WHERE id=?", updates)
        connection.execute("UPDATE tool_calls SET output=NULL, content_hash=NULL WHERE id='aaaaaaaaaaaaaaan'")
        stored = dict(connection.execute("SELECT id, typeof(is_error) FROM tool_calls WHERE id BETWEEN 'aaaaaaaaaaaaaaae' AND 'aaaaaaaaaaaaaaak'"))
        assert stored == {
            "aaaaaaaaaaaaaaae": "integer", "aaaaaaaaaaaaaaaf": "real",
            "aaaaaaaaaaaaaaag": "text", "aaaaaaaaaaaaaaah": "text",
            "aaaaaaaaaaaaaaai": "text", "aaaaaaaaaaaaaaaj": "blob",
            "aaaaaaaaaaaaaaak": "null",
        }

    all_known = roots["db"] / "all-known.db"
    write(all_known, "bbbbbbbbbbbbbbba", "known-success", 5, False, 0, True)
    write(all_known, "bbbbbbbbbbbbbbbb", "known-failure", 10, True, 1, True)

    all_unknown = roots["db"] / "all-unknown.db"
    write(all_unknown, "ccccccccccccccca", "plain-unknown", 5)
    write(all_unknown, "cccccccccccccccb", "malformed-unknown", 10)
    write(all_unknown, "cccccccccccccccc", "exit-only-unknown", 15, exit_code=4)
    with sqlite3.connect(all_unknown) as connection:
        connection.execute("UPDATE tool_calls SET is_error=-1 WHERE id='cccccccccccccccb'")

    empty = roots["db"] / "empty.db"
    write(empty, "dddddddddddddddd", "delete-me", 5, False, 0, True)
    with sqlite3.connect(empty) as connection:
        connection.execute("DELETE FROM compression_dispositions")
        connection.execute("DELETE FROM tool_calls")
        connection.execute("DELETE FROM sessions")

    def equal(actual, expected):
        return actual is expected if actual is None or expected is None else math.isclose(actual, expected, abs_tol=1e-9)

    def assert_outcomes(outcomes, success, error, unknown, error_rate, coverage):
        actual = (
            outcomes["successful_calls"], outcomes["error_calls"],
            outcomes.get("unknown_calls", outcomes.get("unknown_status_calls")),
            outcomes.get("known_calls", outcomes.get("known_status_calls")),
            outcomes.get("total_calls", outcomes.get("total_tool_calls")),
        )
        assert actual == (success, error, unknown, success + error, success + error + unknown), outcomes
        assert equal(outcomes["error_rate_pct_of_known"], error_rate), outcomes
        assert equal(outcomes["outcome_coverage_pct"], coverage), outcomes

    def contains_outcomes(value):
        if isinstance(value, dict):
            if any(key in value for key in ("outcomes", "successful_calls", "error_calls", "unknown_calls", "known_calls", "outcome_coverage_pct")):
                return True
            return any(contains_outcomes(item) for item in value.values())
        if isinstance(value, list):
            return any(contains_outcomes(item) for item in value)
        return False

    cases = [
        ("mixed", mixed, (2, 1, 8, 100 / 3, 300 / 11)),
        ("empty", empty, (0, 0, 0, None, None)),
        ("all-known", all_known, (1, 1, 0, 50.0, 100.0)),
        ("all-unknown", all_unknown, (0, 0, 3, None, 0.0)),
    ]
    results = {}
    for name, database, expected in cases:
        summary = json.loads(run(database, "stats", "summary", "--window", "1h", "--format", "json"))
        breakdown = json.loads(run(database, "stats", "breakdown", "--window", "1h", "--format", "json"))
        summary_text = run(database, "stats", "summary", "--window", "1h", "--format", "text")
        breakdown_text = run(database, "stats", "breakdown", "--window", "1h", "--format", "text")
        assert summary["schema_version"] == 5 and breakdown["schema_version"] == 2
        assert_outcomes(summary["outcomes"], *expected)
        assert summary["outcomes"] == breakdown["outcomes"]
        assert summary["calls"] == breakdown["calls"] and summary["tokens"] == breakdown["tokens"]
        assert summary["calls"]["total_with_output"] == sum(expected[:3])
        assert not contains_outcomes(breakdown["delivery_breakdown"])
        assert "ERROR_PCT_KNOWN" in summary_text and "OUTCOME_COVERAGE_PCT" in summary_text
        assert "ERROR_PCT_KNOWN" in breakdown_text and "OUTCOME_COVERAGE_PCT" in breakdown_text
        results[name] = {"stats": summary["outcomes"], "calls": summary["calls"], "tokens": summary["tokens"]}

    report_cases = [
        ("mixed", mixed, (2, 1, 10, 33.33, 23.08)),
        ("empty", empty, (0, 0, 0, None, None)),
        ("all-known", all_known, (1, 1, 0, 50.0, 100.0)),
        ("all-unknown", all_unknown, (0, 0, 3, None, 0.0)),
    ]
    for name, database, expected in report_cases:
        before = hashlib.sha256(database.read_bytes()).hexdigest()
        with sqlite3.connect(f"file:{database}?mode=ro", uri=True) as connection:
            version_before = connection.execute("PRAGMA user_version").fetchone()[0]
        raw = run(database, "db", "--path", str(database), "report", "--output", "-")
        report = json.loads(raw)
        after = hashlib.sha256(database.read_bytes()).hexdigest()
        with sqlite3.connect(f"file:{database}?mode=ro", uri=True) as connection:
            version_after = connection.execute("PRAGMA user_version").fetchone()[0]
        assert report["report_schema_version"] == 3
        assert_outcomes(report["failure_metrics"], *expected)
        assert before == after and version_before == version_after == 9
        assert str(root) not in raw and "database_path" not in report["source"]
        coverage = report["measurement_coverage"]["coverage"]["error_status"]
        assert coverage["measured_calls"] == expected[0] + expected[1]
        assert equal(coverage["coverage_pct"], expected[4])
        results[name]["report"] = {
            "outcomes": report["failure_metrics"], "sha256_before": before,
            "sha256_after": after, "user_version_before": version_before,
            "user_version_after": version_after, "source": report["source"],
        }

    results["binary"] = {
        "version": run(roots["db"] / "identity.db", "--version").strip(),
        "sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
    }
    print(json.dumps(results, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
