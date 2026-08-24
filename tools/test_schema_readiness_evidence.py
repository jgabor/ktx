import os
from pathlib import Path
import sys
import json
import subprocess
import tempfile
import unittest

from tools import schema_readiness_evidence


class MeasureTest(unittest.TestCase):
    def test_approved_limits_preserve_unchanged_budgets(self):
        self.assertEqual(
            schema_readiness_evidence.APPROVED_LIMITS,
            {
                "readiness_latency_ns": 35_322_974,
                "exclusive_lock_hold_ns": 101_037_622,
                "journal_growth_bytes": 50_000,
                "peak_rss_bytes": 26_557_440,
                "total_completion_ns": 400_000_000,
            },
        )
        self.assertIn("user-approved override", schema_readiness_evidence.LIMIT_BASIS)

    def test_large_child_stderr_does_not_block_completion(self):
        with tempfile.TemporaryDirectory() as root:
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

    def test_release_report_identifies_correctness_failure(self):
        report = self.release_report()
        item = next(item for item in report["criteria"] if item["criterion"] == "interruption_restart")
        item["status"] = "FAIL"
        report["release_gate"] = "FAIL"
        result = self.validate_in_child(report)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("interruption_restart", result.stderr)

    def test_release_report_identifies_budget_failure(self):
        report = self.release_report()
        item = next(item for item in report["criteria"] if item["criterion"] == "peak_rss_bytes")
        item["status"] = "FAIL"
        report["release_gate"] = "FAIL"
        result = self.validate_in_child(report)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("peak_rss_bytes", result.stderr)

    @staticmethod
    def validate_in_child(report):
        return subprocess.run(
            [sys.executable, "-c", "import json,sys; from tools.schema_readiness_evidence import validate_release_report; validate_release_report(json.load(sys.stdin))"],
            input=json.dumps(report), text=True, capture_output=True,
        )

    @staticmethod
    def release_report():
        names = [
            "prior_schema_identity", "representative_scale", "concurrent_generated_traffic",
            "interruption_restart", *schema_readiness_evidence.APPROVED_LIMITS,
        ]
        return {
            "release_gate": "PASS",
            "criteria": [schema_readiness_evidence.criterion(name, True, {}) for name in names],
        }


if __name__ == "__main__":
    unittest.main()
