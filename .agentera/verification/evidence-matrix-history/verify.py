"""Explicit archival adaptation; run from the checkout with an isolated environment.

The original Go checker remains byte-preserved. Its only runtime adaptations are
the relocated matrix/evaluator paths and a standalone digest helper formerly
supplied by the pipeline test package. Every original hash assertion still runs.
"""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[3]
ARCHIVE = Path(__file__).resolve().parent
ACTIVE = ROOT / "tools/bench/evidence/targeted-residual-evidence-matrix.json"
ORIGINAL = ARCHIVE / "targeted-residual-evidence-matrix.json"
CHECKER = ARCHIVE / "evidence_integrity_test.go.original"
OLD_SHA = "b13a859487c0fbd329174fdda3e67c212761aa61152bf278208f5d1a8257a2d9"
NEW_SHA = "e55c0e979239c4dd80c33f504ef80d40b4d2499bcbd44153d96b45f89af438b6"
CHECKER_SHA = "3481d6204c2d8ba91060e4e5f34fb8e603a7a861632157756659e945d06d5d8b"


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


assert sha(ORIGINAL) == OLD_SHA
assert sha(CHECKER) == CHECKER_SHA
assert (
    sha(ARCHIVE / "evidence_matrix_test.go.original")
    == "505e60e7768c148b01210361be88f495ddb04d8b9257e0c7055f7bc46d1b5b39"
)
assert sha(ACTIVE) == NEW_SHA
old = json.loads(ORIGINAL.read_bytes())
active = json.loads(ACTIVE.read_bytes())
assert len(old["cases"]) == len(active["cases"]) == 7
provenance = old.pop("provenance")
assert provenance["behavior_shapes"] == "deterministic_generated"
assert provenance["generator"] == "internal/pipeline/evidence_matrix_test.go"
prevalence = provenance["historical_prevalence"]
assert prevalence["source"] and len(prevalence["source_sha256"]) == 64
assert (
    prevalence["seventy_two_hour_calls"] == 4216
    and prevalence["seven_day_calls"] == 6030
)
assert len(provenance["source_chain"]) == 3 and provenance["claim_boundary"]
for entry in provenance["source_chain"]:
    assert entry["source"] and len(entry["sha256"]) == 64 and entry["verified"]
assert old == active, "only provenance and formatting may differ"
print(
    "PASS archived matrix/checker pins; identical seven cases and all non-provenance fields",
    flush=True,
)

source = CHECKER.read_text()
old_path = (
    'matrixPath := "../../tools/bench/evidence/targeted-residual-evidence-matrix.json"'
)
new_path = 'matrixPath := "../../.agentera/verification/evidence-matrix-history/targeted-residual-evidence-matrix.json"'
assert source.count(old_path) == 1
source = source.replace(old_path, new_path)
old_evaluator = 'evaluatorPath := "evidence_matrix_test.go"'
assert source.count(old_evaluator) == 1
source = source.replace(
    old_evaluator,
    'evaluatorPath := "../../.agentera/verification/evidence-matrix-history/evidence_matrix_test.go.original"',
)
assert source.count("import (\n") == 1
source = source.replace("import (\n", 'import (\n\t"crypto/sha256"\n\t"fmt"\n')
source += (
    '\nfunc digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }\n'
)
with tempfile.TemporaryDirectory(prefix="archival-integrity-") as tmp:
    adapted = Path(tmp) / "archival_integrity_test.go"
    adapted.write_text(source)
    binary = Path(tmp) / "archival-integrity.test"
    subprocess.run(
        ["go", "test", "-c", "-o", str(binary), str(adapted)],
        cwd=ROOT,
        check=True,
    )
    subprocess.run(
        [str(binary), "-test.count=1", "-test.v"],
        cwd=ROOT / "internal/pipeline",
        check=True,
    )
print(
    "PASS complete historical matrix/report/evaluator/candidate/controlled hash chain",
    flush=True,
)

# The installed acceptance surface remains strict even for valid JSON with the
# same schema/case count. Each local CLI invocation owns fresh application roots.
for name, content, accepted in [
    ("active", ACTIVE.read_bytes(), True),
    (
        "tampered",
        ACTIVE.read_bytes().replace(b"HEAD_REQUIRED", b"HEAD_MODIFIED"),
        False,
    ),
    ("wrong-sha-same-json", ACTIVE.read_bytes() + b"\n", False),
    ("historical", ORIGINAL.read_bytes(), False),
]:
    with tempfile.TemporaryDirectory(prefix="matrix-cli-") as tmp:
        root = Path(tmp)
        env = dict(os.environ)
        env.pop("KTX_DB", None)
        for key in [
            "HOME",
            "XDG_DATA_HOME",
            "XDG_CONFIG_HOME",
            "XDG_CACHE_HOME",
            "XDG_STATE_HOME",
            "XDG_RUNTIME_DIR",
            "TMPDIR",
            "TMP",
            "TEMP",
        ]:
            directory = root / key
            directory.mkdir()
            env[key] = str(directory)
        matrix = root / "matrix.json"
        matrix.write_bytes(content)
        database = Path(env["XDG_DATA_HOME"]) / "ktx/ktx.db"
        subprocess.run(
            ["go", "run", str(ARCHIVE / "seed.go"), str(database)],
            cwd=ROOT,
            env=env,
            check=True,
        )
        before = database.read_bytes()
        command = [
            str(ROOT / "build/ktx"),
            "stats",
            "live-validation",
            "--from",
            "2026-08-01T00:00:00Z",
            "--to",
            "2026-08-01T01:00:00Z",
            "--collection",
            "collectionabcdef",
            "--ingress",
            "run",
            "--candidate",
            "keep-ends:73:1",
        ]
        if name != "active":
            command += ["--matrix", str(matrix)]
        result = subprocess.run(
            command, cwd=ROOT, env=env, capture_output=True, text=True
        )
        assert result.returncode == 3, (name, result.returncode, result.stderr)
        report = json.loads(result.stdout)
        assert report["artifacts"]["matrix"]["accepted"] is accepted, (name, report)
        assert (
            report["artifacts"]["matrix"]["sha256"]
            == hashlib.sha256(content).hexdigest()
        )
        assert report["status"] == "blocked"  # no fabricated live qualification
        assert database.read_bytes() == before
        print(
            f"PASS local CLI {name}: accepted={accepted}, empty-evidence exit=3, seeded DB unchanged"
        )
