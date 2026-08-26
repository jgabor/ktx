# Changelog

## [Unreleased]

### Added

- `ktx bench screen` now evaluates explicit complete pipeline configurations over one frozen generated population without model or database access, emitting bounded aggregate reconciliation and a controlled-fact-gated shortlist.
- `mage schemaReleaseGate` now blocks schema releases unless the exact prior physical schema passes the retained 3.2 GB scale, generated-adapter concurrency, interruption/restart, correctness, and approved combined-workload limits. Memory is aggregate concurrent KTX RSS, with per-process peaks retained separately.
- The schema-readiness contract now requires automatic preparation for every supported stale database surface, single-owner timeout attribution, and reproducible controlled-host resource gates with a retained raw baseline and disposable fixture harness.
- Schema 14 adds explicit OpenCode collection evidence for hook-owned delivery, wrapper-owned runtime timing and lifecycle, acknowledged recovery cues, linked byte-exact recovery attempts, and the read-only `ktx stats live-validation` fixed-window report. Ordinary `ktx run` behavior remains unchanged.
- Schema 13 retains metadata-only `run` ingress collection scope and lifecycle closure evidence. Continuity qualification reports explicit gaps for missing closure, interruption, conflict, pre-deployment history, and unelapsed windows; after readiness, evaluation is read-only and does not migrate or backfill evidence.
- `ktx stats summary` schema 10 and `ktx stats breakdown` schema 4 report trusted-ingress agent-attribution coverage over the exact output-bearing call population; historical and unverified calls remain unknown, and empty coverage is null.
- New wrapper sessions record the launched executable as identified ownership; complete recognized OpenCode and Codex hooks can identify reused sessions, while compatibility input remains unknown and the first trusted identity wins.
- Schema 12 records session attribution authority separately from the existing agent label; historical labels remain readable and migrate as unknown.
- `ktx stats summary` schema 9 and `ktx stats breakdown` schema 3 report requested-window latency states, runtime provenance, measured coverage and time bounds, and millisecond distributions; unavailable latency remains null.
- Schema 10 preserves latency values while recording explicit measured, unknown, or malformed state, milliseconds, and runtime authority; migrated history remains non-authoritative.
- `view_image` uses an explicit empty passthrough profile by default, and the example configuration exposes the same route.
- Newly accepted source and artifact rows retain independent first-wins command provenance from the existing closed producer vocabulary. Schema 11 leaves historical provenance unknown and does not change content-only output.
- Optional `[logs]` configuration now makes one best-effort private typed JSONL append for database-unavailable diagnostics without changing stderr behavior.
- Current prompt-cache comparisons use methodology v6 and `cache-reuse-v2` to emit token-weighted summary schema 5; methodology v5, summary schemas 1–4, and `cache-reuse-v1` remain historical evidence.
- Schema-5 prompt-cache benchmark summaries now publish a fail-closed, quality-adjusted API-equivalent cost guard; valid rejected candidates remain evidence but are not recommended by rank.
- `ktx stats summary` schema 8 now token-weights `prompt_cache_token_hit_rate_pct` across all accepted providers and models under the supported runtime contract without changing the response shape. Measured zero remains zero, incomplete evidence remains null, and retained schema 6 summaries remain unchanged historical evidence.
- `mage engineeringVerify` retains component and real-process load reports plus a hash manifest. It requires load correctness, exact reconciliation, and supported-lock checks while keeping timing descriptive.
- Engineering verification now retains a separate blocked-decompression process report with output barriers, byte-exact output evidence, hook timing and outcome, participant classes, and exact storage reconciliation.
- Engineering reports now record first-class wall-clock timing, focused `ktx read` measurements, and a reproducible noise-aware comparison contract while preserving retained schema-1 evidence.
- Real-process load reports now compare matched writers-only and mixed-agent workloads with distinct sessions, exact reconciliation, supported failure rates, and actual and percentage contention penalties.
- A pinned developer hook-only contention workload uses the 4,096-byte seed-6001 fixture at concurrency 1/2/4/8/16/32. Its one warmup and five measured rounds per level produced 6 warmup and 30 measured labels, 378/378 completions, 36/36 reconciled rounds, zero observed failures, and exact totals of 378 sources, artifacts, dispositions, source producers, and artifact producers on disposable databases.

### Fixed

- `mage install` now refreshes already-enabled global and current-project OpenCode and Codex adapters after installing the development binary, preserves unrelated Codex hooks, and fails with an exact recovery command for every scope that does not refresh.
- Current user and agent guidance now consistently describes automatic stale-schema readiness, post-readiness read-only queries, forward-only newer-schema failure, isolated verification, and the separate production-scale schema release gate.
- Query, doctor, telemetry, hook, recovery, and retained-artifact inspection paths now prepare existing supported older databases automatically, then use read-only access where required. Missing-database, current-schema, newer-schema, coordination, and timeout contracts remain unchanged.
- Schema 15 preparation now uses an atomic primary-key cursor for bounded compressed-timestamp batches, populates sortable timestamps on every artifact write path, resumes after process termination, and publishes schema 16 without a data-sized final index build.
- Generated `ktx.ts` callbacks no longer kill KTX after 10 seconds; `ktx hook` retry preflight now uses writer-preferred coordination and stable `database-unavailable` attribution.
- `ktx run` now rejects an explicit empty `--record-stream=` value while preserving supported boolean and numeric forms and child arguments.
- Plugin commands now reject malformed configuration without mutation, and durable configuration, price-cache, report, experiment, preflight, and replay-fixture updates publish complete same-directory replacements without treating temporary files as authority.
- Current-schema hooks now fast-open without exclusive coordination, fall back to coordinated creation or migration, then revalidate schema under writer ownership and keep ordered source, artifact, and disposition commits inside one exclusive coordination window. A concurrent upgrade fails closed before hook evidence writes, while complete exact retries and pre-source rejections remain read-only.
- Database-unavailable diagnostics now identify the callback surface, pinned executable, database target, stable failure class, operation phase, elapsed wait, and observed Linux coordination blockers without exposing driver text or persisted content.
- Linux coordinator-timeout diagnostics now label verified supported blocker commands from a closed vocabulary and report all uncertain identities as `unknown`.
- Supported read-only database handles now coordinate with writers, and supported writers coordinate each SQLite transaction across processes.
- OpenCode ordinary telemetry and hook callbacks now share one arrival-order queue, and each write-capable database handle uses one process-local SQLite connection.
- SQLite lock exhaustion now returns `database-unavailable` with the canonical database path and retry guidance instead of driver lock diagnostics.
- `ktx read` now releases supported read ownership after retrieving a record, so blocked output no longer delays an unrelated supported writer.
- `ktx hook` now takes delivery footer policy from the selected retained artifact's recorded structured-recovery provenance. Exact retries preserve repaired JSON and NDJSON validity and ordinary retained-artifact footer behavior; legacy artifacts without provenance remain readable and keep conservative behavior without inferred reconstruction.
