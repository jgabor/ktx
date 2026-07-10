import hashlib
import importlib.util
import sqlite3
import tempfile
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).with_name("generate_db_report.py")
SPEC = importlib.util.spec_from_file_location("generate_db_report", SCRIPT_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"could not load {SCRIPT_PATH}")
REPORT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(REPORT)


SCHEMA = """
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    agent TEXT,
    started_at TEXT,
    ended_at TEXT,
    last_seen_at TEXT,
    tool_call_count INTEGER DEFAULT 0
);
CREATE TABLE tool_calls (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    tool TEXT,
    input TEXT,
    output TEXT,
    content_hash TEXT,
    timestamp TEXT,
    event_type TEXT DEFAULT 'tool_call',
    tokens_in INTEGER,
    tokens_out INTEGER,
    cost REAL,
    latency_ms INTEGER,
    model TEXT,
    provider TEXT,
    exit_code INTEGER,
    is_error INTEGER
);
CREATE TABLE compressed (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES tool_calls(id),
    output TEXT,
    content_hash TEXT,
    pipeline TEXT,
    timestamp TEXT
);
CREATE TABLE recorded_streams (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    bench_run_id TEXT,
    stream_text TEXT,
    content_hash TEXT,
    stream_format TEXT,
    captured_at TEXT
);
CREATE TABLE bench_runs (
    id TEXT PRIMARY KEY,
    score REAL
);
"""


def content_hash(value):
    return hashlib.sha256(value.encode()).hexdigest()


class GenerateDatabaseReportTest(unittest.TestCase):
    def make_database(self):
        temporary = tempfile.NamedTemporaryFile(suffix=".db", delete=False)
        temporary.close()
        self.addCleanup(Path(temporary.name).unlink, missing_ok=True)
        connection = sqlite3.connect(temporary.name)
        connection.executescript(SCHEMA)
        return temporary.name, connection

    def test_report_uses_schema_metrics_without_benchmark_data(self):
        path, connection = self.make_database()
        connection.executemany(
            """
            INSERT INTO sessions
                (id, agent, started_at, ended_at, last_seen_at, tool_call_count)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            [
                (
                    "s1",
                    "unknown",
                    "2026-07-01T00:00:00Z",
                    "2026-07-01T00:01:00Z",
                    "2026-07-01T00:01:00Z",
                    2,
                ),
                ("s2", "opencode", "2026-07-02T00:00:00Z", None, "2026-07-02T00:00:01Z", 1),
            ],
        )
        connection.executemany(
            """
            INSERT INTO tool_calls
                (id, session_id, tool, input, output, content_hash, timestamp,
                 tokens_in, tokens_out, cost, latency_ms, model, provider,
                 exit_code, is_error)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            [
                (
                    "t1",
                    "s1",
                    "bash",
                    "x",
                    "héllo",
                    content_hash("héllo"),
                    "2026-07-01T00:00:01Z",
                    10,
                    2,
                    0.25,
                    10,
                    "model-a",
                    "provider-a",
                    0,
                    0,
                ),
                (
                    "t2",
                    "s1",
                    "bash",
                    "y",
                    "ERROR",
                    content_hash("ERROR"),
                    "2026-07-01T00:00:02Z",
                    None,
                    None,
                    None,
                    30,
                    None,
                    None,
                    1,
                    1,
                ),
                (
                    "t3",
                    "s2",
                    "read",
                    "z",
                    "data",
                    content_hash("data"),
                    "2026-07-02T00:00:01Z",
                    None,
                    None,
                    None,
                    None,
                    None,
                    "provider-b",
                    None,
                    None,
                ),
            ],
        )
        connection.executemany(
            """
            INSERT INTO compressed
                (id, source_id, output, content_hash, pipeline, timestamp)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            [
                ("c1", "t1", "hi", content_hash("hi"), '["head:1"]', "2026-07-01T00:00:03Z"),
                ("c2", "t1", "h", content_hash("h"), '["head:1"]', "2026-07-01T00:00:04Z"),
                ("c3", "t2", "ERROR", content_hash("ERROR"), "[]", "2026-07-01T00:00:05Z"),
            ],
        )
        connection.execute(
            """
            INSERT INTO recorded_streams
                (id, session_id, stream_text, content_hash, stream_format, captured_at)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            (
                "r1",
                "s1",
                '{"event":"done"}\n',
                content_hash('{"event":"done"}\n'),
                "jsonl-1",
                "2026-07-01T00:01:00Z",
            ),
        )
        connection.execute("INSERT INTO bench_runs (id, score) VALUES ('b1', 1)")
        connection.commit()
        connection.close()

        report = REPORT.generate_report(path)

        self.assertNotIn("bench_runs", report)
        self.assertNotIn("bench_runs", report["database_schema"])
        self.assertEqual(report["overview"]["total_sessions"], 2)
        self.assertEqual(report["overview"]["total_tool_calls"], 3)
        self.assertEqual(report["overview"]["total_output_bytes"], 15)
        self.assertEqual(report["agent_metrics"][0]["tool_call_count"], 2)
        self.assertEqual(report["tool_metrics"][0]["call_count"], 2)
        self.assertEqual(report["compression"]["overview"]["artifact_count"], 3)
        self.assertEqual(report["compression"]["overview"]["unique_source_count"], 2)
        self.assertEqual(report["compression"]["overview"]["recompressed_source_count"], 1)
        self.assertEqual(report["compression"]["overview"]["unique_source_bytes"], 11)
        self.assertEqual(report["compression"]["overview"]["source_bytes_across_artifacts"], 17)
        self.assertEqual(report["compression"]["overview"]["compressed_bytes"], 8)
        self.assertEqual(report["failure_metrics"]["unknown_status_calls"], 1)
        self.assertEqual(report["measurement_coverage"]["coverage"]["cost"]["measured_calls"], 1)
        self.assertEqual(report["integrity"]["tool_calls"]["mismatch_count"], 0)
        self.assertNotIn("database_path", report["source"])
        self.assertNotIn("session_id", report["compression"]["top_sessions"][0])

    def test_empty_database_preserves_absence_as_null(self):
        path, connection = self.make_database()
        connection.commit()
        connection.close()

        report = REPORT.generate_report(path)

        self.assertEqual(report["overview"]["total_tool_calls"], 0)
        self.assertEqual(report["overview"]["open_sessions"], 0)
        self.assertIsNone(report["measurement_coverage"]["totals"]["total_cost"])
        self.assertIsNone(report["failure_metrics"]["error_rate_pct_of_known"])
        self.assertIsNone(report["compression"]["overview"]["reduction_pct"])
        self.assertEqual(report["daily_activity"], [])

    def test_atomic_writer_emits_utf8_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "report.json"
            REPORT.write_json(output, '{"value":"héllo"}\n')
            self.assertEqual(output.read_bytes(), '{"value":"héllo"}\n'.encode())


if __name__ == "__main__":
    unittest.main()
