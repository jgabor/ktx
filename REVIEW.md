# Code review — overcomplication, overengineering, redundancy, intent mismatches

Date: 2026-08-23

Scope: full-repo read-only audit across four sweeps — `internal/db` + `internal/dbreport`,
`internal/cli` + `cmd/ktx`, `internal/cache` / `pipeline` / `canon` / `parser` /
`telemetry` / `config`, and `tools/` + docs + CI + magefile.

Overall verdict: the core designs are sound — flat dispatcher registry, narrow Stage
interface, fail-closed DB validation, coherent coordination stack. The debt is
concentrated in duplication and transitional scaffolding around those good cores,
plus a handful of genuine behavior defects where output does not match inferred intent.

Findings are grouped by theme; each carries a severity and file:line references.
Line numbers refer to the working tree as of this review date and will drift.

---

## 1. Output does not match inferred intent

### 1.1 `ktx run <agent> --help` silently exits 0 with no output — High

`filterKtxFlags` (run.go:239-258) strips a post-agent `--help` from the agent's
arguments; the dispatched path then swallows it (`run.go:499-502`, `return nil, nil`).
Nothing is rendered, no error is raised, exit code 0 — and the agent never sees the
flag either, because it was removed from `agentArgs`. The help text explicitly promises
both behaviors are meaningful: "-h, --help show this help" (run.go:728) and "may also
appear after the agent command" (run.go:736). The test-only direct-call path does render
help (`printRunHelp`, run.go:124-127), so the two entry points also disagree with each
other.

Why it matters: worst-case outcome for a wrapper whose contract is transparent proxying
(run.go:8-12).

### 1.2 Double error envelopes on stderr for `ktx doctor` and `ktx init opencode` — High

Handlers call `printError` themselves _and_ return an error that the dispatcher renders
again:

- `doctorLogic` prints the detailed envelope then returns an exit status;
  `doctorCommandFunc` converts it into a fresh generic error (`usageError("ktx doctor:
usage error")` / `internalError("ktx doctor: status is error")`) which
  `dispatch.go:377` prints a second time (doctor.go:144-163, 211-222). This is also an
  exit-code round trip: logic returns a code -> wrapper fabricates an error ->
  `exitCodeOf` maps it back to the same code.
- `initOpencodeUninstall` / `initOpencodeShow` do `printError(stderr, internalError(...))`
  then `return internalError(...)` — two distinct error values from one constructor
  (init.go:810-838), producing two envelopes per failure.

The correct pattern already exists in the bench subcommands: render once, forward the
bare status via `benchSubToExit` / `exitWith` (bench.go:167-174, errors.go:75-81),
which the dispatcher honors without re-rendering (dispatch.go:362-365).

Why it matters: scripts and agents parsing stderr JSON see two envelopes for one
failure and can double-count `error.code`.

### 1.3 Empty-input phantom transforms violate the no-help-no-row contract — High

For empty input, both the anti-worsening gate (`len(input) > 0`, pipeline/pipeline.go:365)
and the empty-output guard (`len(input) > 0`, pipeline.go:427) never fire, so every
configured stage records `ran=true` while output stays identical to input. Downstream,
`compressPipelineWithConfig` treats non-empty `Ran` as transformation
(`execution.output != source.Output || len(res.Ran) > 0`, cli/compress.go:224), so an
empty-output tool call under a configured profile yields `transformed=true` and the hook
path persists a compressed artifact whose `Output` is byte-identical to the source
(hook.go:596-597 builds a candidate whenever `transformed`).

This contradicts the runner's own documented contract: "a transform that does not help
returns the original with compressed_id=null ... NO compressed row is created"
(pipeline.go:355-361). The `|| len(res.Ran) > 0` clause is otherwise redundant post-gate;
it is exactly the clause that manufactures the phantom artifact.

### 1.4 First-artifact authority implemented three ways atop a non-monotonic timestamp — Medium-High

Seven sites select the authoritative first `compressed` row per source. Five use
`ORDER BY timestamp ASC, id ASC` (compress.go:109, 242, 305; stats.go:276-280;
disposition.go:123-125); two use `ORDER BY timestamp,rowid`
(recovery_evidence.go:41, runtime_source_observations.go:63).

Additionally `compressed.timestamp` stores caller-supplied RFC3339Nano text
(compress.go doc, schema.go:267), and Go's RFC3339Nano strips trailing fractional zeros,
so `...00.1Z` sorts _after_ `...00.12Z` under SQLite string comparison. The codebase knows
this pitfall — it maintains `timestamp_unix_nano` columns plus a backfill for exactly
this reason on `tool_calls` and `session_telemetry` (schema.go:660-686, timestamp.go) —
but none of the artifact-selection queries use a sortable form.

Why it matters: when two artifacts share a timestamp boundary, the delivery check
(runtime_source_observations.go), recovery-cue check (recovery_evidence.go), and
disposition check (disposition.go) can pick different "first" artifacts, so legitimately
delivered output can be rejected.

### 1.5 Plugin config writes are not atomic despite claiming so; corrupt config swallowed — Medium

Header comment claims "`ktx plugin add/remove` reads, modifies, and writes the config
atomically" (plugin.go:4-5); implementation is a plain `os.WriteFile(path, ..., 0o644)`
with no temp-file+rename (plugin.go:104, 239). A crash mid-write truncates the user's
ktx.toml. Additionally `cfg, _ := config.Load()` swallows TOML parse errors in
`pluginAdd` (:71), `pluginList` (:143), and `pluginRemove` (:183): a corrupt config is
silently treated as "no plugins" for duplicate/removable checks while the textual editor
still edits the broken file — inconsistent state with no diagnostic.

### 1.6 README and shipped help advertise nonexistent flags — Medium

- README instructs `ktx config show --json`; no such flag exists anywhere in
  `internal/`. The verb's own help says `--format json|text` (config_cli.go:151) and the
  dispatcher rejects unknown flags. Following the README yields a usage error.
- Shipped help advertises `ktx plugin list [--json]` with that exact example
  (plugin.go:11, 395, 404), but `pluginCommandFunc` drops `subArgs` entirely for `list`
  (plugin.go:45-49, 122) and `pluginList(stdout)` takes none. It "works" only by
  accident since output is always JSON — and `--text`, `--format json`, or any typo'd
  flag is silently ignored too.

### 1.7 `ktx db report` bypasses the schema-version gate every other read command has — Low-Medium

`dbreport.Generate` opens via `db.OpenReadOnly(path)` directly (report.go:24) instead of
`db.OpenQuery` (database.go:325-358), which returns actionable
`SchemaTooOldError`/`SchemaTooNewError`. On an unmigrated database the user instead gets
a raw `"required table is missing: recorded_streams"` hard-fail from `extractSchema`
(report.go:134-153), or silently partial aggregates on schemas where all four tables
exist but shapes differ. The CLI layer demonstrates the intended pattern at
cli/db.go:113 but `dbReportLogic` (cli/db.go:180) skips it.

### 1.8 Doctor's schema check disagrees with migration's mandatory columns — Low

`expectedSchema["sessions"]` in inspect.go:64-72 omits `agent_attribution_authority`,
while database opens fail without it ("current sessions schema is missing agent
attribution authority", schema.go:527-533). `ktx doctor` reports a healthy schema for a
database that `Open` will refuse to treat as current.

Related drift: prices.go:284-290 claims the mutex guards "the on-disk write," but
`WriteToDisk` holds only an RLock for marshalling and performs the file write outside it
(prices.go:444-494).

### 1.9 Probe preflight attests `network=none` while its live mode requires network — Low

`tools/bench/evidence/opencode-runtime-cache-controls/probe.sh` prints
`ready: network=none model_calls=0 ...` for `--preflight`, while `--live-changed` runs
`docker run --network bridge` gated on `OPENCODE_API_KEY`. The line reads as an
attestation about the probe itself; an evidence auditor could wrongly conclude the
changed-prompt leg ran offline.

### 1.10 StructuredFallback provenance survives the anti-worsening revert — Low

The structure guard sets `res.StructuredFallback = true` and replaces `Out` with the
faithful rendering (pipeline.go:339-342); the anti-worsening gate then reverts `Out` to
the input (pipeline.go:365-369) but leaves the flag set. Callers observe
`StructuredFallback=true` alongside `Out == input` — a provenance claim that no longer
describes the returned bytes. Contained today on the hook path, but `ktx compress`'s
always-insert path (compressCore, compress.go:244-283) persists rows carrying
`"structured_fallback":true` next to an untouched original.

---

## 2. Overengineering and parallel machinery

### 2.1 Twelve test-only `runXWith` seams duplicate the dispatcher lifecycle — High

The dispatcher (dispatch.go:245-381) owns format extraction, DB resolution/open, stdin
capture, `printError`, and `exitCodeOf`. Yet twelve verbs also export a
`runXWith(args, opts, stdout, stderr)` seam that re-implements the same pipeline and is
called only from tests: read.go:30-53, write.go:36-55, get.go:38-66, list.go:37-65,
compress.go:89-108, decompress.go:47-66, run.go:92-135, bench.go:56-104, db.go:38-45,
prices.go:48-55, init.go:673-680, doctor.go:122-125 (~250 lines total). Some re-run
`extractFormat` the dispatcher already executed (get.go:43, list.go:42); comments admit
the coupling (list.go:39-41).

Why it matters: any change to DB-open or error-envelope behavior must be replicated in
up to 12 places; drift between the two paths is invisible until one passes while the
other regresses. Tests can invoke `runDispatch` directly — several test files already do
(dispatch_query_test.go, stats_live_validation_test.go:168).

### 2.2 Twin nine-case switch routers in `bench` — Medium

`runBenchWith` (bench.go:56-104, test-only) and `benchCommandFunc` (bench.go:109-149)
each contain a full switch over run/report/export/compare/rank/validate/corpus/agent/
evidence mapping to the same nine handlers. One returns exit codes directly, the other
wraps them via `benchSubToExit`. The table must be updated in lockstep; a single
`map[string]func` consulted by one router removes the twin tables.

### 2.3 `ktx db --path` parsed three times through string form — Medium

`parseDBDispatchArgs` hand-parses `--path` (db.go:235-255); `dbCommandFunc` re-injects
it into argv (`args = append([]string{"--path", parsed.pathOverride}, args...)`,
db.go:226-231, comment: "Rebuild the args so dbLogic remains the single path-resolution
module"); `dbLogic`'s own flag set parses it a third time (db.go:57-63). A typed
parameter would do.

### 2.4 Four overlapping arg-scanning layers in `ktx run` — Medium

Production dispatch path: `parseRunDispatchArgs` (run.go:541-562, strips
`--record-stream`/`--collection=` anywhere) -> dispatcher flag set (dispatch.go:282-296)
-> post-agent `filterKtxFlags` scan (run.go:239-258, strips quiet/help/record-stream
again). Test-only path adds `parseRunArgs` (run.go:168-196) plus a second
`extractCollectionFlag` call (run.go:95). Three of four implement the same
"strip known ktx flags, keep everything else" loop with slightly different accepted
sets. Comments at dispatch.go:206-211 and run.go:495-498 document why each layer exists —
a sign the layers compensate for each other rather than compose.

### 2.5 Hot-path PRAGMA introspection guarding an unreachable state — Medium

Every `PersistToolCall` runs `tableColumns(tx, "tool_calls")` (a `PRAGMA table_info`)
inside the write transaction and picks between two full INSERT statements, the legacy
one selected via the opaque splice `args = append(args[:7], args[8:18]...)`
(records.go:336-354). Every `GetToolCall` runs the same PRAGMA and keeps a second SELECT
plus `dest = dest[:17]` (records.go:562-580). All production open paths guarantee a
current schema first — `Open`/`OpenExisting` migrate unconditionally (database.go:177,
221), `OpenHook` gates on version (database.go:272-274), `OpenQuery` rejects old/new
(database.go:349-356), `WithHookWriter` re-checks under lock (database.go:516-525).
Only the artificial downgrade in migration_matrix_test.go:128 reaches the legacy branch.
Cost: a PRAGMA round-trip on every tool-call insert/read, forever, on the hottest path
in the tool.

### 2.6 Schema truth maintained twice, plus three identical struct types and five copy-pasted migration loops — Medium

Column lists duplicated verbatim between schema.go (costColumns :351-358,
benchRunsColumns :416-431, compressedColumns :461-469) and inspect.go's doctor lists
(:64-120). Three structurally identical `{Name, Type}` types (schema.go:339-342,
401-404, 448-451). Five near-identical ALTER TABLE ADD COLUMN loops in `migrateTx`
(schema.go:611-659) plus two more (692-700, 706-714) — seven instances differing only
in error-message prefix. inspect.go:88 even documents the hand-sync: "The order matches
costColumns in db.go" — referencing a file that does not exist (see 3.6).

### 2.7 Three copy-pasted preflight validators, drift begun — Medium

`validateProposal` (cache/reuse/preflight.go:228-287), `validateAnthropicProposal`
(anthropic_preflight.go:9-58), and `validateGeminiProposal` (gemini_preflight.go:110-154)
repeat the same preamble: schema-version equality, exactly-2 paths, destination-prefix
check, redaction-policy-required, per-path blank checks. Global-ceiling logic is likewise
duplicated (`unenforceableConditions` preflight.go:289-331 vs `geminiCeilingCondition`
gemini_preflight.go:166-187, same walk order, near-identical messages). Gemini already
omits the per-reference loop the others have. Validator selection is a hidden transport
scan (`if path.Transport == "direct_anthropic" { validate = ... }`, preflight.go:161-168)
— a mixed-transport proposal gets validated by whichever validator matches its first
Anthropic path only. Each new provider multiplies the copies.

### 2.8 `initCommandFunc` duplicates `initLogic` line-for-line — Medium

Compare init.go:687-716 with init.go:954-986: identical sequence — parse, help,
missing-runtime usage error (same string at :698 and :968), uninstall/show mutex error
with copy-pasted comment block (:701-703 vs :971-973), same three-way runtime switch.
Delegating (as `runDBWith` delegates to `dbLogic`, db.go:38-45) removes ~30 lines and
one drift surface.

---

## 3. Redundancy

### 3.1 Five divergent atomic-write helpers across packages — Medium

At least five atomic temp-file+rename implementations exist across internal/cache,
internal/pipeline, internal/db, internal/cli, and tools. At least one uses a
`path + ".tmp"` fixed name rather than `os.CreateTemp`: concurrent writers race on the
same temp path (no `O_EXCL`) and a crash between write and rename leaves a stale `.tmp`
beside the live cache (the prices-cache writer). One shared helper (single fsync policy,
single temp-file policy) eliminates both duplication and divergence.

### 3.2 Twin JSON compactors inside one module — Medium

Two implementations of "compact one JSON value, else NDJSON if every line is a complete
value, preserving trailing newline, rejecting blank records":

- pipeline/json_guard.go:87-123 (`compactJSON` / `compactJSONLines`)
- pipeline/stages/json_compact.go:14-49 (`JSONCompact`)

Near line-for-line parallel (`bytes.Split` on `\n`, same trailing-newline handling).
These encode load-bearing semantics; if one changes, an artifact can pass the guard yet
differ from what `json-compact` would emit.

### 3.3 Copy-pasted scan blocks between paired getters — Low

- `QueryBenchRuns` vs `QueryBenchRun`: identical 14-column SELECT and five identical
  null-coalesce blocks (db/bench.go:179-237 vs 244-293).
- `GetCompressed` vs `GetCompressedBySource`: same 7-column scan twice (compress.go:278-294,
  300-317).
- `GetRecordedStreamBySession` vs `GetRecordedStreamByBenchRun` (recorded_streams.go:181-201,
  213-233).
- `GetSession` vs `ListSessions` (records.go:654-676, 711-742).

Each pair must be edited in lockstep when a column is added; a `scanBenchRow(scanner)`
style helper removes the bug class.

### 3.4 Per-verb copy-pasted flag-set/help boilerplate — Low

The identical five-line prologue (`flag.NewFlagSet(..., ContinueOnError)` +
`fs.SetOutput(io.Discard)` + `BoolVar(--help)` + `BoolVar(-h)`) appears in ~15 places
(get.go:84-87, list.go:80-83, read.go:69-72, write.go:67-70, compress.go:117-120,
decompress.go:79-82, stats.go:418-421, 748-751, prices.go:60-63, db.go:57-61,
doctor.go:149-152, bench_run.go:79-80, cli.go:108-120, dispatch.go:282-289).
Database-path footers repeat verbatim across ~14 help printers. A `contract_sync_test.go`
exists precisely to police this class of drift — evidence the pattern is already
straining.

### 3.5 Token estimate (bytes/4) re-implemented four places — Info

`(len+3)/4` independently in pipeline/stages/token_budget.go:69, cli/stats.go:554-556,
cli/bench_experiment.go:392, experiment/corpus.go:145. Trivial each, but a heuristic
change must move four sites plus contract-sync test strings together.

### 3.6 Stale references, dead switch, garbled comments — Low

- bench.go:4 and inspect.go:88 reference "`db.go`", which does not exist in `internal/db`.
- schema.go:471-473 carries an orphaned doc comment for `DefaultPath` mid-file; the real
  function lives in database.go:80.
- `providerModel` (db/bench.go:111-119): a switch whose both branches return
  `"anthropic"` — the shape promises model-specific mapping that does not exist.
- decompress.go:27-28 registration comment cites a renamed field (`needsDB=true`; actual
  option is `DatabaseAccess: DatabaseReadOnly`, decompress.go:154).
- get.go:220-222 contains a garbled sentence ("If a // Unknown record variants...").
- Self-contradictory contract comment on `Result.Ran` (pipeline.go:253-256): the second
  half contradicts the first; the later restatement on the field itself (:271-279) is
  correct.
- dispatch.go:319-323 special-cases one verb by name (`if verb == "hook"`) where registry
  options should carry the choice.

### 3.7 Pipeline recipe guidance maintained in three places — Low

The same sanitize / keep-ends-mid / aggressive-head recommendations appear in commented
recipes in ktx.example.toml (~155-170), as TOML files in tools/bench/pipelines/\*.toml,
and as prose in docs/pipelines.md. They agree today; nothing enforces that they continue
to.

### 3.8 Dead and unconsumed API surface — Low

- `db.LookupPrice` (prices.go:643-654): zero callers outside tests.
- `PriceRateSelection`/`SelectRatesWithProvenance` provenance fields (`Basis`,
  `ContextTierSize`, `ContextTokenCount`): computed, never consumed; the only production
  chain is `SelectRates` -> `.Rates` (prices.go:276-278, cost.go:89).
- `db.OpenWithoutMigration` (database.go:461-470): tests only.
- Alias-shim files cache/reuse/sequence.go and estimate.go (~27 lines each) re-export the
  sub-package; the primary consumer imports the sub-package directly
  (experiment/secondary_cache_estimate.go:79). Only `RequiredRuntime`/
  `RequiredRuntimeVersion` reach the facade (cli/stats.go:340).
- `pipeline/stages.ParseNameArg` (stages.go:116-123): character-for-character duplicate
  of pipeline.splitToken (pipeline.go:195-202), exported for tests only.
- Test-only wrappers `teeAndParse`/`teeAndParseRuntime` (run.go:580-587) fill default
  parameters for test callers; production calls `teeAndParseRuntimeCollection` directly.
- Orphaned docs: docs/cache-evidence-opencode-runtime-temporal-controls.md has zero
  inbound references; several other cache-evidence docs are reachable only from
  historical progress notes.

### 3.9 Single-caller wrappers, ceremony interfaces, delegation chains — Info

- `router.Match` wraps `Resolve` discarding provenance (router.go:62-64);
  `CommandFromInput` wraps unexported `commandFromInput` verbatim (router.go:126-128).
- Exported `RecoveryFooter` -> `buildRecoveryFooter` -> `dbpkg.RecoveryFooter`: two
  pass-through wrappers around one call (hook.go:859-866).
- `costDeriver` empty struct whose single method could be a function (db/cost.go:30-34,
  164-171; called at records.go:765).
- Single-implementation interfaces: dbreport `queryer` (report.go:85-88), db
  `deliveryQueryer` (compress.go:93-95), `PriceSource` (cost.go:30-34).
- Heavy write-func threading: eight persistence functions take an injected
  `write func(ctx, func(*sql.Tx) error) error` so logic can run under coordinator lock
  or held lease, plus a mirrored ~11-method HookWriter delegate surface
  (records.go:203-486, compress.go:162-272, disposition.go:37-87). Verified intentional
  (database.go:504-512); informational — the largest boilerplate source in the
  persistence layer.
- `stats live-validation --help` routing hack (stats.go:79-81): swaps argv order so the
  dispatcher's stdlib flag set sees `--help` first; works only for exactly two args in
  exactly that order.
- `matchRecordStreamFlag` coerces invalid values to true (run.go:215-222), justified
  inline but contradicting the CLI-wide rule that malformed flags are usage errors
  (dispatch.go:273-276).

---

## 4. Other behavior notes

- **Timeout abandons rather than cancels the pipeline** (Info): on expiry
  compressPipelineWithConfig reports "returning original" (cli/compress.go:209-217)
  while the goroutine keeps running, including external plugin subprocesses with no kill
  switch. Buffered channel avoids the leak; a hung plugin outlives the report.
- **Doc-vs-behavior on stage copying** (Low): pipeline.go:287-291 claims "each stage
  receives a copy of the previous stage's output," immediately conceding aliasing; no
  copying occurs anywhere in `Runner.Run` (:313-337).
- **Empty-input runs mark all stages as ran**: root cause shared with 1.3 — the
  anti-worsening and empty-output guards both precondition on `len(input) > 0`
  (pipeline.go:365, 427).
- **Hook computes then discards its own footer gate** (Medium, cli-local):
  `hookLogicObserved` computes `envelopeOutput` including the recovery-footer
  anti-worsening check (hook.go:570-581) then discards it (`_ = envelopeOutput`,
  hook.go:669); all success paths instead call `writeHookResult`, which rebuilds the
  footer with a mirrored condition (hook.go:826-831 uses `<` where hook.go:573 uses
  `>=`). Only side effects of the first gate are consumed.

---

## 5. Clean areas (verified)

- Dispatcher core design (flat registry, declarative `RegisterOptions`, single error
  envelope with stable codes and canonical mappings) — genuinely good; offenders above
  simply bypass it.
- `binary_capabilities.go` — model citizen: typed `ParseArgs`, single handler, no seam.
- Error envelope primitives (`errors.go`, `commandExit`) — sound and underused by the
  double-render call sites.
- canon.go — careful RFC 8785-style canonicalization with explicit surrogate and
  duplicate-key fallbacks; internally coherent.
- identity, version, config loading — small, boring, well-tested.
- Ratio math consistent between writer and verifier; token-based reductions label units.
- Coordination stack (~600 lines flock/fcntl gating with `/proc/locks` holder
  attribution) — large but coherent rather than gratuitous.
- Recent-window stats queries are SQL-bounded and indexed rather than full-table
  timestamp scans. `StatsSummaryRows` filters `tool_calls.timestamp_unix_nano`, and
  `StatsSummaryCacheRows` filters `session_telemetry.captured_at_unix_nano`
  (stats.go:126-137, 219-227). Migration backfills both sortable columns and creates
  dedicated time indexes (schema.go:660-685); timestamp tests verify index-backed query
  plans. This is already implemented in commit `63db472`, not outstanding review debt.
- Writer admission prefers a waiting exclusive owner over new shared readers. The short
  coordination gate is acquired before the data lock and released immediately after
  admission (writer_lock.go:114-116, 133-205), preventing repeated stats polls from
  indefinitely overtaking telemetry writers while preserving concurrent readers. This is
  already implemented in commit `94f1e0a`, not outstanding review debt.
- Tooling/docs discipline: nearly every script, tool, and doc traced resolves to a live
  consumer; historical material explicitly archived and labeled; CI minimal and pinned;
  mage targets match behavior except `mage fmt` omitting tools/bench/external-identity
  (magefile.go:378); no project-memory marker leaks (decision IDs, milestone IDs, phase
  tokens, acceptance tokens, `agentera` strings) outside `.agentera/` and root
  project-state files.

### Finding ownership and regression baseline

This matrix is the handoff authority for this review. An owner is the plan task that
must resolve or explicitly retain the item. `Reproduced` means the defect was observed
or remains directly present in source. `Verified` means current evidence contradicts
the allegation or supports intentional retention. Source-only observations are not
claims that a user-visible failure was executed.

| Item                  | Owner                                                  | Affected contract                                                         | Current result and evidence                                                                                                                                                                                                                                                                                                                                                                                                                             | Severity    | Intended disposition                                                                                 |
| --------------------- | ------------------------------------------------------ | ------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ---------------------------------------------------------------------------------------------------- |
| 1.1                   | Restore public CLI and dispatch consistency            | `run` help and wrapped-argument ownership                                 | Reproduced with the local build: `run --help` returned help (1,970 stdout bytes), while `run sh --help` returned 0 with empty stdout/stderr.                                                                                                                                                                                                                                                                                                            | High        | Fix and add normal/post-agent help regression cases.                                                 |
| 1.2                   | Restore public CLI and dispatch consistency            | One failure, one diagnostic envelope and exit status                      | Disposable current DB: normal `doctor --format json` returned 0 with one stdout envelope and no stderr. Adverse `doctor unexpected` returned 1 with two stderr lines, each containing an error envelope. Source inspection confirms the same render-and-return pattern in OpenCode init.                                                                                                                                                                | High        | Fix and test one normal and one failing case for each affected command.                              |
| 1.3                   | Correct pipeline and artifact truth                    | Identity transforms create no compressed row                              | Focused runner cases passed: non-empty input emptied by a stage returned the original, while empty input/empty output preserved empty output and recorded both stages in `Ran`. Source inspection establishes the persistence boundary: `len(res.Ran) > 0` sets `transformed` and the hook constructs an artifact candidate. Direct `compress` is inapplicable because that command intentionally always inserts a row; the defect is hook-specific.    | High        | Fix with empty/non-empty identity and row-persistence cases.                                         |
| 1.4                   | Unify database authority and schema diagnostics        | One total first-artifact order across persistence readers                 | Normal `TestDeliveryAuthorityUsesFirstCompressedArtifact` passed. A boundary SQLite query sorted `.12Z` before `.1Z`, opposite chronological order; current selectors also differ on `id` versus `rowid`. No public API accepts competing caller-created artifacts, so direct SQL insertion would bypass rather than test the persistence contract.                                                                                                     | Medium-high | Fix with normal chronology and fractional/equal-time boundary cases.                                 |
| 1.5                   | Make configuration and durable file updates truthful   | Atomic plugin config mutation and malformed-config rejection              | Disposable config: normal `plugin add mycat /bin/cat` returned 0 and created the file. With malformed TOML, adverse `plugin add other /bin/cat` also returned 0 with no stderr and changed the malformed file. Source inspection shows plain `os.WriteFile`; process-kill timing is not used as an atomicity test because it is nondeterministic.                                                                                                       | Medium      | Fix with valid and malformed/interrupted update cases.                                               |
| 1.6                   | Restore public CLI and dispatch consistency            | Documented flags are accepted and unknown flags rejected                  | Partly reproduced. `config show --format json` and documented `--json` both returned JSON successfully, so that subclaim is already satisfied. `plugin list --typo` returned 0 and the same 659-byte output as the normal call, confirming ignored plugin arguments.                                                                                                                                                                                    | Medium      | Retain config compatibility; fix plugin parsing/help and prevent duplicate config work.              |
| 1.7                   | Unify database authority and schema diagnostics        | Read-only inspection applies the common schema gate                       | Disposable current DB: `db report` returned 0, 11,603 stdout bytes, and no stderr. After changing only `user_version` from 15 to 14, it still returned 0, 11,602 stdout bytes, and no stderr. This reproduces the missing schema gate without migration.                                                                                                                                                                                                | Low-medium  | Fix with current-schema normal and old/new-schema adverse cases.                                     |
| 1.8a                  | Unify database authority and schema diagnostics        | Doctor health matches mandatory schema                                    | Disposable current DB: doctor returned 0 with top-level and schema status `ok`. After dropping mandatory `sessions.agent_attribution_authority`, doctor still returned 0 with top-level and `schema_valid` status `ok`.                                                                                                                                                                                                                                 | Low         | Add the mandatory field to shared diagnostic authority and cover present/missing cases.              |
| 1.8b                  | Make configuration and durable file updates truthful   | Price-cache synchronization claim matches the protected operation         | Normal `TestPrices_WriteToDisk` and adverse malformed/absent/valid load cases passed, establishing ordinary file behavior but not concurrency. Source inspection gives the concurrency boundary: the read lock covers marshalling and is released before `os.WriteFile`. A process-kill race is not a valid lock test, and no public synchronization hook exists, so deterministic direct execution is inapplicable without remediation test machinery. | Low         | Correct locking or its claim under the durable-write owner, with deterministic concurrency coverage. |
| 1.9                   | Reconcile benchmark, cache, and tooling validation     | Probe capability claims describe the selected mode                        | With a dummy key and no model call, preflight returned 0 and printed `network=none model_calls=0`. Adverse live invocation without its explicit live guard returned 1 before output or model work. Source inspection confirms the guarded live branch uses `--network bridge`; running it is unsafe here because it requires credentials and paid network model work.                                                                                   | Low         | Correct wording and test preflight/live mode outputs without paid execution.                         |
| 1.10                  | Correct pipeline and artifact truth                    | Fallback provenance describes returned bytes                              | Focused normal cases passed for smaller faithful JSON/NDJSON fallback. The adverse compact-JSON boundary returned original bytes and set anti-worsening rejection when fallback was not smaller. Source inspection confirms this revert does not clear `StructuredFallback`, and `compress` can persist the stale flag; the current test omits that flag assertion.                                                                                     | Low         | Fix with accepted and reverted fallback cases.                                                       |
| 2.1                   | Restore public CLI and dispatch consistency            | Tests and production share dispatch lifecycle                             | Reproduced: twelve `runXWith` seams remain and tests still call them; direct dispatcher tests show the replacement path exists.                                                                                                                                                                                                                                                                                                                         | High        | Consolidate only after caller coverage is established.                                               |
| 2.2                   | Restore public CLI and dispatch consistency            | One bench subcommand registry                                             | Reproduced: the two nine-case switches remain.                                                                                                                                                                                                                                                                                                                                                                                                          | Medium      | Consolidate while preserving normal/unknown-subcommand behavior.                                     |
| 2.3                   | Restore public CLI and dispatch consistency            | `db --path` has one typed owner                                           | Reproduced: parse, string reinjection, and parse again remain.                                                                                                                                                                                                                                                                                                                                                                                          | Medium      | Consolidate with explicit/default path cases.                                                        |
| 2.4                   | Restore public CLI and dispatch consistency            | `run` flags have one ownership model                                      | Reproduced: four overlapping scans remain; item 1.1 demonstrates current drift.                                                                                                                                                                                                                                                                                                                                                                         | Medium      | Consolidate with owned, passthrough, and malformed flag cases.                                       |
| 2.5                   | Unify database authority and schema diagnostics        | Current-schema hot paths avoid legacy probes                              | Reproduced: per-call table introspection and legacy branches remain; production open paths still gate schema.                                                                                                                                                                                                                                                                                                                                           | Medium      | Remove only after supported-caller verification.                                                     |
| 2.6                   | Unify database authority and schema diagnostics        | One schema and migration metadata authority                               | Reproduced: duplicate column truth, identical types, repeated migration loops, and stale file-reference comment remain.                                                                                                                                                                                                                                                                                                                                 | Medium      | Consolidate with ordinary/adverse migration checks.                                                  |
| 2.7                   | Reconcile benchmark, cache, and tooling validation     | Shared preflight invariants and explicit provider selection               | Reproduced: three validators and ceiling walks remain; first matching transport selects validation.                                                                                                                                                                                                                                                                                                                                                     | Medium      | Consolidate with accepted, rejected, and mixed-transport cases.                                      |
| 2.8                   | Restore public CLI and dispatch consistency            | Init has one parse/dispatch implementation                                | Reproduced: `initCommandFunc` and `initLogic` still duplicate the sequence.                                                                                                                                                                                                                                                                                                                                                                             | Medium      | Delegate through one path with normal and invalid-runtime cases.                                     |
| 3.1                   | Make configuration and durable file updates truthful   | Durable writes share safe temp, rename, permission, and failure semantics | Reproduced: divergent helpers and fixed `.tmp` price-cache path remain.                                                                                                                                                                                                                                                                                                                                                                                 | Medium      | Consolidate only compatible policies; test success and rejected/interrupted writes.                  |
| 3.2                   | Correct pipeline and artifact truth                    | JSON and NDJSON compaction have one fidelity contract                     | Reproduced: guard and stage compactors remain parallel implementations.                                                                                                                                                                                                                                                                                                                                                                                 | Medium      | Unify with JSON, NDJSON, blank-record, and trailing-newline cases.                                   |
| 3.3                   | Remove verified residual duplication and dead surfaces | Paired DB getters share scan truth                                        | Reproduced by source inspection for all three listed getter pairs.                                                                                                                                                                                                                                                                                                                                                                                      | Low         | Deduplicate after database correctness work; preserve not-found cases.                               |
| 3.4                   | Remove verified residual duplication and dead surfaces | Flag setup has one maintained definition                                  | Reproduced by the review's referenced command flag blocks.                                                                                                                                                                                                                                                                                                                                                                                              | Low         | Consolidate only exact semantic matches.                                                             |
| 3.5                   | Remove verified residual duplication and dead surfaces | Token estimation uses one maintained rule                                 | Reproduced by the review's referenced implementations; no behavior change tested in this baseline.                                                                                                                                                                                                                                                                                                                                                      | Info        | Verify callers, then consolidate or record why units differ.                                         |
| 3.6                   | Remove verified residual duplication and dead surfaces | Comments and names describe current code                                  | Reproduced by the stale references listed in the finding.                                                                                                                                                                                                                                                                                                                                                                                               | Low         | Correct during owning code changes, without unrelated cleanup.                                       |
| 3.7                   | Reconcile benchmark, cache, and tooling validation     | Benchmark mappings and recipes have one authority                         | Reproduced by the duplicated mappings/recipes; current copies agree.                                                                                                                                                                                                                                                                                                                                                                                    | Low         | Consolidate or mark generated/historical authority; preserve schemas.                                |
| 3.8                   | Remove verified residual duplication and dead surfaces | Exported/dead surfaces retain required compatibility only                 | Reproduced by current caller search for the listed APIs, shims, wrappers, and orphaned docs.                                                                                                                                                                                                                                                                                                                                                            | Low         | Remove only after production and compatibility caller checks.                                        |
| 3.9                   | Remove verified residual duplication and dead surfaces | Intentional wrappers/interfaces have concrete current rationale           | Mixed. Hook-writer delegation is verified intentional because one implementation serves coordinated and held-lease writes. Other listed pass-throughs, help routing, and invalid record-stream coercion remain.                                                                                                                                                                                                                                         | Info        | Retain the persistence delegation rationale; review the rest after correctness tasks.                |
| 4.1 timeout           | Correct pipeline and artifact truth                    | Timeout return and subprocess lifetime                                    | Reproduced by source inspection: caller returns original while stage goroutine can continue.                                                                                                                                                                                                                                                                                                                                                            | Info        | Intentionally retain unless remediation proves cancellation is required; document observed behavior. |
| 4.2 stage copying     | Correct pipeline and artifact truth                    | Stage buffer ownership documentation                                      | Reproduced: runner passes bytes without the copy promised by its comment.                                                                                                                                                                                                                                                                                                                                                                               | Low         | Correct documentation unless behavior changes with regression coverage.                              |
| 4.3 empty-input `Ran` | Correct pipeline and artifact truth                    | Stage execution provenance                                                | Same reproduced root cause and evidence as 1.3.                                                                                                                                                                                                                                                                                                                                                                                                         | High        | Resolve with 1.3, not as duplicate implementation.                                                   |
| 4.4 hook footer gate  | Correct pipeline and artifact truth                    | One recovery-cue delivery/persistence gate                                | Reproduced: first computed envelope is discarded and success rendering rebuilds the condition.                                                                                                                                                                                                                                                                                                                                                          | Medium      | Unify with smaller, equal-size, and larger boundary cases.                                           |

Baseline commands used fresh disposable `HOME` and XDG roots with `KTX_DB` unset:

- `mage build`: passed and produced `build/ktx`.
- Local CLI normal/adverse checks for run help, config JSON, and plugin list: results
  are recorded in items 1.1 and 1.6.
- `go test ./internal/pipeline ./internal/db ./internal/dbreport ./internal/cli`:
  pipeline, db, and dbreport passed. CLI failed in
  `TestConcurrentBenchmarkHooksUseOneExplicitOwnerWithoutProcessFiles` with one
  `SQLITE_BUSY` helper and 5 observed calls instead of 8. This baseline did not alter
  or classify that unrelated failure.

The verified clean-area list above remains outside the finding matrix. In particular,
indexed recent-window statistics and writer-preferred coordination are already complete
and are not owned by a remediation task.

---

## 6. Highest-leverage fixes, ranked

1. Route CLI tests through `runDispatch` and delete the twelve `runXWith` seams
   (removes 2.1, halves 2.2's blast radius).
2. Enforce render-once error contract: convert doctor/init to the bench `commandExit`
   pattern (fixes 1.2).
3. Drop `|| len(res.Ran) > 0` from the transformed decision and gate `Ran` on non-empty
   input (fixes 1.3).
4. Add a sortable timestamp column for `compressed` selection and unify the seven
   first-artifact queries on it (fixes 1.4).
5. Fix `ktx run --help` swallowing and the advertised-but-unparsed flags
   (`config show --json` in README, `plugin list --json` in shipped help) (fixes 1.1,
   1.6).
6. Make plugin config writes actually atomic and stop swallowing config parse errors
   (fixes 1.5).
7. Consolidate before drift compounds: schema truth (2.6), preflight validators (2.7),
   atomic-write helpers (3.1), JSON compaction (3.2).
8. Remove the unreachable legacy-schema branches and their per-call PRAGMA cost (2.5);
   route `db report` through `OpenQuery` (1.7); align doctor's expected schema with
   migration requirements (1.8).
