import os
from pathlib import Path
import sys
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


if __name__ == "__main__":
    unittest.main()
