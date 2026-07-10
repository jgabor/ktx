import hashlib
import importlib.util
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
MODULE_PATH = ROOT / "scripts" / "live_ab.py"
SPEC = importlib.util.spec_from_file_location("live_ab", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"could not load {MODULE_PATH}")
LIVE_AB = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(LIVE_AB)


def sha256(value):
    return hashlib.sha256(value.encode()).hexdigest()


class LiveABHarnessTest(unittest.TestCase):
    def write_executable(self, directory, name, content):
        path = Path(directory) / name
        path.write_text(content, encoding="utf-8")
        path.chmod(0o755)
        return path

    def test_summary_excludes_invalid_attempts(self):
        manifest = {
            "experiment_id": "example",
            "target_valid_per_arm": 1,
            "task": {"minimum_tool_calls": 2},
        }
        attempts = [
            {
                "attempt_id": "baseline-001",
                "arm": "baseline",
                "status": "completed",
                "session_id": "baseline",
                "stream_complete": True,
                "answer_matches_expected": True,
                "tool_call_count": 2,
                "tokens_in": 100,
                "tokens_out": 10,
                "cost": 0.1,
                "score": 1,
            },
            {
                "attempt_id": "treatment-001",
                "arm": "treatment",
                "status": "timeout",
                "session_id": None,
                "stream_complete": False,
                "answer_matches_expected": False,
                "tool_call_count": 0,
            },
            {
                "attempt_id": "treatment-002",
                "arm": "treatment",
                "status": "completed",
                "session_id": "treatment",
                "stream_complete": True,
                "answer_matches_expected": True,
                "tool_call_count": 3,
                "tokens_in": 40,
                "tokens_out": 8,
                "cost": 0.04,
                "score": 1,
            },
        ]

        summary = LIVE_AB.build_summary(manifest, attempts)

        self.assertEqual(summary["arms"]["baseline"]["valid_count"], 1)
        self.assertEqual(summary["arms"]["treatment"]["valid_count"], 1)
        self.assertEqual(summary["baseline_minus_treatment"]["tokens_in"], 60)
        self.assertEqual(summary["exclusions"][0]["attempt_id"], "treatment-001")

    def test_parse_stream_requires_stop_and_exact_answer(self):
        with tempfile.TemporaryDirectory() as directory:
            stream = Path(directory) / "stream.jsonl"
            stream.write_text(
                "\n".join(
                    [
                        json.dumps({"type": "tool_use", "part": {}}),
                        json.dumps(
                            {
                                "type": "step_finish",
                                "part": {
                                    "reason": "tool-calls",
                                    "tokens": {"input": 10, "output": 2},
                                    "cost": 0.01,
                                },
                            }
                        ),
                        json.dumps({"type": "text", "part": {"text": "done"}}),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )

            metrics = LIVE_AB.parse_stream(stream, "done")

        self.assertFalse(metrics["stream_complete"])
        self.assertTrue(metrics["answer_matches_expected"])
        self.assertEqual(metrics["tool_call_count"], 1)
        self.assertEqual(metrics["tokens_in"], 10)

    def test_parse_stream_preserves_missing_usage_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            stream = Path(directory) / "stream.jsonl"
            stream.write_text(
                "\n".join(
                    [
                        json.dumps({"type": "tool_use", "part": {}}),
                        json.dumps({"type": "step_finish", "part": {"reason": "stop"}}),
                        json.dumps({"type": "text", "part": {"text": "done"}}),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )

            metrics = LIVE_AB.parse_stream(stream, "done")

        self.assertIsNone(metrics["tokens_in"])
        self.assertIsNone(metrics["tokens_out"])
        self.assertIsNone(metrics["cost"])

    def test_recorded_stream_links_retains_all_matches(self):
        with tempfile.TemporaryDirectory() as directory:
            database = Path(directory) / "ktx.db"
            with sqlite3.connect(database) as connection:
                connection.execute(
                    "CREATE TABLE recorded_streams (id TEXT, bench_run_id TEXT, session_id TEXT)"
                )
                connection.executemany(
                    "INSERT INTO recorded_streams VALUES (?, ?, ?)",
                    [("stream-b", "run-b", "session"), ("stream-a", "run-a", "session")],
                )

            links = LIVE_AB.recorded_stream_links(database, "session")

        self.assertEqual(
            links,
            [
                {"id": "stream-a", "bench_run_id": "run-a"},
                {"id": "stream-b", "bench_run_id": "run-b"},
            ],
        )

    def test_attempt_statuses_preserve_failures(self):
        complete = {
            "stream_complete": True,
            "answer_matches_expected": True,
            "tool_call_count": 1,
        }
        incomplete = {**complete, "stream_complete": False}

        self.assertEqual(LIVE_AB.attempt_status(124, [], complete), "timeout")
        self.assertEqual(LIVE_AB.attempt_status(1, [], complete), "failed")
        self.assertEqual(LIVE_AB.attempt_status(0, [], complete), "unlinked")
        self.assertEqual(LIVE_AB.attempt_status(0, ["one", "two"], complete), "unlinked")
        self.assertEqual(LIVE_AB.attempt_status(0, ["one"], incomplete), "invalid")
        self.assertEqual(LIVE_AB.attempt_status(0, ["one"], complete), "completed")

    def test_counted_attempt_requires_answer_and_tool_quality_gates(self):
        complete = {
            "status": "completed",
            "session_id": "session",
            "stream_complete": True,
            "answer_matches_expected": True,
            "tool_call_count": 2,
        }

        self.assertTrue(LIVE_AB.attempt_is_counted(complete, 2))
        self.assertFalse(LIVE_AB.attempt_is_counted({**complete, "answer_matches_expected": False}, 2))
        self.assertFalse(LIVE_AB.attempt_is_counted({**complete, "tool_call_count": 1}, 2))

    def test_harness_records_session_linked_alternating_attempts(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            auth = workspace / "auth.json"
            auth.write_text("{}", encoding="utf-8")
            data_dir = workspace / "data"
            docker = self.write_executable(
                workspace,
                "docker",
                """#!/usr/bin/env python3
import json
import os
import pathlib
import sys

args = sys.argv[1:]
if args[:2] == ["image", "inspect"]:
    print("sha256:fake-image")
    raise SystemExit(0)
if args and args[0] == "run":
    if "--identity" in args:
        print("ktx\\tfake-ktx")
        print("opencode\\tfake-opencode")
        raise SystemExit(0)
    if os.environ.get("FAKE_DOCKER_EXIT_CODE"):
        raise SystemExit(int(os.environ["FAKE_DOCKER_EXIT_CODE"]))
    fail_once = os.environ.get("FAKE_DOCKER_FAIL_ONCE_FILE")
    if fail_once and not pathlib.Path(fail_once).exists():
        pathlib.Path(fail_once).touch()
        raise SystemExit(1)
    mounts = [args[i + 1] for i, value in enumerate(args[:-1]) if value == "-v"]
    database = next(value.split(":", 1)[0] for value in mounts if ":/home/node/.local/share/ktx/ktx.db:" in value)
    config = next(value.split(":", 1)[0] for value in mounts if ":/home/node/.config/ktx/ktx.toml:" in value)
    arm = pathlib.Path(config).name.rsplit(".", 1)[-1]
    workspace = next(value.split(":", 1)[0] for value in mounts if ":/work:rw" in value)
    pathlib.Path(database + ".session").write_text(arm)
    pathlib.Path(workspace, "agent-created").write_text(arm)
    for event in (
        {"type": "tool_use", "part": {}},
        {"type": "step_finish", "part": {"reason": "stop", "tokens": {"input": 100 if arm == "baseline" else 40, "output": 5}, "cost": 0.1 if arm == "baseline" else 0.04}},
        {"type": "text", "part": {"text": "done"}},
    ):
        print(json.dumps(event))
    raise SystemExit(0)
raise SystemExit(2)
""",
            )
            ktx = self.write_executable(
                workspace,
                "ktx",
                """#!/usr/bin/env python3
import json
import os
import pathlib
import sys

if sys.argv[1:] != ["list", "--format", "json"]:
    raise SystemExit(2)
database = pathlib.Path(os.environ["KTX_DB"])
marker = pathlib.Path(str(database) + ".session")
if marker.exists():
    print(json.dumps([{"id": marker.read_text(), "agent": "fake", "tool_call_count": 1}]))
else:
    print("[]")
""",
            )
            environment = {
                **os.environ,
                "DOCKER_BIN": str(docker),
                "KTX_BIN": str(ktx),
            }
            command = [
                sys.executable,
                str(ROOT / "scripts" / "live_ab.py"),
                "--model",
                "neuralwatt/fake",
                "--prompt",
                "do work",
                "--task-id",
                "fake",
                "--expected-output",
                "done",
                "--min-tool-calls",
                "1",
                "--target-valid",
                "2",
                "--max-attempts",
                "4",
                "--data-dir",
                str(data_dir),
                "--auth",
                str(auth),
                "--repo-root",
                str(ROOT),
                "--experiment-id",
                "run",
            ]

            result = subprocess.run(command, capture_output=True, text=True, env=environment)

            self.assertEqual(result.returncode, 0, result.stderr)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["arms"]["baseline"]["valid_count"], 2)
            self.assertEqual(summary["arms"]["treatment"]["valid_count"], 2)
            attempt_records = [
                json.loads(line)
                for line in (data_dir / "run" / "attempts.jsonl").read_text(encoding="utf-8").splitlines()
            ]
            self.assertEqual(
                [record["arm"] for record in attempt_records],
                ["baseline", "treatment", "treatment", "baseline"],
            )
            self.assertEqual(
                [record["session_id"] for record in attempt_records],
                ["baseline", "treatment", "treatment", "baseline"],
            )
            manifest = json.loads((data_dir / "run" / "manifest.json").read_text(encoding="utf-8"))
            self.assertTrue((data_dir / "run" / manifest["repository"]["snapshot"]["path"]).is_dir())
            self.assertTrue((data_dir / "run" / "configs" / "ktx.toml.baseline").is_file())
            self.assertNotIn("agent-created", [path.name for path in ROOT.iterdir()])
            for record in attempt_records:
                self.assertTrue((data_dir / "run" / record["artifacts"]["workspace"] / "agent-created").is_file())

            summary_before = (data_dir / "run" / "summary.json").read_bytes()
            regenerate = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "scripts" / "live_ab.py"),
                    "--summarize",
                    str(data_dir / "run"),
                ],
                capture_output=True,
                text=True,
                env=environment,
            )
            self.assertEqual(regenerate.returncode, 0, regenerate.stderr)
            self.assertEqual((data_dir / "run" / "summary.json").read_bytes(), summary_before)

            rerun = subprocess.run(command, capture_output=True, text=True, env=environment)
            self.assertEqual(rerun.returncode, 2)
            self.assertIn("experiment directory already exists", rerun.stderr)
            self.assertEqual(
                len((data_dir / "run" / "attempts.jsonl").read_text(encoding="utf-8").splitlines()),
                4,
            )

            exhausted_command = command.copy()
            for option, value in (
                ("--target-valid", "1"),
                ("--max-attempts", "2"),
                ("--data-dir", str(workspace / "exhausted-data")),
                ("--experiment-id", "exhausted"),
            ):
                exhausted_command[exhausted_command.index(option) + 1] = value
            exhausted = subprocess.run(
                exhausted_command,
                capture_output=True,
                text=True,
                env={**environment, "FAKE_DOCKER_EXIT_CODE": "1"},
            )
            self.assertEqual(exhausted.returncode, 1)
            exhausted_summary = json.loads(exhausted.stdout)
            self.assertEqual(exhausted_summary["arms"]["baseline"]["status_counts"]["failed"], 1)
            self.assertEqual(exhausted_summary["arms"]["treatment"]["status_counts"]["failed"], 1)

            retry_command = command.copy()
            for option, value in (
                ("--target-valid", "1"),
                ("--max-attempts", "3"),
                ("--data-dir", str(workspace / "retry-data")),
                ("--experiment-id", "retry"),
            ):
                retry_command[retry_command.index(option) + 1] = value
            retry = subprocess.run(
                retry_command,
                capture_output=True,
                text=True,
                env={
                    **environment,
                    "FAKE_DOCKER_FAIL_ONCE_FILE": str(workspace / "fail-once"),
                },
            )
            self.assertEqual(retry.returncode, 0, retry.stderr)
            retry_summary = json.loads(retry.stdout)
            self.assertEqual(retry_summary["arms"]["baseline"]["status_counts"]["failed"], 1)
            self.assertEqual(retry_summary["arms"]["baseline"]["valid_count"], 1)
            self.assertEqual(retry_summary["arms"]["treatment"]["valid_count"], 1)

    def test_runner_rejects_preflight_stage_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            config_root = workspace / "config"
            config_path = config_root / "ktx" / "ktx.toml"
            config_path.parent.mkdir(parents=True)
            config_path.write_text("[pipeline.default]\nstages = [\"head:2\"]\n", encoding="utf-8")
            auth = workspace / "auth.json"
            auth.write_text("{}", encoding="utf-8")
            invoked = workspace / "opencode-invoked"
            self.write_executable(
                workspace,
                "ktx",
                """#!/usr/bin/env sh
printf '%s\\n' '{"profiles":{"default":{"stages":[]}}}'
""",
            )
            self.write_executable(
                workspace,
                "opencode",
                f"""#!/usr/bin/env sh
touch "{invoked}"
""",
            )
            environment = {
                **os.environ,
                "PATH": f"{workspace}:{os.environ['PATH']}",
                "XDG_CONFIG_HOME": str(config_root),
                "KTX_EXPECTED_STAGES_JSON": '["head:2"]',
                "KTX_DB": str(workspace / "ktx.db"),
                "OPENCODE_AUTH_PATH": str(auth),
            }

            result = subprocess.run(
                [
                    "sh",
                    str(ROOT / "tools" / "bench" / "ktx-runner"),
                    "-m",
                    "neuralwatt/fake",
                    "do work",
                ],
                capture_output=True,
                text=True,
                env=environment,
            )

            self.assertEqual(result.returncode, 2)
            self.assertIn("configured stages mismatch", result.stderr)
            self.assertFalse(invoked.exists())

    def test_runner_starts_agent_after_matching_preflight(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            config_root = workspace / "config"
            config_path = config_root / "ktx" / "ktx.toml"
            config_path.parent.mkdir(parents=True)
            config_path.write_text("[pipeline.default]\nstages = []\n", encoding="utf-8")
            auth = workspace / "auth.json"
            auth.write_text("{}", encoding="utf-8")
            invoked = workspace / "opencode-invoked"
            self.write_executable(
                workspace,
                "ktx",
                """#!/usr/bin/env sh
printf '%s\\n' '{"profiles":{"default":{"stages":[]}}}'
""",
            )
            self.write_executable(
                workspace,
                "opencode",
                f"""#!/usr/bin/env sh
touch "{invoked}"
printf '%s\\n' '{{"type":"text","part":{{"text":"done"}}}}'
""",
            )
            environment = {
                **os.environ,
                "PATH": f"{workspace}:{os.environ['PATH']}",
                "XDG_CONFIG_HOME": str(config_root),
                "KTX_EXPECTED_STAGES_JSON": "[]",
                "KTX_DB": str(workspace / "ktx.db"),
                "OPENCODE_AUTH_PATH": str(auth),
            }

            result = subprocess.run(
                [
                    "sh",
                    str(ROOT / "tools" / "bench" / "ktx-runner"),
                    "-m",
                    "neuralwatt/fake",
                    "do work",
                ],
                capture_output=True,
                text=True,
                env=environment,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue(invoked.exists())
            self.assertIn('"type":"text"', result.stdout)


if __name__ == "__main__":
    unittest.main()
