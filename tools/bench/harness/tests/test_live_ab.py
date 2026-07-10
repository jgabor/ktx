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


ROOT = Path(__file__).resolve().parents[4]
MODULE_PATH = Path(__file__).resolve().parents[1] / "live_ab.py"
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
            "target_valid_per_pipeline": 1,
            "task": {"minimum_tool_calls": 2},
        }
        attempts = [
            {
                "attempt_id": "baseline-001",
                "side": "baseline",
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
                "attempt_id": "candidate-001",
                "side": "candidate",
                "status": "timeout",
                "session_id": None,
                "stream_complete": False,
                "answer_matches_expected": False,
                "tool_call_count": 0,
            },
            {
                "attempt_id": "candidate-002",
                "side": "candidate",
                "status": "completed",
                "session_id": "candidate",
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

        self.assertEqual(summary["pipelines"]["baseline"]["valid_count"], 1)
        self.assertEqual(summary["pipelines"]["candidate"]["valid_count"], 1)
        self.assertEqual(summary["pipelines"]["baseline"]["evaluated_count"], 1)
        self.assertEqual(summary["baseline_minus_candidate"]["tokens_in"], 60)
        self.assertEqual(summary["exclusions"][0]["attempt_id"], "candidate-001")
        self.assertEqual(summary["methodology_version"], "v4-eval-matrix")

    def test_summary_keeps_partial_fact_scores_in_evaluated(self):
        manifest = {
            "experiment_id": "partial",
            "target_valid_per_pipeline": 1,
            "task": {"minimum_tool_calls": 1},
        }
        attempts = [
            {
                "attempt_id": "baseline-001",
                "side": "baseline",
                "status": "completed",
                "session_id": "b1",
                "stream_complete": True,
                "answer_matches_expected": True,
                "full_pass": True,
                "tool_call_count": 2,
                "tokens_in": 100,
                "tokens_out": 10,
                "cost": 0.2,
                "score": 1.0,
            },
            {
                "attempt_id": "candidate-001",
                "side": "candidate",
                "status": "completed",
                "session_id": "t1",
                "stream_complete": True,
                "answer_matches_expected": False,
                "full_pass": False,
                "tool_call_count": 3,
                "tokens_in": 40,
                "tokens_out": 8,
                "cost": 0.05,
                "score": 0.6,
            },
        ]

        summary = LIVE_AB.build_summary(manifest, attempts)

        self.assertEqual(summary["pipelines"]["candidate"]["evaluated_count"], 1)
        self.assertEqual(summary["pipelines"]["candidate"]["valid_count"], 0)
        self.assertAlmostEqual(summary["pipelines"]["candidate"]["mean_fact_score"], 0.6)
        self.assertAlmostEqual(summary["pipelines"]["candidate"]["full_pass_rate"], 0.0)
        self.assertGreater(
            summary["pipelines"]["candidate"]["expected_cost_per_correct"],
            1000,
        )
        self.assertAlmostEqual(summary["tool_call_inflation"], 1.5)

    def test_score_answer_multi_fact_fractional(self):
        facts = [
            "FACT_A=one",
            "FACT_B=two",
            "FACT_C=three",
        ]
        scored = LIVE_AB.score_answer(
            "FACT_A=one\nnoise\nFACT_C=three",
            expected_facts=facts,
        )
        self.assertAlmostEqual(scored["score"], 2 / 3)
        self.assertFalse(scored["full_pass"])
        self.assertEqual(scored["facts_matched"], ["FACT_A=one", "FACT_C=three"])
        self.assertEqual(scored["facts_missed"], ["FACT_B=two"])

    def test_load_task_file_crossfile(self):
        task = LIVE_AB.load_task_file(ROOT / "tools/bench/tasks/crossfile-nav-v1.toml")
        self.assertEqual(task["id"], "crossfile-nav-v1")
        self.assertEqual(task["minimum_tool_calls"], 5)
        self.assertEqual(len(task["expected_facts"]), 5)
        self.assertEqual(task["workspace_mode"], "corpus")
        self.assertEqual(len(task["corpus_files"]), 5)
        self.assertIn("FACT_ALPHA_MID=", task["expected_facts"][0])
        self.assertIn("tools/bench/corpus/src_alpha.txt", task["prompt"])

    def test_prepare_corpus_workspace_excludes_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            corpus = root / "tools" / "bench" / "corpus"
            corpus.mkdir(parents=True)
            (corpus / "src_alpha.txt").write_text(
                "FACT_ALPHA_MID=alpha-mid-4b18e0d7\n", encoding="utf-8"
            )
            (corpus / "MANIFEST.json").write_text('{"leak": true}\n', encoding="utf-8")
            workspace = root / "workspace"
            meta = LIVE_AB.prepare_attempt_workspace(
                root,
                workspace,
                mode="corpus",
                corpus_files=["src_alpha.txt"],
            )
            self.assertEqual(meta["mode"], "corpus")
            self.assertTrue((workspace / "tools/bench/corpus/src_alpha.txt").is_file())
            self.assertFalse((workspace / "tools/bench/corpus/MANIFEST.json").exists())
            self.assertEqual(
                sorted(p.name for p in (workspace / "tools/bench/corpus").iterdir()),
                ["src_alpha.txt"],
            )

    def test_prepare_repo_workspace_strips_answer_keys(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repo"
            corpus = root / "tools" / "bench" / "corpus"
            tasks = root / "tools" / "bench" / "tasks"
            corpus.mkdir(parents=True)
            tasks.mkdir(parents=True)
            (root / "README.md").write_text("ok\n", encoding="utf-8")
            (corpus / "src_alpha.txt").write_text("body\n", encoding="utf-8")
            (corpus / "MANIFEST.json").write_text("{}\n", encoding="utf-8")
            (tasks / "crossfile-nav-v1.toml").write_text('id="x"\n', encoding="utf-8")
            (root / "tools" / "bench" / "generate_corpus.py").write_text(
                "print(1)\n", encoding="utf-8"
            )
            docs = root / "docs"
            docs.mkdir()
            (docs / "pipelines.md").write_text("facts\n", encoding="utf-8")
            workspace = Path(directory) / "workspace"
            meta = LIVE_AB.prepare_attempt_workspace(
                root,
                workspace,
                mode="repo",
                corpus_files=[],
            )
            self.assertEqual(meta["mode"], "repo")
            self.assertTrue((workspace / "README.md").is_file())
            self.assertTrue((workspace / "tools/bench/corpus/src_alpha.txt").is_file())
            self.assertFalse((workspace / "tools/bench/corpus/MANIFEST.json").exists())
            self.assertFalse((workspace / "tools/bench/tasks").exists())
            self.assertFalse((workspace / "tools/bench/generate_corpus.py").exists())
            self.assertFalse((workspace / "docs/pipelines.md").exists())

    def test_parse_stream_multi_fact(self):
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
                                    "reason": "stop",
                                    "tokens": {"input": 12, "output": 3},
                                    "cost": 0.02,
                                },
                            }
                        ),
                        json.dumps(
                            {
                                "type": "text",
                                "part": {
                                    "text": "FACT_A=one\nFACT_B=two",
                                },
                            }
                        ),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            metrics = LIVE_AB.parse_stream(
                stream,
                expected_facts=["FACT_A=one", "FACT_B=two", "FACT_C=three"],
            )
        self.assertTrue(metrics["stream_complete"])
        self.assertAlmostEqual(metrics["score"], 2 / 3)
        self.assertFalse(metrics["full_pass"])

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

    def test_full_pass_requires_answer_and_tool_quality_gates(self):
        complete = {
            "status": "completed",
            "session_id": "session",
            "stream_complete": True,
            "answer_matches_expected": True,
            "tool_call_count": 2,
        }

        self.assertTrue(LIVE_AB.attempt_is_full_pass(complete, 2))
        self.assertFalse(
            LIVE_AB.attempt_is_full_pass({**complete, "answer_matches_expected": False}, 2)
        )
        self.assertFalse(LIVE_AB.attempt_is_full_pass({**complete, "tool_call_count": 1}, 2))

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
    side = pathlib.Path(config).name.rsplit(".", 1)[-1]
    workspace = next(value.split(":", 1)[0] for value in mounts if ":/work:rw" in value)
    pathlib.Path(database + ".session").write_text(side)
    pathlib.Path(workspace, "agent-created").write_text(side)
    for event in (
        {"type": "tool_use", "part": {}},
        {"type": "step_finish", "part": {"reason": "stop", "tokens": {"input": 100 if side == "baseline" else 40, "output": 5}, "cost": 0.1 if side == "baseline" else 0.04}},
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
                str(MODULE_PATH),
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
            self.assertEqual(summary["pipelines"]["baseline"]["valid_count"], 2)
            self.assertEqual(summary["pipelines"]["candidate"]["valid_count"], 2)
            attempt_records = [
                json.loads(line)
                for line in (data_dir / "run" / "attempts.jsonl")
                .read_text(encoding="utf-8")
                .splitlines()
            ]
            self.assertEqual(
                [record["side"] for record in attempt_records],
                ["baseline", "candidate", "candidate", "baseline"],
            )
            self.assertEqual(
                [record["session_id"] for record in attempt_records],
                ["baseline", "candidate", "candidate", "baseline"],
            )
            manifest = json.loads((data_dir / "run" / "manifest.json").read_text(encoding="utf-8"))
            self.assertTrue(
                (data_dir / "run" / manifest["repository"]["snapshot"]["path"]).is_dir()
            )
            self.assertTrue((data_dir / "run" / "configs" / "ktx.toml.baseline").is_file())
            self.assertNotIn("agent-created", [path.name for path in ROOT.iterdir()])
            for record in attempt_records:
                self.assertTrue(
                    (
                        data_dir / "run" / record["artifacts"]["workspace"] / "agent-created"
                    ).is_file()
                )

            summary_before = (data_dir / "run" / "summary.json").read_bytes()
            regenerate = subprocess.run(
                [
                    sys.executable,
                    str(MODULE_PATH),
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
            self.assertEqual(
                exhausted_summary["pipelines"]["baseline"]["status_counts"]["failed"], 1
            )
            self.assertEqual(
                exhausted_summary["pipelines"]["candidate"]["status_counts"]["failed"], 1
            )

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
            self.assertEqual(retry_summary["pipelines"]["baseline"]["status_counts"]["failed"], 1)
            self.assertEqual(retry_summary["pipelines"]["baseline"]["valid_count"], 1)
            self.assertEqual(retry_summary["pipelines"]["candidate"]["valid_count"], 1)

    def test_runner_rejects_preflight_stage_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            config_root = workspace / "config"
            config_path = config_root / "ktx" / "ktx.toml"
            config_path.parent.mkdir(parents=True)
            config_path.write_text('[pipeline.default]\nstages = ["head:2"]\n', encoding="utf-8")
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
