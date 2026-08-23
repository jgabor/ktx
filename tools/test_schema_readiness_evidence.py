import os
from pathlib import Path
import sys
import tempfile
import unittest

from tools import schema_readiness_evidence


class MeasureTest(unittest.TestCase):
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
