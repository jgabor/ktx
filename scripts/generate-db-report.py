#!/usr/bin/env python3
"""
Pull and aggregate data from the live ktx database into a JSON file.

Reports the sessions, tool_calls, compressed, and recorded_streams tables.
Benchmark runs are intentionally excluded.

Usage:
    scripts/generate-db-report.py [--db <path>] [--output <path>|-]

Defaults:
    --db      $KTX_DB or $XDG_DATA_HOME/ktx/ktx.db or ~/.local/share/ktx/ktx.db
    --output  docs/notes/ktx-database-analysis.json
"""

import argparse
import hashlib
import json
import math
import os
import sqlite3
import sys
import tempfile
from collections import defaultdict
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path

DEFAULT_DB = os.environ.get(
    "KTX_DB",
    os.path.join(os.environ.get("XDG_DATA_HOME", os.path.expanduser("~/.local/share")), "ktx", "ktx.db"),
)
DEFAULT_OUTPUT = "docs/notes/ktx-database-analysis.json"
REPORT_SCHEMA_VERSION = 2
REPORT_TABLES = ("sessions", "tool_calls", "compressed", "recorded_streams")

PIPELINE_LABELS = {
    '["strip-ansi","dedup-blank","normalize","keep-ends:50:20","token-budget:2000"]': "Conservative A",
    '["strip-ansi","dedup-blank","normalize","keep-ends:200:100","token-budget:8000"]': "Conservative B",
    '["strip-ansi","dedup-blank","normalize"]': "Lossless-ish",
    '["strip-ansi","dedup-blank"]': "Minimal",
    '["strip-ansi","dedup-blank","head:30"]': "Aggressive Head",
    '["strip-ansi","dedup-blank","keep-ends:50:20","token-budget:2000"]': "Conservative A (no normalize)",
    '["strip-ansi","dedup-blank","normalize","token-budget:8000"]': "Token-budget 8K",
    "[]": "Passthrough",
}


@contextmanager
def open_database(db_path):
    uri = Path(db_path).resolve().as_uri() + "?mode=ro"
    conn = sqlite3.connect(uri, uri=True)
    conn.row_factory = sqlite3.Row
    try:
        conn.execute("PRAGMA query_only = ON")
        conn.execute("BEGIN")
        yield conn
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


def query_json(conn, sql, params=()):
    rows = conn.execute(sql, params).fetchall()
    return [dict(r) for r in rows]


def query_one(conn, sql, params=()):
    return dict(conn.execute(sql, params).fetchone())


def percentage(numerator, denominator):
    if denominator in (None, 0):
        return None
    return round(100.0 * numerator / denominator, 2)


def reduction_pct(original_bytes, result_bytes):
    if not original_bytes:
        return None
    return round(100.0 * (original_bytes - result_bytes) / original_bytes, 2)


def percentile(sorted_values, fraction):
    if not sorted_values:
        return None
    position = (len(sorted_values) - 1) * fraction
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return sorted_values[lower]
    weight = position - lower
    return round(sorted_values[lower] * (1 - weight) + sorted_values[upper] * weight, 2)


def distribution(values):
    values = sorted(v for v in values if v is not None)
    if not values:
        return {
            "count": 0,
            "min": None,
            "p50": None,
            "p95": None,
            "max": None,
            "average": None,
        }
    return {
        "count": len(values),
        "min": values[0],
        "p50": percentile(values, 0.50),
        "p95": percentile(values, 0.95),
        "max": values[-1],
        "average": round(sum(values) / len(values), 2),
    }


def pipeline_label(pipeline):
    if pipeline is None:
        return None
    if pipeline in PIPELINE_LABELS:
        return PIPELINE_LABELS[pipeline]
    return pipeline[:40] + "..." if len(pipeline) > 40 else pipeline


def extract_schema(conn):
    schema = {}
    for table in REPORT_TABLES:
        columns = query_json(conn, f"PRAGMA table_info({table})")
        if not columns:
            raise RuntimeError(f"required table is missing: {table}")
        schema[table] = [
            {
                "name": column["name"],
                "type": column["type"],
                "nullable": not bool(column["notnull"]),
                "default": column["dflt_value"],
                "primary_key": bool(column["pk"]),
            }
            for column in columns
        ]
    return schema


def extract_overview(conn):
    sessions = query_one(conn, """
        SELECT COUNT(*) AS total_sessions,
               COALESCE(SUM(CASE WHEN ended_at IS NULL OR ended_at = '' THEN 1 ELSE 0 END), 0) AS open_sessions,
               COALESCE(SUM(CASE WHEN ended_at IS NOT NULL AND ended_at <> '' THEN 1 ELSE 0 END), 0) AS closed_sessions
        FROM sessions
    """)
    calls = query_one(conn, """
        SELECT COUNT(*) AS total_tool_calls,
               COALESCE(SUM(length(CAST(input AS BLOB))), 0) AS total_input_bytes,
               COALESCE(SUM(length(CAST(output AS BLOB))), 0) AS total_output_bytes,
               COUNT(DISTINCT session_id) AS sessions_with_tool_calls
        FROM tool_calls
    """)
    streams = query_one(conn, """
        SELECT COUNT(*) AS total_recorded_streams,
               COALESCE(SUM(length(CAST(stream_text AS BLOB))), 0) AS total_stream_bytes,
               COUNT(DISTINCT session_id) AS sessions_with_recorded_streams
        FROM recorded_streams
    """)
    return {**sessions, **calls, **streams}


def extract_measurement_coverage(conn):
    metrics = query_one(conn, """
        SELECT COUNT(*) AS total_tool_calls,
               COUNT(tokens_in) AS calls_with_tokens_in,
               COUNT(tokens_out) AS calls_with_tokens_out,
               COUNT(cost) AS calls_with_cost,
               COUNT(latency_ms) AS calls_with_latency,
               COUNT(model) AS calls_with_model,
               COUNT(provider) AS calls_with_provider,
               COUNT(exit_code) AS calls_with_exit_code,
               COUNT(is_error) AS calls_with_error_status,
               SUM(tokens_in) AS total_tokens_in,
               SUM(tokens_out) AS total_tokens_out,
               SUM(cost) AS total_cost
        FROM tool_calls
    """)
    total = metrics.pop("total_tool_calls")
    coverage = {}
    for name in (
        "tokens_in",
        "tokens_out",
        "cost",
        "latency",
        "model",
        "provider",
        "exit_code",
        "error_status",
    ):
        count = metrics.pop(f"calls_with_{name}")
        coverage[name] = {
            "measured_calls": count,
            "coverage_pct": percentage(count, total),
        }

    latency_values = [
        row[0]
        for row in conn.execute("SELECT latency_ms FROM tool_calls WHERE latency_ms IS NOT NULL")
    ]
    return {
        "total_tool_calls": total,
        "coverage": coverage,
        "totals": metrics,
        "latency_ms_distribution": distribution(latency_values),
        "models": query_json(conn, """
            SELECT model, COUNT(*) AS call_count,
                   SUM(tokens_in) AS tokens_in,
                   SUM(tokens_out) AS tokens_out,
                   SUM(cost) AS cost
            FROM tool_calls
            WHERE model IS NOT NULL
            GROUP BY model
            ORDER BY call_count DESC, model
        """),
        "providers": query_json(conn, """
            SELECT provider, COUNT(*) AS call_count,
                   SUM(tokens_in) AS tokens_in,
                   SUM(tokens_out) AS tokens_out,
                   SUM(cost) AS cost
            FROM tool_calls
            WHERE provider IS NOT NULL
            GROUP BY provider
            ORDER BY call_count DESC, provider
        """),
    }


def extract_failure_metrics(conn):
    statuses = query_one(conn, """
        SELECT COALESCE(SUM(CASE WHEN is_error = 1 THEN 1 ELSE 0 END), 0) AS error_calls,
               COALESCE(SUM(CASE WHEN is_error = 0 THEN 1 ELSE 0 END), 0) AS successful_calls,
               COALESCE(SUM(CASE WHEN is_error IS NULL THEN 1 ELSE 0 END), 0) AS unknown_status_calls
        FROM tool_calls
    """)
    known = statuses["error_calls"] + statuses["successful_calls"]
    statuses["error_rate_pct_of_known"] = percentage(statuses["error_calls"], known)
    statuses["exit_codes"] = query_json(conn, """
        SELECT exit_code, COUNT(*) AS call_count
        FROM tool_calls
        WHERE exit_code IS NOT NULL
        GROUP BY exit_code
        ORDER BY call_count DESC, exit_code
    """)
    return statuses


def extract_tool_metrics(conn):
    return query_json(conn, """
        SELECT tool,
               COUNT(*) AS call_count,
               COUNT(DISTINCT session_id) AS session_count,
               COALESCE(SUM(length(CAST(input AS BLOB))), 0) AS input_bytes,
               COALESCE(SUM(length(CAST(output AS BLOB))), 0) AS output_bytes,
               SUM(tokens_in) AS tokens_in,
               SUM(tokens_out) AS tokens_out,
               SUM(cost) AS cost,
               ROUND(AVG(latency_ms), 2) AS average_latency_ms,
               SUM(CASE WHEN is_error = 1 THEN 1 ELSE 0 END) AS error_calls,
               SUM(CASE WHEN is_error IS NULL THEN 1 ELSE 0 END) AS unknown_status_calls
        FROM tool_calls
        GROUP BY tool
        ORDER BY call_count DESC, tool
    """)


def extract_agent_metrics(conn):
    return query_json(conn, """
        WITH session_counts AS (
            SELECT agent, COUNT(*) AS session_count
            FROM sessions
            GROUP BY agent
        ),
        call_counts AS (
            SELECT s.agent,
                   COUNT(tc.id) AS tool_call_count,
                   COALESCE(SUM(length(CAST(tc.output AS BLOB))), 0) AS output_bytes,
                   SUM(tc.tokens_in) AS tokens_in,
                   SUM(tc.tokens_out) AS tokens_out,
                   SUM(tc.cost) AS cost,
                   ROUND(AVG(tc.latency_ms), 2) AS average_latency_ms,
                   SUM(CASE WHEN tc.is_error = 1 THEN 1 ELSE 0 END) AS error_calls
            FROM sessions s
            LEFT JOIN tool_calls tc ON tc.session_id = s.id
            GROUP BY s.agent
        )
        SELECT sc.agent, sc.session_count,
               cc.tool_call_count, cc.output_bytes,
               cc.tokens_in, cc.tokens_out, cc.cost,
               cc.average_latency_ms, cc.error_calls
        FROM session_counts sc
        JOIN call_counts cc ON sc.agent IS cc.agent
        ORDER BY cc.tool_call_count DESC, sc.agent
    """)


def compression_group(rows, key):
    grouped = defaultdict(list)
    for row in rows:
        grouped[row[key]].append(row)

    result = []
    for group_key, artifacts in grouped.items():
        original_bytes = sum(row["original_bytes"] for row in artifacts)
        compressed_bytes = sum(row["compressed_bytes"] for row in artifacts)
        savings = [row["original_bytes"] - row["compressed_bytes"] for row in artifacts]
        ratios = [
            reduction_pct(row["original_bytes"], row["compressed_bytes"])
            for row in artifacts
        ]
        item = {
            key: group_key,
            "artifact_count": len(artifacts),
            "unique_source_count": len({row["source_id"] for row in artifacts}),
            "source_bytes_across_artifacts": original_bytes,
            "compressed_bytes": compressed_bytes,
            "bytes_saved": original_bytes - compressed_bytes,
            "reduction_pct": reduction_pct(original_bytes, compressed_bytes),
            "improved_artifacts": sum(value > 0 for value in savings),
            "passthrough_artifacts": sum(value == 0 for value in savings),
            "worsened_artifacts": sum(value < 0 for value in savings),
            "bytes_saved_distribution": distribution(savings),
            "reduction_pct_distribution": distribution(ratios),
        }
        if key == "pipeline":
            item["label"] = pipeline_label(group_key)
        result.append(item)
    return sorted(result, key=lambda item: (-item["artifact_count"], str(item[key])))


def extract_compression_metrics(conn, total_tool_calls, include_identifiers):
    rows = query_json(conn, """
        SELECT c.id, c.source_id, c.pipeline, c.timestamp,
               tc.tool, tc.session_id, s.agent, s.started_at,
               COALESCE(length(CAST(tc.output AS BLOB)), 0) AS original_bytes,
               COALESCE(length(CAST(c.output AS BLOB)), 0) AS compressed_bytes
        FROM compressed c
        JOIN tool_calls tc ON tc.id = c.source_id
        JOIN sessions s ON s.id = tc.session_id
        ORDER BY c.timestamp, c.id
    """)
    source_counts = defaultdict(int)
    for row in rows:
        source_counts[row["source_id"]] += 1

    original_bytes = sum(row["original_bytes"] for row in rows)
    compressed_bytes = sum(row["compressed_bytes"] for row in rows)
    unique_sources = len(source_counts)
    unique_source_bytes = sum({
        row["source_id"]: row["original_bytes"] for row in rows
    }.values())
    savings = [row["original_bytes"] - row["compressed_bytes"] for row in rows]

    session_groups = compression_group(rows, "session_id")
    session_call_stats = {}
    for row in query_json(conn, """
            SELECT tc.session_id, COUNT(*) AS tool_call_count,
                   COALESCE(SUM(length(CAST(tc.output AS BLOB))), 0) AS total_output_bytes
            FROM tool_calls tc
            GROUP BY tc.session_id
        """):
        session_call_stats[row.pop("session_id")] = row
    session_metadata = {}
    for row in rows:
        session_metadata[row["session_id"]] = {
            "agent": row["agent"],
            "started_at": row["started_at"],
        }

    top_sessions = []
    for rank, row in enumerate(session_groups[:20], start=1):
        session_id = row.pop("session_id")
        row["rank"] = rank
        row.update(session_metadata[session_id])
        row.update(session_call_stats[session_id])
        if include_identifiers:
            row["session_id"] = session_id
        top_sessions.append(row)

    return {
        "overview": {
            "artifact_count": len(rows),
            "unique_source_count": unique_sources,
            "recompressed_source_count": sum(count > 1 for count in source_counts.values()),
            "maximum_artifacts_per_source": max(source_counts.values(), default=0),
            "sessions_with_compression": len({row["session_id"] for row in rows}),
            "source_coverage_pct": percentage(unique_sources, total_tool_calls),
            "unique_source_bytes": unique_source_bytes,
            "source_bytes_across_artifacts": original_bytes,
            "compressed_bytes": compressed_bytes,
            "bytes_saved": original_bytes - compressed_bytes,
            "reduction_pct": reduction_pct(original_bytes, compressed_bytes),
            "improved_artifacts": sum(value > 0 for value in savings),
            "passthrough_artifacts": sum(value == 0 for value in savings),
            "worsened_artifacts": sum(value < 0 for value in savings),
            "bytes_saved_distribution": distribution(savings),
            "reduction_pct_distribution": distribution([
                reduction_pct(row["original_bytes"], row["compressed_bytes"])
                for row in rows
            ]),
        },
        "by_tool": compression_group(rows, "tool"),
        "by_pipeline": compression_group(rows, "pipeline"),
        "top_sessions": top_sessions,
    }


def extract_session_lifecycle(conn, include_identifiers):
    rows = query_json(conn, """
        SELECT s.id, s.tool_call_count AS recorded_tool_call_count,
               COUNT(tc.id) AS actual_tool_call_count,
               s.started_at, s.ended_at, s.last_seen_at,
               CASE
                   WHEN julianday(s.ended_at) >= julianday(s.started_at)
                   THEN CAST((julianday(s.ended_at) - julianday(s.started_at)) * 86400000 AS INTEGER)
               END AS duration_ms
        FROM sessions s
        LEFT JOIN tool_calls tc ON tc.session_id = s.id
        GROUP BY s.id
        ORDER BY s.id
    """)
    durations = []
    mismatches = []
    for row in rows:
        if row["duration_ms"] is not None:
            durations.append(row["duration_ms"])
        if row["recorded_tool_call_count"] != row["actual_tool_call_count"]:
            mismatch = {
                "recorded_tool_call_count": row["recorded_tool_call_count"],
                "actual_tool_call_count": row["actual_tool_call_count"],
            }
            if include_identifiers:
                mismatch["session_id"] = row["id"]
            mismatches.append(mismatch)

    return {
        "open_sessions": sum(not row["ended_at"] for row in rows),
        "closed_sessions": sum(bool(row["ended_at"]) for row in rows),
        "zero_call_sessions": sum(row["actual_tool_call_count"] == 0 for row in rows),
        "missing_started_at": sum(not row["started_at"] for row in rows),
        "missing_last_seen_at": sum(not row["last_seen_at"] for row in rows),
        "duration_ms_distribution": distribution(durations),
        "rollup_mismatch_count": len(mismatches),
        "rollup_mismatches": mismatches,
    }


def extract_recorded_stream_metrics(conn):
    overview = query_one(conn, """
        SELECT COUNT(*) AS stream_count,
               COUNT(DISTINCT session_id) AS session_count,
               COALESCE(SUM(length(CAST(stream_text AS BLOB))), 0) AS stream_bytes,
               MIN(captured_at) AS first_captured_at,
               MAX(captured_at) AS last_captured_at
        FROM recorded_streams
    """)
    overview["formats"] = query_json(conn, """
        SELECT stream_format, COUNT(*) AS stream_count,
               COALESCE(SUM(length(CAST(stream_text AS BLOB))), 0) AS stream_bytes
        FROM recorded_streams
        GROUP BY stream_format
        ORDER BY stream_count DESC, stream_format
    """)
    return overview


def extract_daily_activity(conn):
    rows = query_json(conn, """
        WITH dates AS (
            SELECT DATE(started_at) AS date FROM sessions
            UNION
            SELECT DATE(timestamp) AS date FROM tool_calls
            UNION
            SELECT DATE(timestamp) AS date FROM compressed
            UNION
            SELECT DATE(captured_at) AS date FROM recorded_streams
        ),
        session_daily AS (
            SELECT DATE(started_at) AS date, COUNT(*) AS sessions
            FROM sessions
            WHERE DATE(started_at) IS NOT NULL
            GROUP BY DATE(started_at)
        ),
        call_daily AS (
            SELECT DATE(timestamp) AS date,
                   COUNT(*) AS tool_calls,
                   COALESCE(SUM(length(CAST(output AS BLOB))), 0) AS output_bytes,
                   SUM(tokens_in) AS tokens_in,
                   SUM(tokens_out) AS tokens_out,
                   SUM(cost) AS cost,
                   SUM(CASE WHEN is_error = 1 THEN 1 ELSE 0 END) AS error_calls
            FROM tool_calls
            WHERE DATE(timestamp) IS NOT NULL
            GROUP BY DATE(timestamp)
        ),
        compression_daily AS (
            SELECT DATE(c.timestamp) AS date,
                   COUNT(*) AS compression_artifacts,
                   COUNT(DISTINCT c.source_id) AS compressed_sources,
                   COALESCE(SUM(length(CAST(tc.output AS BLOB))), 0) AS source_bytes,
                   COALESCE(SUM(length(CAST(c.output AS BLOB))), 0) AS compressed_bytes
            FROM compressed c
            JOIN tool_calls tc ON tc.id = c.source_id
            WHERE DATE(c.timestamp) IS NOT NULL
            GROUP BY DATE(c.timestamp)
        ),
        stream_daily AS (
            SELECT DATE(captured_at) AS date,
                   COUNT(*) AS recorded_streams,
                   COALESCE(SUM(length(CAST(stream_text AS BLOB))), 0) AS stream_bytes
            FROM recorded_streams
            WHERE DATE(captured_at) IS NOT NULL
            GROUP BY DATE(captured_at)
        )
        SELECT d.date,
               COALESCE(s.sessions, 0) AS sessions,
               COALESCE(tc.tool_calls, 0) AS tool_calls,
               COALESCE(tc.output_bytes, 0) AS output_bytes,
               tc.tokens_in, tc.tokens_out, tc.cost,
               COALESCE(tc.error_calls, 0) AS error_calls,
               COALESCE(c.compression_artifacts, 0) AS compression_artifacts,
               COALESCE(c.compressed_sources, 0) AS compressed_sources,
               COALESCE(c.source_bytes, 0) AS compression_source_bytes,
               COALESCE(c.compressed_bytes, 0) AS compressed_bytes,
               COALESCE(c.source_bytes, 0) - COALESCE(c.compressed_bytes, 0) AS bytes_saved,
               COALESCE(rs.recorded_streams, 0) AS recorded_streams,
               COALESCE(rs.stream_bytes, 0) AS stream_bytes
        FROM dates d
        LEFT JOIN session_daily s ON s.date = d.date
        LEFT JOIN call_daily tc ON tc.date = d.date
        LEFT JOIN compression_daily c ON c.date = d.date
        LEFT JOIN stream_daily rs ON rs.date = d.date
        WHERE d.date IS NOT NULL
        ORDER BY d.date
    """)
    for row in rows:
        row["compression_reduction_pct"] = reduction_pct(
            row["compression_source_bytes"], row["compressed_bytes"]
        )
    return rows


def extract_integrity(conn):
    checks = {}
    for table, content_column in (
        ("tool_calls", "output"),
        ("compressed", "output"),
        ("recorded_streams", "stream_text"),
    ):
        counts = {
            "row_count": 0,
            "checked_count": 0,
            "valid_count": 0,
            "mismatch_count": 0,
            "missing_hash_count": 0,
        }
        cursor = conn.execute(
            f"SELECT content_hash, CAST({content_column} AS BLOB) FROM {table}"
        )
        for stored_hash, content in cursor:
            counts["row_count"] += 1
            if not stored_hash:
                counts["missing_hash_count"] += 1
                continue
            counts["checked_count"] += 1
            actual_hash = hashlib.sha256(content or b"").hexdigest()
            if actual_hash == stored_hash:
                counts["valid_count"] += 1
            else:
                counts["mismatch_count"] += 1
        checks[table] = counts
    return checks


def generate_report(db_path, include_identifiers=False):
    with open_database(db_path) as conn:
        schema = extract_schema(conn)
        overview = extract_overview(conn)
        report = {
            "report_schema_version": REPORT_SCHEMA_VERSION,
            "generated_at": datetime.now(timezone.utc).isoformat(),
            "source": {"database_file": Path(db_path).name},
            "units": {
                "storage": "UTF-8 bytes",
                "latency": "milliseconds",
                "cost": "USD",
                "percentages": "0-100",
            },
            "database_schema": schema,
            "overview": overview,
            "measurement_coverage": extract_measurement_coverage(conn),
            "failure_metrics": extract_failure_metrics(conn),
            "tool_metrics": extract_tool_metrics(conn),
            "agent_metrics": extract_agent_metrics(conn),
            "compression": extract_compression_metrics(
                conn, overview["total_tool_calls"], include_identifiers
            ),
            "session_lifecycle": extract_session_lifecycle(conn, include_identifiers),
            "recorded_streams": extract_recorded_stream_metrics(conn),
            "daily_activity": extract_daily_activity(conn),
            "integrity": extract_integrity(conn),
        }
        if include_identifiers:
            report["source"]["database_path"] = str(Path(db_path).resolve())
        return report


def write_json(path, text):
    if path == "-":
        sys.stdout.write(text)
        return

    output = Path(path)
    output.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary_path = tempfile.mkstemp(
        prefix=f".{output.name}.", suffix=".tmp", dir=output.parent
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary_path, 0o644)
        os.replace(temporary_path, output)
    except Exception:
        try:
            os.unlink(temporary_path)
        except FileNotFoundError:
            pass
        raise


def main():
    parser = argparse.ArgumentParser(
        description="Aggregate non-benchmark ktx database evidence into JSON"
    )
    parser.add_argument("--db", default=DEFAULT_DB, help="Path to ktx.db")
    parser.add_argument(
        "--output", default=DEFAULT_OUTPUT, help="Output JSON path, or - for stdout"
    )
    parser.add_argument(
        "--include-identifiers",
        action="store_true",
        help="Include session IDs and the absolute database path",
    )
    args = parser.parse_args()

    if not os.path.isfile(args.db):
        print(f"fatal: database not found: {args.db}", file=sys.stderr)
        return 2

    try:
        print(f"querying database: {args.db}", file=sys.stderr)
        report = generate_report(args.db, args.include_identifiers)
        json_out = json.dumps(report, indent=2, ensure_ascii=False) + "\n"
        write_json(args.output, json_out)
    except (OSError, RuntimeError, sqlite3.Error) as error:
        print(f"fatal: could not generate report: {error}", file=sys.stderr)
        return 1

    destination = "stdout" if args.output == "-" else args.output
    print(
        f"json written: {destination} ({len(json_out.encode('utf-8')):,} bytes)",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
