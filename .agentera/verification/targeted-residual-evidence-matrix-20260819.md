# Targeted residual representative evidence matrix — 2026-08-19

## Outcome

**MATRIX READY.** Candidate evaluation may use the retained matrix; no route or transform has been evaluated, selected, or implemented.

The exact opportunity signal remains `read / command absent / subcommand absent / default / default_fallback / anti_worsening_rejected / 1k-10k`.

## Inventory

The machine-readable matrix is `tools/bench/evidence/targeted-residual-evidence-matrix.json`, SHA-256 `7eece02dc19887eafb17403bc0a0c11c9d737a3d08d55fe226470f095c548a32`.

It contains 7 bounded cases: 4 eligible and 3 rejecting byte-exact passthrough cases. Category coverage is head 1, middle 1, tail 1, structured 1, failure 1, eligible 4, and passthrough 3. All generated outputs are 4,096–7,168 bytes, within the selected 1k-10k band. The tool rule has positive `read` and rejecting `bash` cases. The explicit-success rule has positive success and rejecting failure/unknown cases.

Every case defines, before candidate evaluation, a complete `fixed_lines_v1` or `json_object_v1` recipe; exact target bytes; required facts and exact occurrence counts; explicit error state, text, and count; byte-exact recovery; eligibility; and passthrough. `fixed_lines_v1` specifies the fill byte, newline interval, facts, and exact byte offsets. `json_object_v1` specifies every semantic value, repetition count, padding byte, and target length. The executable generator and behavioral validator are co-located in `internal/pipeline/evidence_matrix_test.go`, SHA-256 `505e60e7768c148b01210361be88f495ddb04d8b9257e0c7055f7bc46d1b5b39`.

## Provenance and claim boundary

Behavior shapes are deterministic generated cases and do not claim historical content or prevalence. Historical recurrence comes only from the existing non-live aggregate chain. Current committed bytes were re-audited without rewriting the historical artifacts:

- `historical-aggregates-20260812-19.md` — SHA-256 `7671534015cca4bc0df841966c016e0d1d016e18f2df0abba25615a976e990ca`. Rechecked the fixed-window totals, detailed-plus-omitted reconciliations, target-tool disposition totals, and descriptive-only/continuity-unknown boundary.
- `multidimensional-residual-ranking-20260819.md` — SHA-256 `c6a4f025770566589186b3017cf96fb08fbcb36559419cc999e5cfa4e0571903`. Rechecked its citation to the aggregate artifact, the target rows (4,216 calls in 72 hours and 6,030 in seven days), the largest-subgroup statement against the retained target rows, and the bounded-experiment-only boundary. Its cited predecessor digest is historical and stale; this successor cites the current verified aggregate digest above.
- `targeted-residual-diagnosis-20260819.md` — SHA-256 `afb1dee3499109545a04ed6608ac0448e1890ebc5ac9d03f1b4712685c258fc0`. Rechecked the generated 6,400-byte controlled inventory, supported metadata and unsupported size/content predicate limits, mechanical-reduction-only conclusion, and representative-case recovery condition.

No raw historical content or ambient database was inspected. The previous no-change cycle remains historical evidence and is not reclassified. Only this successor report and matrix cite the current re-audited hashes; historical artifacts and their prior audit records remain unchanged.

## Immutable evaluation rules

Candidate evaluation must run every case. Eligible outputs must preserve every required fact and exact count, error text and state, and byte-exact recovery. Ineligible outputs must pass through byte for byte and create no transformed artifact. Eligibility may use only configured tool, command prefix, subcommand, and documented exit status, with ordered first match. Content and size are not routing predicates. Growth, omitted evidence, count changes, changed error behavior, failed recovery, or failed passthrough rejects a candidate.

These rules establish the evaluation boundary, not candidate safety. Safety is established only if a later candidate passes the matrix. Historical aggregate recurrence does not establish that any transform will reduce these behavior shapes.

## Verification

Each verification command allocated a fresh `/tmp/ktx-matrix-verify.*` root, unset `KTX_DB`, and set fresh `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME`, `TMPDIR`, and `KTX_VERIFY_ROOT`.

```sh
mage build
go test ./internal/pipeline ./internal/router ./internal/config
./build/ktx binary-capabilities --format json
git diff --check
```

The focused matrix test generates all seven cases and verifies their exact lengths, marker and error occurrence counts, JSON validity, explicit failure/unknown state, eligibility from tool plus outcome, byte-exact ineligible passthrough with no artifact, byte-exact retained-original recovery, category coverage, and routing/provenance schema. It does not run or score a compression transform.

Results: build passed; focused tests passed (`internal/pipeline`, `internal/router`, `internal/config`); the changed local CLI reported schema `ktx.binary-capabilities/v1`, build version `dev`, maximum database schema 13, and executable SHA-256 `d186bc7f1e1e6d74ec44407cb041057e5b47c29d675364721607621a612d3c88`; `git diff --check` passed. No database-capable command ran.

## Limitations and recovery

The matrix does not inspect historical content, prove subgroup-precise size routing, evaluate a candidate, prove reduction, authorize comparison, or support a default/profile rollout. If a future candidate cannot pass every applicable case, the candidate outcome must be **BLOCKED**, with recovery requiring a deterministic case or supported metadata predicate that resolves the exact failed fact, count, error, recovery, eligibility, or passthrough expectation. It must not be labeled no-candidate merely because safety or recurrence is unresolved.
