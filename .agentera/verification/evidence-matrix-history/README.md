# Historical matrix and integrity checker

Decision `faoenlxbos` explicitly authorizes the active artifact identity cutover,
not a marker exception or weaker validation. This directory preserves the exact
original matrix, checker, and evaluator bytes. The original dated reports and candidate and
controlled-comparison receipts remain unchanged at their old locations. Their
matrix paths describe history, not the current accepted artifact.

`verify.py` is the separately identified executable archival adaptation. It pins
all three archived originals, verifies that all seven cases and every other
non-provenance field equal the active matrix, then runs every original Go
integrity assertion with only the matrix/evaluator file locations adapted and its digest
helper supplied. The historical provenance assertions also remain checked here.
The Go test binary runs from the original pipeline working directory. The local
CLI fixtures use an explicitly seeded disposable continuity database, whose bytes
must remain unchanged. It exercises the freshly built local CLI's default path,
tampered JSON, whitespace-only wrong SHA, and historical identity rejection.
Acceptance of the active artifact is not live-evidence qualification.

Run after `mage build`, in a fresh HOME/XDG/TMP environment with KTX_DB unset:

```sh
python3 .agentera/verification/evidence-matrix-history/verify.py
```

Current candidate and controlled-comparison behavior coverage stays in
`internal/pipeline`. The matrix evaluator replaces only historical provenance
assertions with provenance absence and the new exact digest; its generators,
case validation, candidate evaluation and controlled comparison are unchanged.
The archival verifier is explicit project-memory
verification, not a new shipped command or part of normal package discovery.
