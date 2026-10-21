import os
from pathlib import Path
import sys
import json
import subprocess
import tempfile
import unittest
from unittest import mock
import copy
import hashlib
import re

from tools import schema_readiness_evidence


class MeasureTest(unittest.TestCase):
    def test_current_guidance_matches_passing_report(self):
        repository = Path.cwd()
        guidance = (repository / "docs/reference/schema-readiness.md").read_text()
        report_path = repository / "docs/evidence/schema-readiness/schema-readiness-release.json"
        report = json.loads(report_path.read_text())

        match = re.search(
            r"current passing authority measured `build/ktx`, SHA-256\s+"
            r"`([0-9a-f]{64})`.*?report role is\s+`([^`]+)`",
            guidance,
            re.DOTALL,
        )
        self.assertIsNotNone(match, "current guidance lacks a full measured binary identity")
        self.assertEqual(match.groups(), (report["binary"]["sha256"], report["report_role"]))
        schema_readiness_evidence.validate_release_report(report)

        historical = repository / report["historical_failure_evidence"]["path"]
        self.assertEqual(
            hashlib.sha256(historical.read_bytes()).hexdigest(),
            report["historical_failure_evidence"]["sha256"],
        )
        self.assertRegex(report["raw_measurements_sha256"], r"^[0-9a-f]{64}$")

    def test_current_guidance_identity_rejects_stale_malformed_and_missing_values(self):
        report = {"binary": {"sha256": "a" * 64}, "report_role": "current_passing_authority"}

        def verify(guidance):
            match = re.search(
                r"current passing authority measured `build/ktx`, SHA-256\s+"
                r"`([0-9a-f]{64})`.*?report role is\s+`([^`]+)`",
                guidance,
                re.DOTALL,
            )
            if match is None or match.groups() != (report["binary"]["sha256"], report["report_role"]):
                raise ValueError("guidance and passing report binary identity differ")

        for guidance in (
            f"current passing authority measured `build/ktx`, SHA-256\n`{'b' * 64}`; report role is `current_passing_authority`",
            "current passing authority measured `build/ktx`, SHA-256 `bad`; report role is `current_passing_authority`",
            "current passing authority has no identity",
        ):
            with self.subTest(guidance=guidance), self.assertRaises(ValueError):
                verify(guidance)

    def test_approved_combined_limits_preserve_readiness_and_journal(self):
        self.assertEqual(
            schema_readiness_evidence.APPROVED_LIMITS,
            {
                "readiness_latency_ns": 35_322_974,
                "exclusive_lock_hold_ns": 560_264_885,
                "journal_growth_bytes": 50_000,
                "peak_rss_bytes": 59_130_880,
                "total_completion_ns": 893_936_754,
            },
        )
        self.assertIn("combined-workload revision", schema_readiness_evidence.LIMIT_BASIS)
        self.assertIn("aggregate concurrent KTX RSS", schema_readiness_evidence.LIMIT_BASIS)

    def test_historical_failed_report_remains_exact(self):
        record = schema_readiness_evidence.historical_failure_evidence(Path.cwd())
        self.assertEqual(record["report_role"], "historical_failure_evidence")
        self.assertEqual(
            record["sha256"], schema_readiness_evidence.HISTORICAL_FAILURE_SHA256
        )

    def test_large_child_stderr_does_not_block_completion(self):
        # Release measurements pin CPU 2. This test only checks stderr
        # draining, so it pins a CPU that exists on any host.
        available = str(min(os.sched_getaffinity(0)))
        with tempfile.TemporaryDirectory() as root, mock.patch.object(
            schema_readiness_evidence, "CPU", available
        ):
            result = schema_readiness_evidence.measure(
                [
                    sys.executable,
                    "-c",
                    "import os; os.write(2, b'x' * 1048576); print('done')",
                ],
                os.environ.copy(),
                Path(root) / "unused.db",
                "fcntl",
            )

        self.assertEqual(result["stdout"], "done")

    def test_consumed_fixture_must_match_recomputed_manifest_record(self):
        actual = {
            "bytes": 3205681152,
            "sha256": "a" * 64,
            "rows": 3200,
            "payload_bytes": 32000,
            "source_payload_bytes": 3200000000,
            "physical_schema_sha256": "b" * 64,
        }
        schema_readiness_evidence.compare_consumed_fixture(actual, copy.deepcopy(actual))
        prepared = copy.deepcopy(actual)
        prepared["sha256"] = "c" * 64
        with self.assertRaisesRegex(ValueError, "sha256"):
            schema_readiness_evidence.compare_consumed_fixture(actual, prepared)

    def test_release_publication_identifies_correctness_failure_without_replacing_authority(self):
        report = self.release_report()
        item = next(item for item in report["criteria"] if item["criterion"] == "interruption_restart")
        item["status"] = "FAIL"
        report["release_gate"] = "FAIL"
        report["report_role"] = "failed_release_attempt"
        with tempfile.TemporaryDirectory() as root:
            output = Path(root) / "report.json"
            authority = b'{"release_gate":"PASS","authority":"unchanged"}\n'
            output.write_bytes(authority)
            result = self.publish_in_child(report, output)
            failed = json.loads(Path(str(output) + ".attempt.failed.json").read_text())
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("interruption_restart", result.stderr)
            self.assertEqual(output.read_bytes(), authority)
            self.assertEqual(
                [item["criterion"] for item in failed["criteria"] if item["status"] == "FAIL"],
                ["interruption_restart"],
            )
            self.assertFalse(Path(str(output) + ".attempt.failed.json.tmp").exists())

    def test_release_publication_identifies_budget_failure_without_replacing_authority(self):
        report = self.release_report()
        item = next(item for item in report["criteria"] if item["criterion"] == "peak_rss_bytes")
        item["status"] = "FAIL"
        item["evidence"] = {
            "observed": schema_readiness_evidence.APPROVED_LIMITS["peak_rss_bytes"] + 1,
            "limit": schema_readiness_evidence.APPROVED_LIMITS["peak_rss_bytes"],
            "passed": False,
        }
        report["release_gate"] = "FAIL"
        report["report_role"] = "failed_release_attempt"
        with tempfile.TemporaryDirectory() as root:
            output = Path(root) / "report.json"
            authority = b"passing authority\n"
            output.write_bytes(authority)
            result = self.publish_in_child(report, output)
            failed = json.loads(Path(str(output) + ".attempt.failed.json").read_text())
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("peak_rss_bytes", result.stderr)
            self.assertEqual(output.read_bytes(), authority)
            failed_item = next(
                item for item in failed["criteria"] if item["criterion"] == "peak_rss_bytes"
            )
            self.assertEqual(failed_item["evidence"]["observed"], item["evidence"]["observed"])
            self.assertEqual(failed["report_role"], "failed_release_attempt")

    def test_passing_publication_retains_raw_combined_samples(self):
        report = self.release_report()
        with tempfile.TemporaryDirectory() as root:
            output = Path(root) / "report.json"
            schema_readiness_evidence.publish_release_report(report, output)
            retained = json.loads(output.read_text())
        samples = retained["raw_samples"]["schema15_to_16_production"]
        self.assertEqual(len(samples), schema_readiness_evidence.SAMPLES + 1)
        self.assertTrue(all("exclusive_lock_intervals_ns" in sample for sample in samples))
        self.assertTrue(all(sample["combined_workload"]["attributed"] for sample in samples))

    def test_rehearsal_exception_is_published_as_exact_criterion(self):
        report = self.release_report()
        item = next(
            item for item in report["criteria"] if item["criterion"] == "concurrent_generated_traffic"
        )
        item["status"] = "FAIL"
        item["evidence"] = schema_readiness_evidence.stage_failure(
            subprocess.TimeoutExpired("generated adapter", 30)
        )
        report["release_gate"] = "FAIL"
        report["report_role"] = "failed_release_attempt"
        with tempfile.TemporaryDirectory() as root:
            output = Path(root) / "report.json"
            result = self.publish_in_child(report, output)
            failed = json.loads(Path(str(output) + ".attempt.failed.json").read_text())
        self.assertNotEqual(result.returncode, 0)
        failed_items = [item for item in failed["criteria"] if item["status"] == "FAIL"]
        self.assertEqual(failed_items[0]["criterion"], "concurrent_generated_traffic")
        self.assertEqual(failed_items[0]["evidence"]["error_type"], "TimeoutExpired")

    def test_combined_sample_requires_query_hook_telemetry_and_exact_evidence(self):
        report = self.release_report()
        schema_readiness_evidence.validate_release_report(report)
        report["raw_samples"]["schema15_to_16_production"][1]["combined_workload"][
            "attributed"
        ] = False
        with self.assertRaisesRegex(ValueError, "combined-workload attribution"):
            schema_readiness_evidence.validate_release_report(report)

    def test_combined_memory_criterion_rejects_one_process_value(self):
        report = self.release_report()
        sample = report["raw_samples"]["schema15_to_16_production"][1]
        sample["peak_rss_bytes"] = sample["combined_workload"][
            "maximum_individual_peak_rss_bytes"
        ]
        with self.assertRaisesRegex(ValueError, "aggregate concurrent KTX RSS"):
            schema_readiness_evidence.validate_release_report(report)

    def test_release_identity_comes_from_artifact_bytes_and_readiness_samples(self):
        with tempfile.TemporaryDirectory() as root:
            artifact = Path(root) / "ktx"
            artifact.write_bytes(b"built artifact")
            self.assertEqual(
                schema_readiness_evidence.digest(artifact, "test artifact"),
                hashlib.sha256(b"built artifact").hexdigest(),
            )
        workloads = {"migration": [{"final_schema": 16}]}
        self.assertEqual(
            schema_readiness_evidence.supported_schema_evidence(workloads),
            {"maximum": 16, "evidence": "measured workload final_schema"},
        )
        with self.assertRaisesRegex(ValueError, "supported database schema"):
            schema_readiness_evidence.supported_schema_evidence(
                {"migration": [{"final_schema": 15}, {"final_schema": 16}]}
            )

    def test_automatic_interruption_result_rejects_later_migrate(self):
        result = {
            "durable_rows_before_restart": 128,
            "schema_before_restart": 15,
            "concurrent_query_exit": 0,
            "concurrent_query_raw_lock_error": False,
            "post_recovery_migrate_invoked": False,
        }
        self.assertTrue(schema_readiness_evidence.automatic_interruption_passed(result))
        result["post_recovery_migrate_invoked"] = True
        self.assertFalse(schema_readiness_evidence.automatic_interruption_passed(result))

    @staticmethod
    def publish_in_child(report, output):
        return subprocess.run(
            [
                sys.executable,
                "-c",
                "import json,sys; from pathlib import Path; "
                "from tools.schema_readiness_evidence import publish_release_report; "
                "publish_release_report(json.load(sys.stdin), Path(sys.argv[1]))",
                str(output),
            ],
            input=json.dumps(report), text=True, capture_output=True,
        )

    @staticmethod
    def release_report():
        names = [
            "prior_schema_identity", "representative_scale", "concurrent_generated_traffic",
            "interruption_restart", *schema_readiness_evidence.APPROVED_LIMITS,
        ]
        sample = {
            "kind": "measured",
            "readiness_latency_ns": 1,
            "exclusive_lock_hold_ns": 2,
            "journal_growth_bytes": 3,
            "peak_rss_bytes": 7,
            "total_completion_ns": 5,
            "exclusive_lock_intervals_ns": [2],
            "per_process_peak_rss_bytes": [
                {"pid": 1, "peak_rss_bytes": 4},
                {"pid": 2, "peak_rss_bytes": 3},
            ],
            "combined_workload": {
                "attributed": True,
                "participants": [
                    {"role": "query"}, {"role": "hook"}, {"role": "telemetry"},
                ],
                "rss_criterion": "aggregate concurrent KTX RSS",
                "aggregate_peak_rss_bytes": 7,
                "maximum_individual_peak_rss_bytes": 4,
                "per_process_peak_rss_bytes": [
                    {"pid": 1, "peak_rss_bytes": 4},
                    {"pid": 2, "peak_rss_bytes": 3},
                ],
            },
            "combined_evidence": {"passed": True},
        }
        samples = [dict(sample, kind="warmup")] + [copy.deepcopy(sample) for _ in range(schema_readiness_evidence.SAMPLES)]
        digest = "a" * 64
        criteria = []
        for name in names:
            evidence = {}
            if name in schema_readiness_evidence.APPROVED_LIMITS:
                evidence = {
                    "observed": 0,
                    "limit": schema_readiness_evidence.APPROVED_LIMITS[name],
                    "passed": True,
                }
            criteria.append(schema_readiness_evidence.criterion(name, True, evidence))
        return {
            "schema_version": 4,
            "report_role": "current_passing_authority",
            "release_gate": "PASS",
            "binary": {"sha256": digest},
            "supported_database_schema": {"maximum": 16, "evidence": "measured workload final_schema"},
            "repository": {"commit": "b" * 40, "worktree_dirty": True},
            "historical_failure_evidence": {
                "report_role": "historical_failure_evidence",
                "path": str(schema_readiness_evidence.HISTORICAL_FAILURE_REPORT),
                "sha256": schema_readiness_evidence.HISTORICAL_FAILURE_SHA256,
            },
            "policy": {},
            "host": {},
            "criteria": criteria,
            "raw_samples": {"schema15_to_16_production": samples},
        }


if __name__ == "__main__":
    unittest.main()
