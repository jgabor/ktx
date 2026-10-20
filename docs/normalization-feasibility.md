# Instruction and tool-schema normalization feasibility

Date: 2026-09-28. Scope: pinned-container infrastructure qualification and
family-specific feasibility, not a normalization implementation or savings claim.

**Current disposition: infrastructure qualification PASS; feasibility assessment
can pass within its bounded evidence scope.** The
[retained-config qualification](#qualification-with-retained-runtime-configuration--2026-09-28)
completed a valid two-attempt pair using the prescribed container, exact model
registration, and `high` variant. Both answers passed, both collector intervals
completed, and local archive validation passed. New-batch retained estimated
cost is **USD 0.001165**; runtime-reported cost is **USD 0**, not proof of free
usage. Four agent attempts have now occurred across the historical failures and
this successful batch; cumulative cost remains unknown.

This is **not a general normalization GO or an architecture decision**.
Instruction normalization still has no evidenced worthwhile general candidate;
the read-description candidate is conditional, and executable application/update
boundaries remain unsupported for both families. The assessment records those
permitted unsupported outcomes rather than claiming observed preservation or
savings. No task/evaluation state is changed.

The earlier zero-call stop, two failed preliminary attempts, exact OAuth-filter
diagnosis, disposable registry recovery, and missing-input handoff below are
dated historical observations. Their stop dispositions are superseded only by
the explicit prerequisite implementation and qualification recorded at the end;
their archives, costs, and original outcomes remain unchanged.

## Correction and pre-call freeze

The earlier qualification observation in
`docs/notes/gpt-6-luna-container-qualification.md` stopped with **zero paid
attempts**. Its demand for a provable hard-dollar ceiling was an added constraint,
not a user requirement. That historical observation is retained; its reason for
blocking paid qualification is superseded here. Finite attempt/time limits and a
price-derived operational stop threshold were authorized. They do not guarantee
a maximum provider invoice.

Frozen before any paid call in this correction:

- At most **two agent attempts total**, one smallest supported exact-match
  baseline/candidate pair; both use passthrough. No warmups, no replacement
  attempts, no automatic benchmark retries, and no additional paid hook probes.
- Only the existing image built from `Dockerfile.benchmark`, OpenCode 1.18.5,
  `--model openai/gpt-6-luna --variant high`. No substitutions.
- `--attempt-timeout 4m30s --termination-grace 30s`: at most ten minutes total
  agent execution plus termination across two attempts. Offline preparation is
  separate. No additional attempt after a known runtime, model, authentication,
  collector, or compatible-estimate failure.
- **USD 1 pinned-price estimated-spend target/stop threshold, NOT a guaranteed
  hard bill cap.** Provider-internal requests/retries and actual billing are not
  bounded by this estimate. A runtime-reported zero does not mean free usage.
  No new spend-enforcement mechanism is introduced.
- Pin the saved third-party catalog record before calling; retain all observed
  terminal failures and costs. Do not inspect credential bodies. Use only the
  existing read-only `--auth` mount and isolated owner databases.

The explicit archive root is
`~/git/ktx/.ktx/experiments`, selected by compare's
`--repo-root ~/git/ktx` and the same working directory for archive
list/show/validate. Fresh HOME and XDG roots do not change this archive location.
The planned experiment ID is `gpt-6-luna-qualification-20260928`.

## Evidence identity reused

No production source changed. The prior successful `mage build` and collector
preflight are reused, not rerun. Hashes were rechecked before the correction:

| Input                  | Identity                                                                                                     |
| ---------------------- | ------------------------------------------------------------------------------------------------------------ |
| Checkout HEAD          | `e285faf9413fe1287dd93022e8befeb2a7d1d257`                                                                   |
| Local `build/ktx`      | SHA-256 `13f1933e9be0dede4e010f7fc6a2c2db4aae171d76cea1c288157ebe0a913c3e`                                   |
| Docker image           | `sha256:7ba62d87f79d46d9bba62388eda2faf46ae4a88bb0122cf438853327e9d262da`                                    |
| Dockerfile             | SHA-256 `0029723c0d446e9c5f3a269a3d841b2496665b70cfa4a13a05b2ec769e2fcbbf`                                   |
| Runtime / collector    | OpenCode `1.18.5` / `opencode-event-v3`                                                                      |
| Generated plugin       | SHA-256 `57d5f3cc78129dbf3112489b83e957226368c16debfa548f8cdfe76af0d0337a`                                   |
| Saved full price cache | SHA-256 `1e17c1b8073c52efb0a6af50136d12b336b8b0d90b7f5117c4abef48b2659d68`, retrieved `2026-09-28T16:17:02Z` |

The saved disposable cache is
`/tmp/ktx-qual-prices.PeOoZM/data/ktx/prices.json`. Its exact OpenAI/Luna record
was transcribed into the supported snapshot format at
`/tmp/opencode/gpt-6-luna-qualification-pricing.json`, preserving both the
272,000-context tier and legacy over-200k rates. USD per million tokens:
input 0.1, output 0.5, cache read 0.01, cache write 0.125; higher-context rates
0.2, 0.75, 0.02, 0.25 respectively. No separate reasoning rate is provided, so
the existing estimator uses the applicable output rate. The third-party catalog
is an estimate basis, not verified billing authority.

## Executed qualification

The focused compare help confirmed semantic IDs; the local CLI's flag
registration and runner confirmed the additional supported flags. Exact-match
rejects an explicit `--warmup`; omitting it means no warmups. The two-attempt
budget permits one baseline and one candidate in the first cycle, with no
budget remaining for benchmark retries. It is not a claim that provider-internal
retries are disabled. Compare performs its own mandatory capability inspection;
no extra standalone preflight or image build was run in this correction.

Working directory for every command below: `~/git/ktx`.
The one paid-capable invocation was:

```bash
root=$(mktemp -d /tmp/opencode/ktx-correction-compare.XXXXXX)
env -u KTX_DB -u KTX_PRICES -u KTX_BIN -u DOCKER_BIN \
  HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" \
  ./build/ktx bench compare \
  --spec exact-match --model openai/gpt-6-luna --variant high \
  --task grep-noise --baseline passthrough --candidate passthrough \
  --target-valid 1 --max-attempts 2 \
  --attempt-timeout 4m30s --termination-grace 30s \
  --auth ~/.local/share/opencode/auth.json \
  --image sha256:7ba62d87f79d46d9bba62388eda2faf46ae4a88bb0122cf438853327e9d262da \
  --repo-root ~/git/ktx \
  --experiment-id gpt-6-luna-qualification-20260928 \
  --pricing-snapshot /tmp/opencode/gpt-6-luna-qualification-pricing.json \
  --pricing-sha256 99fbdcbf38233c00f574dd03469849914414babf654aee3a49d3776c9fa6c6cf \
  --format json
```

The existing auth file was only tested for readability and mounted read-only;
its body was not inspected, printed, or copied. Only the benchmark's fresh,
explicit owner database was opened. The registered task mounts synthetic corpus
content, asks for a single planted fact, and requires at least one tool call.
The required image, model, variant, timeout, grace, and pricing identities are
retained in the manifest; configured model/variant are not observed model-step
identity.

### Terminal result and costs

| Observation                             | Result                                                                                                                                                              |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Agent attempts started                  | **1**, `baseline-001`; no candidate directory, second attempt, retry, or paid hook probe                                                                            |
| Owner interval                          | `2026-09-28T16:25:24Z` to `2026-09-28T16:25:32.080171515Z`, approximately **8.08 seconds**; this is the retained interval, not separately measured provider latency |
| Runtime exit                            | **1**, retained interval reason `opencode exit 1`                                                                                                                   |
| Runtime stream                          | One `error` event: `UnknownError`, `Unexpected server error. Check server logs for details.`, reference `err_2b159367`                                              |
| Runtime stderr                          | Empty (0 bytes)                                                                                                                                                     |
| Collector                               | One epoch, five events: one `collector_open`, four `other`; zero model steps; zero contradictions; close state **incomplete**                                       |
| Tool calls                              | 0                                                                                                                                                                   |
| Compare                                 | Failed: `attempt "baseline-001" interval ownership or completion mismatch`                                                                                          |
| Runtime-reported cost                   | **Unavailable**, not USD 0                                                                                                                                          |
| Price-derived attempt cost              | **Unavailable**: no complete token usage/model steps to estimate                                                                                                    |
| Actual provider charges / request count | **Unknown**; absence of model-step evidence does not prove no billable work                                                                                         |
| Remaining owned containers              | None reported by `docker ps --all --filter name=ktx-gpt-6-luna-qualification-20260928 --format '{{.Names}} {{.Status}}'`                                            |

Thus the authorized paid-capable attempt count is exactly **one of two**. The
USD 1 estimated-spend threshold cannot be evaluated from this failed attempt;
it is not recorded as satisfied. The unavailable estimate and incomplete
collector are stop conditions. **No further paid calls were made.** There is no
evidence of excessive spend, but also no defensible numeric bill or estimated
cost for the failed attempt.

The proximate failure is runtime exit followed by fail-closed collector
rejection, not a stale executable or a successful lifecycle mistaken for
completion. The matched binary/image identities rule out accidental use of an
older local build. The retained generic server error does **not** identify
whether model support, authentication, provider service, or another runtime
subsystem caused it. Server-side logs were not retained in the archive; no
credential inspection or diagnostic paid rerun was attempted. Do not label this
an authentication failure or permanent model incompatibility without evidence.

### Archive checks and preservation

Retained archive ID: **`gpt-6-luna-qualification-20260928`**.
Absolute root:
`~/git/ktx/.ktx/experiments/gpt-6-luna-qualification-20260928`.
Each of these local-binary invocations used a separately allocated disposable
HOME and all five HOME/XDG locations shown above, with `KTX_DB` unset:

| Command                                                                      | Result                                                        |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------- |
| `./build/ktx bench list --format json`                                       | Found the ID, status `ok` (manifest discovery only)           |
| `./build/ktx bench show gpt-6-luna-qualification-20260928 --format json`     | Returned manifest with required identities and frozen limits  |
| `./build/ktx bench validate --format json gpt-6-luna-qualification-20260928` | **Failed**: `attempts.jsonl must contain experiment evidence` |

The runner rejected the incomplete interval before appending `attempts.jsonl`.
That file and `summary.json` are absent; validation failure is expected and is
not repaired by inventing an attempt record. The raw database, stream, stderr,
owner-session metadata, manifest, pinned pricing, configurations, inputs, and
repository snapshot remain intact. No summarize/rewrite was performed.

A disposable read-only diagnostic (`sqlite3` URI `mode=ro`,
`PRAGMA query_only=ON`) inspected only this attempt's owner database. It
confirmed the interval/event counts above. A structural equality check also
confirmed that retained `pricing.json.price` exactly equals the saved full
catalog's `openai.gpt-6-luna` record. Useful SHA-256 identities:

| Retained file                        | SHA-256                                                            |
| ------------------------------------ | ------------------------------------------------------------------ |
| `manifest.json`                      | `74d04a3426b4a0fe113634e1bcca4a37c453609074abf01bd9880baa7e30193e` |
| `pricing.json`                       | `99fbdcbf38233c00f574dd03469849914414babf654aee3a49d3776c9fa6c6cf` |
| `attempts/baseline-001/ktx.db`       | `d42118a87387c6f70fc3bf38f919f55d0d8607b479b61546375fca2add8fc03f` |
| `attempts/baseline-001/stream.jsonl` | `f9f52e5fd77b0f65219636524d57d9d3b830da2594c4dfa5199f8a98351c6cb5` |

The archive is sensitive local evidence and remains ignored by Git, as intended
for experiment data. **This report is outside the ignored `docs/notes/` tree**
and is the trackable, sanitized authority for this correction. The earlier
ignored report retains its zero-call history with a supersession note.

## Existing evidence: facts, unexamined records, and gaps

| Evidence class                                                           | What it supports                                                                             | What it does not support                                                                               |
| ------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| Verified earlier build/preflight and rechecked hashes                    | Matching local executable, pinned image, generated plugin, declared collector protocol       | Actual model authorization or successful model-step collection                                         |
| Fresh saved public pricing and retained snapshot                         | Reproducible keyed estimate basis, including cache/context rates                             | Provider invoice, actual token counts, or a hard-dollar ceiling                                        |
| This failed qualification                                                | A real runtime start, collector opening, incomplete terminal failure, retained raw artifacts | Valid comparison, successful Luna/high execution, complete cost estimate, or normalization feasibility |
| Pinned runtime source analysis below                                     | Declared mutation hooks, ordering, downstream conversion, and source-text duplication        | Executed hook mutations, update visibility, final provider bytes, or measured savings                  |
| Tiny retained synthetic probe configuration                              | Sanitized example of overlapping tool prohibition and disabled tools                         | A worthwhile general instruction transformation or behavior preservation after removing wording        |
| Historical cache/prefix/telemetry reports and larger user-owned archives | Background only; not freshly revalidated or exhaustively examined here                       | Evidence of instruction/schema normalization gains or present-version qualification                    |

No private prompts, credentials, ambient database, or larger personal instruction
corpus were inspected. The prior analyzer ran no executable hook probes. This
correction ran infrastructure qualification only; its passthrough arms do not
test either transformation family. No token-overhead estimate is manufactured
from duplicated source wording.

## Pinned application boundaries

Source authority is OpenCode **1.18.5**, commit
`e5cc278dec9294a627a7b05f47ce6a564408c1a2`. Current documentation corroborates
hook names but is not proof of this pinned implementation. Hook-visible objects
are not provider request bytes.

### Instructions

- [`session/llm/request.ts`, lines 56–112](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/session/llm/request.ts#L56-L112)
  invokes `experimental.chat.system.transform` with mutable `system: string[]`
  after agent, environment, instruction, and user system material is assembled.
- The OpenAI OAuth path uses `system.join("\n")` as instructions; other paths
  supply system messages. Assembly and downstream mapping therefore matter;
  editing a source instruction file is not proof of the submitted representation.
- **Application/update outcome: unsupported by executable evidence here.** No
  changed instruction content was observed through this hook or a downstream
  request boundary. No source/config update was demonstrated to invalidate a
  previously normalized representation. This does not prove the hook is absent.

### Tool definitions and schemas

- [`tool/registry.ts`, lines 305–334](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/tool/registry.ts#L305-L334)
  invokes `tool.definition` with description, parameters, and optional JSON
  schema. This is an actual declared extension point, not an arbitrary source
  substitution.
- [`session/tools.ts`, lines 92–102](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/session/tools.ts#L92-L102)
  subsequently adds runtime description content, converts schema, and calls
  [`ProviderTransform.schema`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/provider/transform.ts#L1489-L1511).
- [`session/llm/request.ts`, lines 148–184](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/session/llm/request.ts#L148-L184)
  filters and sorts tool names. Runtime-owned ordering and schema transformation
  make a generic JSON-order candidate unjustified.
- **Application/update outcome: unsupported by executable evidence here.**
  Description mutation is source-supported, but final post-transform schema,
  surviving parameter descriptions, application, and changed-content visibility
  were not observed in the pinned image.

The pinned [`debug agent` implementation](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/cli/cmd/debug/agent.handler.ts)
was checked as a possible offline seam. It resolves registry tools but prints
agent configuration and a tool-enabled map (or executes a selected tool); it
does not establish the assembled instruction hook or final request-schema
boundary. An instrumented registry-only observation would still not establish
the required downstream application/update visibility. No substitute/emulated
request, patched runtime, or general benchmark feature was built. With the
substantive qualification failure, no further probe setup/testing was expanded.

## Sanitized transformation candidates

### Instruction family: no worthwhile general candidate established

The retained [synthetic configuration](../tools/bench/evidence/opencode-runtime-cache-controls/probe-config.json)
contains:

```json
{
  "prompt": "Do not use tools. Follow the final synthetic instruction exactly.",
  "tools": { "*": false }
}
```

Removing `Do not use tools.` leaves a shorter prompt while the synthetic agent's
tools remain disabled. This is only a **synthetic control**, not evidence of a
useful general-purpose normalizer. Tool availability and model instructions have
different semantics: removing the prohibition may change planning, attempted
tool use, or how the model responds when tools are unavailable. Repetition may
also carry emphasis. No correctness or cost benefit was measured. Instructions
from different precedence levels cannot safely be deduplicated merely because
their text resembles one another.

Any future representation would have to be invalidated when source instruction
content, agent prompt/configuration, tool availability, or permissions change;
otherwise removing text on the basis of a stale disabled-tools setting could
become behavior-changing. No such invalidation was implemented or observed.
**Family result: unresolved / no-go for a general instruction candidate.**

### Schema family: conditional read-tool description candidate

The pinned [`tool/read.txt`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/tool/read.txt)
contains these three top-level usage bullets:

```text
- The filePath parameter should be an absolute path.
- By default, this tool returns up to 2000 lines from the start of the file.
- The offset parameter is the line number to start from (1-indexed).
```

The corresponding fields in pinned
[`tool/read.ts`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/tool/read.ts)
already describe the absolute path, 1-indexed offset, and default 2000-line
limit. A narrow candidate could remove **only those three bullets**, retaining
all parameter descriptions and all other usage guidance. It would change
description text, not validators, defaults, parameter names, requiredness, or
tool execution behavior.

The potential benefit is less repeated descriptive text if both copies survive
to the model. The hazards are loss of emphasis, changed tool-call behavior, and
the possibility that downstream schema conversion omits the parameter wording
on which the deletion relies. Source duplication alone is neither measured
prompt overhead nor savings. The original description must remain recoverable
for any future test; changed tool source/schema, plugin composition, runtime,
provider transformation, or relevant config must invalidate the representation.
None of that application/update preservation was executed here.
**Family result: plausible conditional description candidate, not qualified.**

No JSON key-order candidate is selected: the runtime already owns tool sorting
and provider-specific schema conversion. No new instruction candidate is forced
merely to match the schema family.

## Coverage and remaining blockers

| Required criterion                                                             | Coverage / unresolved portion                                                                                                                                                                                                     |
| ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Distinguish facts, unexamined records, and evidence gaps                       | Covered by the evidence inventory; no historical report is promoted to current runtime observation                                                                                                                                |
| Sanitized candidates, benefits, hazards, runtime redundancy                    | Covered for the small instruction control and conditional read-description candidate; worthwhile general instruction benefit remains unestablished                                                                                |
| Each family's application and update boundary                                  | Pinned source boundaries identified; **executable application/update visibility unsupported for both**, especially final post-transform schema                                                                                    |
| Required model/container/collector/pricing qualification before implementation | Matching image/runtime and pinned catalog established; **gate failed** on the first real attempt, with incomplete collection and unavailable usage/cost. Model authorization and successful Luna/high behavior remain unqualified |
| Stop dependent work on infeasibility                                           | **NO-GO** recorded; no dependent implementation, normalizer, comparison campaign, dependency/runtime substitution, or extra paid call performed; architecture remains unresolved                                                  |

This is sufficient to reject a total GO, not to declare all planned work
complete or prove permanent infeasibility. Replanning must address the failed
pinned-runtime execution/retained diagnostics and whether a supported boundary
can demonstrate applied content and updates for each family. A worthwhile
instruction candidate is still missing independently of runtime qualification.
Any new paid diagnostic or implementation is outside this stopped correction.

Verification coverage is native Linux `7.2.7-1-cachyos`, x86_64, and the existing
Docker benchmark image (earlier environment: Docker 29.8.1, Mage 1.17.2). No
non-Linux qualification is claimed. Existing passing build/preflight evidence
was reused; this docs-only correction did not change product behavior or justify
a full test-suite rerun. Final SHA-256 checks confirmed the executable,
Dockerfile, and the retained files listed above remained unchanged. Git's
whitespace checks emitted no diagnostics; `git status --short` exposed this
report as untracked and `git check-ignore` confirmed it is not ignored. No state
completion/evaluation, commits, pushes, dependency upgrades, or credential
inspection were performed.

## Bounded diagnostic recovery — 2026-09-28

This separate, subsequently authorized diagnostic consumes the **second and
final** preliminary agent attempt. The qualification results and one-attempt
counts above describe the earlier stopped observation, not a reset budget.
The original failed archive must remain unchanged. The task remains blocked;
no qualification pair or dependent implementation is authorized here.

Before execution, the existing image ID and entrypoint were rechecked. Pinned
[`observability/logging.ts`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/core/src/observability/logging.ts)
supports `OPENCODE_PRINT_LOGS=1` and `OPENCODE_LOG_LEVEL=ERROR`; pinned
[`src/index.ts`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/index.ts)
preserves those environment settings unless explicit CLI flags override them.
The existing `ktx bench agent` entrypoint inherits the environment and forwards
OpenCode stderr. Thus error logging can be enabled **without** replacing the
entrypoint, modifying the runner, or patching the runtime.

Frozen diagnostic differences and limits:

- One invocation of the same image ID, `--model openai/gpt-6-luna --variant high`,
  same exact sanitized manifest prompt, and a byte-identical copy of the
  baseline's synthetic corpus and passthrough configuration.
- Enable only the supported error-level logging environment. Capture raw stream
  and stderr privately beneath `/tmp/opencode/ktx-luna-diagnostic-20260928`
  (private parent, output files mode 0600); do not print raw logs.
- Preserve the image's `ktx bench agent` entrypoint, but invoke it directly
  rather than via compare. No required-owner/interval environment or archived
  database is attached; runtime state belongs to this fresh disposable
  container. This is **diagnostic-only**, not full collector verification or
  benchmark qualification.
- Mount the existing auth file read-only, and only copied synthetic work/config
  files. No credential body is read, printed, or copied by diagnostic tooling.
- Host HOME/XDG roots are newly allocated per invocation with `KTX_DB` unset;
  container HOME/XDG paths use the fresh image filesystem. Timeout is 270 seconds
  with up to 30 seconds for termination, reserving the end of that grace for
  forceful cleanup. No retry, extra pair, model/runtime substitution, or upgrade.
- USD 1 remains the **cumulative estimated-spend target**, not a hard bill cap.
  The first attempt's cost is unknown; this diagnostic does not erase that
  uncertainty. Any additional paid run requires separately planned recovery.

### Executed command and retained authority

The disposable supervisor `/tmp/opencode/run-luna-diagnostic.py` checked the
manifest identities and exact prompt, copied only the synthetic corpus,
passthrough config, and pricing snapshot, recorded input hashes, and invoked
Docker exactly once. It waited at most 270 seconds, reserving 30 seconds for
termination (25-second Docker stop followed by forceful cleanup if needed).
The timer was not reached. It captured stdout/stderr directly into private
files, removed its named container, and checked the original evidence hashes.

The supervisor was invoked from `~/git/ktx` as:

```bash
root=$(mktemp -d /tmp/opencode/ktx-recovery-invoke.XXXXXX)
env -u KTX_DB HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" python /tmp/opencode/run-luna-diagnostic.py
```

Its supervised Docker argv was equivalent to the following (the task prompt
contains exactly one newline; do not rerun this outside a newly authorized
budget or without the supervisor):

```bash
diagnostic=/tmp/opencode/ktx-luna-diagnostic-20260928
docker run --name ktx-luna-diagnostic-20260928-final \
  -e OPENCODE_PRINT_LOGS=1 -e OPENCODE_LOG_LEVEL=ERROR \
  -e 'KTX_EXPECTED_STAGES_JSON=[]' \
  -e HOME=/home/node -e XDG_DATA_HOME=/home/node/.local/share \
  -e XDG_CONFIG_HOME=/home/node/.config -e XDG_CACHE_HOME=/home/node/.cache \
  -e XDG_STATE_HOME=/home/node/.local/state \
  -v ~/.local/share/opencode/auth.json:/home/node/.local/share/opencode/auth.json:ro \
  -v "$diagnostic/passthrough.toml:/home/node/.config/ktx/ktx.toml:ro" \
  -v "$diagnostic/workspace:/work:rw" \
  sha256:7ba62d87f79d46d9bba62388eda2faf46ae4a88bb0122cf438853327e9d262da \
  --model openai/gpt-6-luna --variant high \
  $'Search tools/bench/corpus/search_hits.txt for the planted FACT_GREP_NEEDLE line.\nYou may use grep or read. Reply with only that single FACT_* line, no other prose.'
```

The trackable [sanitized diagnostic artifact](normalization-diagnostic-20260928.json)
records the input/image identities, safe error excerpt, selected runtime stack
paths, outcome, costs, and raw-output hashes. Private `inputs.json`,
`receipt.json`, `stream.jsonl`, `stderr.log`, and `sanitized.json` remain beneath
the diagnostic root. Only allowlisted error classifications, the exact
requested model name, bundled runtime stack paths, numeric counts, and hashes
were exposed from raw logs. No full raw log, request body, credential, or private
user history was printed or published.

### Diagnostic result

| Observation                       | Result                                                                                                                                                                             |
| --------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Preliminary attempts consumed     | **2 total**: original `baseline-001` plus this one diagnostic; **0 remaining**, no retries                                                                                         |
| Diagnostic time                   | `2026-09-28T17:16:29.122919Z`–`17:16:38.681481Z`; **9.559 seconds**, including cleanup; no timeout                                                                                 |
| Runtime/entrypoint exit           | **1**                                                                                                                                                                              |
| Scoped server error               | **`ProviderModelNotFoundError`**, safe excerpt **`Model not found: openai/gpt-6-luna`**                                                                                            |
| Wrapper stream                    | One error event; **zero model-step events**                                                                                                                                        |
| Raw stderr                        | 1,439 bytes, privately retained; unlike the empty stderr of the original attempt                                                                                                   |
| Reported and price-derived cost   | Both **unavailable**, not zero: no model-step token usage                                                                                                                          |
| Cumulative estimated/actual spend | **Unknown** for both attempts; the USD 1 target is not shown to be satisfied and is not a hard bill cap                                                                            |
| Cleanup                           | `docker rm --force` exited 0; subsequent named-container check returned none                                                                                                       |
| Original evidence                 | Before/after hashes match for the manifest, pricing, baseline config, owner DB, stream, stderr, both session snapshots, and corpus; original archive was never mounted or modified |

This diagnostic found a concrete **runtime provider/model-resolution failure**
for the requested identifier. It did not merely reproduce an incomplete
collector. Required collector mode was absent here, so that mode is not needed
to produce this diagnostic failure. The original collector's incomplete state
remains a consequence of runtime exit; its unretained server error cannot be
retroactively assigned this exact cause with certainty.

Pinned [`provider/provider.ts`, lines 1094–1103](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/provider/provider.ts#L1094-L1103)
defines this error and message. Its
[`getModel` and `getLanguage` branches](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/provider/provider.ts#L1806-L1858)
can raise it for an unavailable provider, an absent model entry, or an SDK
model lookup failure. The retained bundled stack does not identify which of
those branches applied. A price-catalog entry alone cannot establish runtime
provider/model availability. This is **not evidence of a provider API rejection,
invalid credentials, valid credentials, or successful `high` variant support**.

### Smallest supported recovery boundary and remaining gaps

The immediate observability correction succeeded: enabling the pinned runtime's
supported stderr/error logging retained the missing cause without changing
product code. It did **not** correct model availability.

If subsequent non-secret diagnosis establishes that only a model registration
is absent, the narrow configuration route supported by pinned
[`provider/provider.ts`, lines 1419–1514](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/provider/provider.ts#L1419-L1514)
is an explicit `provider.openai.models` entry for the **same exact model/API
identifier**, with verified capability, limit, and variant metadata. Such an
entry is not a credential fix or proof of provider authorization, and it must
survive the runtime's auth-loader/filtering path. The observed error does not
yet prove that registration is the missing component. No speculative model
entry, alias, backend routing, credential change, runtime patch, or disposable
"fix" was therefore applied. Correct metadata cannot be inferred from prices.

The failure class is located; the exact registration/provider/SDK cause and a
proven compatible correction remain unresolved. **Stop here for separately
planned recovery.** There is no allowance for another paid diagnostic or a new
qualification pair. The original archive remains a validation **FAIL**, and
this diagnostic is not an experiment archive or collector qualification.
Successful model/variant execution, complete cost evidence, the required
collector and pair, and both families' application/update evidence remain
mandatory gaps. No task completion or downstream progress is claimed.

Checks were limited to pinned-source/logging review, existing image/input
identities, disposable-script syntax, private-log classification, original
evidence hashes, cleanup, sanitized-artifact consistency, and whitespace.
No successful build/preflight was repeated, no product behavior changed, and
no full test suite was run. No state lifecycle/evaluation writes, dependency
upgrades, model substitutions, commits, pushes, or delegation occurred.

## Container registry recovery — 2026-09-28

This continuation was explicitly authorized to investigate and recover
autonomously. The two earlier failed attempts are a **historical batch**, not a
permanent spending or permission veto. A new qualification batch was authorized
for at most two attempts, one tiny pair without retries, at most ten minutes
of agent execution/termination, and a USD 1 estimated-spend target rather than a
hard bill cap. **No generating call was needed or made in this continuation.**
The new batch was not started because of the concrete runtime-config input gap
below; the earlier costs remain unknown and were not reset to zero.

The user's successful host `opencode --model openai/gpt-6-luna --variant high
run hi` is accepted as host-availability evidence, not container parity. It was
not rerun, and no host config, credential body, private history, ambient database,
or earlier owner database was inspected. Existing build/preflight evidence was
reused; local binary and Dockerfile hashes still match the identities above.

### Exact cause: pinned OAuth model-name filtering

The actual image's `opencode models --help` confirms `models [provider]`,
`--verbose`, and `--refresh`. Pinned
[`models.ts`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/cli/cmd/models.ts)
implements provider inventory, not generation. With the existing read-only auth
mount, the OpenAI provider **is available**, but its inventory excludes
`gpt-6-luna`. This rules out the earlier missing-provider speculation in this
environment.

The pinned built-in
[`plugin/openai/codex.ts`, lines 280–292](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/plugin/openai/codex.ts#L280-L292)
filters models when the runtime selects OAuth authentication. After its explicit
allow/deny checks, it applies:

```typescript
const match = model.api.id.match(/^gpt-(\d+\.\d+)/);
return match ? parseFloat(match[1]) > 5.4 : false;
```

`gpt-6-luna` is not in that explicit allowlist and **does not match a decimal
version**. It is removed, although names such as `gpt-5.6-luna` pass. This is a
local registry filter, not a provider service rejection or a conclusion drawn
from price availability. No alternate model name or API alias was used.

### Non-generating runtime observations

All rows used the **same immutable benchmark image** and `opencode models
openai --verbose`. Each had fresh disposable host HOME/XDG roots with `KTX_DB`
unset and a fresh container filesystem. Every invocation exited 0 and its named
container was removed successfully. Raw output was captured privately; only
model IDs and selected capability metadata were published.

| Case                               | Inputs varied                                                                                                | OpenAI models | Exact target visible |
| ---------------------------------- | ------------------------------------------------------------------------------------------------------------ | ------------: | -------------------- |
| Baseline                           | Existing read-only auth; image defaults                                                                      |            13 | No                   |
| Refresh                            | Same inputs plus supported `--refresh`; reported `Models cache refreshed`                                    |            13 | No                   |
| Frozen catalog, no auth            | Current public catalog; empty OpenAI provider declaration to expose unauthenticated inventory; no auth mount |            57 | **Yes**              |
| Frozen catalog, auth               | Same public catalog; existing read-only auth                                                                 |            13 | No                   |
| Registered target                  | Same catalog/auth plus explicit exact-model config                                                           |            14 | **Yes**              |
| Registered target, bundled catalog | Exact-model config/auth, no catalog override                                                                 |            14 | **Yes**              |

The last four cases used `--network none` and
`OPENCODE_DISABLE_MODELS_FETCH=1`. Thus the registration result cannot depend on
a model call, fresh authorization, or a background network refresh. The frozen
catalog cases additionally mounted a public snapshot through supported
`OPENCODE_MODELS_PATH`. The no-auth case used only
`{"provider":{"openai":{}}}` to make that provider visible; it did not invent a
model or change authentication. The auth-dependent disappearance, pinned filter,
and post-filter registration together locate the registry failure precisely.

The refresh message is not treated as proof of target availability. Pinned
[`models-dev.ts`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/core/src/models-dev.ts#L178-L251)
can use disk data or its compiled snapshot and refresh asynchronously; the
explicit frozen-catalog cases remove that uncertainty. Refresh alone cannot fix
the name filter. The public snapshot contains the target and was retained with
SHA-256 `b65d346bd912f6d940286af06a127eefe965c5c5fed468476f87a54b0b031605`.
A direct Python public-catalog fetch returned HTTP 403; the web retrieval tool
successfully fetched the same public endpoint. No private source or credentials
were used to obtain it.

### Supported disposable correction, verified at the registry boundary

Pinned
[`provider/provider.ts`, lines 1392–1433](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/provider/provider.ts#L1392-L1433)
applies plugin model filtering **before** extending the registry from explicit
provider model configuration. Therefore supported `OPENCODE_CONFIG` can register
the exact target after that filter, without altering the runtime or credentials.

The applied disposable file is retained as the
[model-registration fixture](normalization-model-registration.json), SHA-256
`7d6e7ec5f4ad84f017793cd80c41fa707fe3e72efbf8702fb053ae12f7f5d827`.
It is documentation evidence, **not a project/host default**. Its provider and API
ID remain `openai` / `gpt-6-luna`, using the existing bundled `@ai-sdk/openai`.
No alternative endpoint, auth material, alias, external plugin, or SDK install
was introduced. Pinned OpenAI loading uses `sdk.responses(modelID)`.

The [official OpenAI model page](https://developers.openai.com/api/docs/models/gpt-6-luna)
confirms the exact API ID, 1,050,000-token context, 922,000 maximum input,
128,000 maximum output, reasoning, text/image input, text output, Responses API
function calling, and `high` reasoning effort. The public runtime catalog
corroborates limits, reasoning, tool calls, and effort values. The fixture uses
the official text/image modalities rather than additionally enabling the
catalog's PDF convenience capability. It does not override runtime cost with a
billing assertion; any future estimate must still use the retained pricing
snapshot and observed usage independently of reported cost.

Both corrected inventory runs returned the exact API ID and limits, reasoning
and tool-call support, and this resolved variant:

```json
{
  "reasoningEffort": "high",
  "reasoningSummary": "auto",
  "include": ["reasoning.encrypted_content"]
}
```

The final bundled-catalog run proves that **only the small configuration file
is needed to restore registry visibility**; a fetched catalog is not a required
execution input. This is runtime-observed registry recovery, not successful
generation, provider authorization, full collector verification, or measured
normalization benefit.

The effective command for that final non-generating verification was:

```bash
docker run --name ktx-model-inventory-registered-bundled-20260928 \
  --entrypoint opencode --network none \
  -e OPENCODE_PRINT_LOGS=1 -e OPENCODE_LOG_LEVEL=ERROR \
  -e OPENCODE_DISABLE_MODELS_FETCH=1 \
  -e OPENCODE_CONFIG=/probe/exact-model.json \
  -v ~/.local/share/opencode/auth.json:/home/node/.local/share/opencode/auth.json:ro \
  -v /tmp/opencode/ktx-container-registry-20260928/exact-model.json:/probe/exact-model.json:ro \
  sha256:7ba62d87f79d46d9bba62388eda2faf46ae4a88bb0122cf438853327e9d262da \
  models openai --verbose
```

The disposable supervisor allocated a fresh `env -u KTX_DB HOME=... XDG_*=...`
host environment, captured private stdout/stderr, bounded each inventory to
90 seconds, and ran `docker rm --force` afterward. Full safe command receipts
remain under `/tmp/opencode/ktx-container-registry-20260928/<case>/sanitized.json`.
The [trackable inventory artifact](normalization-model-inventory-20260928.json)
contains the case results, selected recovered metadata, source/input identities,
and cost/qualification boundaries.

### Exact qualification seam and bounded candidate change

Current compare cannot carry that configuration into the required corpus-task
container:

- `internal/cli/bench_experiment.go:253–284` has no OpenCode runtime-config input.
- `internal/experiment/runner.go:873–903` mounts auth, the **ktx pipeline** config,
  an isolated owner database, and the corpus workspace. Host
  `OPENCODE_CONFIG`/`OPENCODE_CONFIG_CONTENT` are not forwarded to Docker.
- Its one internal `OPENCODE_CONFIG_CONTENT` branch is for cache isolation;
  `cache_control.go:34–58` declares that capability unsupported. It is not a
  general config interface and exact-match does not accept cache-control flags.
- `files.go:140–162` builds the selected corpus workspace from registered `.txt`
  fixtures only. Adding an OpenCode config to the host checkout does not mount
  it. Rewriting the registered task into repository mode would change the
  experiment input contract, not supply the missing config through a supported
  runtime-config input.

**Smallest candidate product change, not implemented here:** one explicit
OpenCode configuration-file input to compare (for example a _proposed_, currently
unsupported `--opencode-config` flag). Capture its bytes once, retain its path
and SHA-256 in the experiment, mount that retained file read-only for both arms,
and set the container's supported `OPENCODE_CONFIG` to that path. Archive
validation must verify the retained/executed config identity. Focused tests
would cover CLI input, single capture, mount/environment, and missing/modified
retained configuration. This needs neither a generic environment-injection
framework nor a runtime/model change.

That is the concrete reason qualification cannot proceed within this turn's
**no production changes** boundary. A Docker wrapper, modified task, derived
image, or bare hello would not constitute the requested unchanged-task,
retained-input qualification, so none was substituted. No unsupported flag was
passed and no collector was bypassed to claim a pass.

### Outcome and remaining gates

- **Exact local cause identified; disposable registry fix applied and observed.**
- **New generating/paid attempts: 0.** The two historical failed attempts and
  their unavailable reported/estimated costs remain separate. Cumulative model
  spend is still **unknown**, not zero or proven below USD 1.
- **Qualification remains FAIL** for the original archive; no new archive or
  passing pair exists. No old database was opened or revalidated in this
  continuation, and the original archive/private diagnostic logs were untouched.
- After the bounded runtime-config input change, the authorized pair still
  needs its limits frozen before calls, real model-step/collector/cost evidence,
  and local archive validation. Registry visibility alone does not settle those
  gates. Instruction/schema feasibility findings remain unchanged.

No further user approval question is implied for already authorized diagnosis
or same-model recovery. The handoff is the exact product-input gap above, under
the explicit prohibition on implementing product changes here. No lifecycle or
evaluation state was written and no task completion is claimed. Verification
was limited to non-generating runtime inventory, pinned-source/official-model
metadata checks, input identities, cleanup, report consistency, and whitespace;
there was no rebuild, repeated preflight, full test run, commit, or delegation.

## Qualification with retained runtime configuration — 2026-09-28

The previously identified input gap has now been implemented and independently
audited. Current `bench compare --runtime-config` captures one restricted,
self-contained model-registration JSON input, retains its bytes/SHA-256, mounts
it read-only for both arms through `OPENCODE_CONFIG`, and validates that identity
with the archive. Opt-in auth is checked and staged in a private temporary copy,
mounted read-only and removed when compare returns. No credentials are inspected
or printed by this qualification work. No additional product changes are made
in this section.

The prerequisite's passing full `mage check` evidence at
`/tmp/opencode/ktx-runtime-check.vYiko3/check.log` is reused. It includes native
tests, 14 Python verifier tests, generated-adapter overlap, zero lint issues, and
18,177 audited verification operations with zero forbidden database operations.
The changed local binary required a **new image build**, not reuse of the stale
image from the earlier failure.

### Limits frozen before paid calls

- One new minimal `exact-match` / `grep-noise` comparison, passthrough versus
  passthrough, `--target-valid 1 --max-attempts 2`: **at most two new agent
  attempts**, no warmups and no benchmark retries or replacement attempts.
- Exact `--model openai/gpt-6-luna --variant high`, with the unchanged verified
  `docs/normalization-model-registration.json`; no model alias, runtime upgrade,
  host generation, credential mutation, or collector bypass.
- `--attempt-timeout 4m30s --termination-grace 30s` per attempt: at most ten
  minutes total agent execution/termination. Build and offline checks are
  separate. Stop on incompatible runtime/model/auth/collector or unusable usage
  and pricing evidence; preserve failures.
- **USD 1 new-batch price-derived estimated-spend target/stop threshold, not a
  guaranteed hard provider bill cap.** The two historical failed attempts and
  unknown prior charges remain separate; cumulative charges are not reset.
- Unique new archive ID: `gpt-6-luna-qualification-runtime-config-20260928`, under
  `~/git/ktx/.ktx/experiments`. Existing failed archives are not opened
  or overwritten.

### Rebuild/preflight and frozen identities

Current documentation and flag registration support `--pricing-snapshot` and
`--pricing-sha256`, **not `--pricing-file`**. Those supported spellings are used
for the already verified matching provider/model record; no fresh price fetch
was needed. The source timestamp, context tier, and original price provenance
are preserved.

| Input                | Identity                                                                                                                            |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| Local `build/ktx`    | SHA-256 `e7378f23ea4e431a2d50f4b810ae87ee3b67bad4ca167c37d08013787310eb38`                                                          |
| Rebuilt image        | `sha256:9994d6b408eb6c00849d5e70efc932f41be67b48abff34d57c04edc771476742`                                                           |
| Dockerfile           | SHA-256 `0029723c0d446e9c5f3a269a3d841b2496665b70cfa4a13a05b2ec769e2fcbbf`                                                          |
| Runtime / collector  | OpenCode `1.18.5` / `opencode-event-v3`                                                                                             |
| Generated plugin     | SHA-256 `57d5f3cc78129dbf3112489b83e957226368c16debfa548f8cdfe76af0d0337a`                                                          |
| Runtime registration | SHA-256 `7d6e7ec5f4ad84f017793cd80c41fa707fe3e72efbf8702fb053ae12f7f5d827`                                                          |
| Pinned pricing       | SHA-256 `99fbdcbf38233c00f574dd03469849914414babf654aee3a49d3776c9fa6c6cf`, OpenAI / `gpt-6-luna`, retrieved `2026-09-28T16:17:02Z` |

The full-argument `--preflight --build` completed at `2026-09-28T20:21:45Z`,
exit 0, in 29.665 seconds: `eligible=true`, no reasons, `model_attempts=0`,
`artifact_creation=false`. Raw build/preflight output is private under
`/tmp/opencode/ktx-configured-qualification-20260928/preflight-build`.
The local image ID was then inspected and pinned for the paid command.

### Executed command and environment

Working directory: `~/git/ktx`. The paid command was:

```bash
root=$(mktemp -d /tmp/opencode/ktx-configured-compare.XXXXXX)
env -u KTX_DB -u KTX_PRICES -u KTX_BIN -u DOCKER_BIN \
  HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" \
  ./build/ktx bench compare \
  --spec exact-match --model openai/gpt-6-luna --variant high \
  --task grep-noise --baseline passthrough --candidate passthrough \
  --target-valid 1 --max-attempts 2 \
  --attempt-timeout 4m30s --termination-grace 30s \
  --auth ~/.local/share/opencode/auth.json \
  --runtime-config ~/git/ktx/docs/normalization-model-registration.json \
  --pricing-snapshot /tmp/opencode/gpt-6-luna-qualification-pricing.json \
  --pricing-sha256 99fbdcbf38233c00f574dd03469849914414babf654aee3a49d3776c9fa6c6cf \
  --repo-root ~/git/ktx \
  --image sha256:9994d6b408eb6c00849d5e70efc932f41be67b48abff34d57c04edc771476742 \
  --experiment-id gpt-6-luna-qualification-runtime-config-20260928 \
  --format json
```

The actual subprocess used the absolute local-binary path and the same argv;
the disposable capture wrapper kept stdout/stderr private. Build/preflight used
these same comparison arguments with `--image ktx-bench --preflight --build`
instead of the immutable image ID. Each help, image-inspection, compare,
list/show/validate, and read-only evidence-check invocation had a new disposable
HOME/XDG environment and `KTX_DB` unset. There was no host generation, ambient
database access, credential inspection, source change, or extra agent attempt.

Native execution was Linux `7.2.7-1-cachyos`, x86_64, using the local Docker
benchmark vehicle. No non-Linux coverage is claimed. The root checkout was
dirty with the audited prerequisite and reports; the manifest retains that
state and a repository snapshot. The new image was built from that current
source, not merely identified by the unchanged Git HEAD.

### Results and cost accounting

**Compare exited 0**, from `2026-09-28T20:23:26.277812Z` to
`20:23:51.204850Z`: **24.914 seconds total**, including preparation and cleanup.
Exactly two new attempts ran, with no warmups or benchmark retries. There was no
timeout and no remaining owned container. The retained attempt timestamps have
second resolution (approximately 12 and 11 seconds respectively).

| Measurement                | `baseline-001` | `candidate-001` |
| -------------------------- | -------------: | --------------: |
| Exit / status              |  0 / completed |   0 / completed |
| Score / full pass          |       1 / true |        1 / true |
| Planted fact matched       |            Yes |             Yes |
| Tool calls                 |              1 |               1 |
| Accepted model steps       |              2 |               2 |
| Uncached input tokens      |          4,789 |           4,788 |
| Output tokens              |             65 |              64 |
| Cache-read tokens          |          7,168 |           7,168 |
| Cache-write tokens         |              0 |               0 |
| Reasoning tokens           |              0 |               0 |
| Runtime-reported USD       |          **0** |           **0** |
| Pinned-price estimated USD |   **0.000583** |    **0.000582** |
| Collector epochs / events  |        1 / 105 |         1 / 105 |
| Collector close state      |       complete |        complete |
| Contradictions             |              0 |               0 |
| Retained stderr bytes      |              0 |               0 |

All token and cost fields above are measured on both model steps in each
attempt. The event-hook ledger accepted both steps, with no duplicate/conflicting
model-step classification and no retry events retained. This is not a claim
about hidden provider-request counts or retries. Wrapper/event-hook projections
agree under archive validation.

The **sum of retained six-decimal per-attempt estimates is USD 0.001165**,
below the frozen USD 1 new-batch target. Estimates use the matching retained
OpenAI/Luna record and its base input, output, and cache rates; the observed
usage does not select the higher-context tier. Reported zero remains a measured
runtime value and is not interpreted as a zero provider invoice. Actual billing
and provider-internal behavior are not available.

Accounting across the whole work: **two historical failed agent attempts plus
two successful new attempts = four agent attempts**. Earlier reported and
estimated costs remain unavailable. Therefore cumulative provider charges and
an all-attempt estimated total remain **unknown**; only this new batch's
estimated total is known. No historical failure or cost was discarded.

### Identity and validation authority

The manifest and both attempt records retain the exact runtime-config SHA-256.
Current validation verifies its input inventory and bytes, and the producer
mounts that captured file read-only with the generated collector still active.
The exact model/API ID and `high` variant are established by the **explicit
launch argv, manifest, retained registration, and pinned runtime behavior**.
They are not inferred from costs or answer style.

The accepted event-hook telemetry reports runtime `opencode`, version `1.18.5`,
but its **provider and model fields are null**; the wrapper stream does not
independently report `variant=high`. Thus the pass qualifies the requested
runtime execution path, not independent provider-reported model/effort identity.
The pricing key is compatible with that configured exact model. No claim of
provider identity attestation, internal reasoning effort, or invoice accuracy is
made. Zero reported reasoning tokens does not prove that `high` was ignored.

New local retained archive (untracked, under `.ktx/experiments/`):
**`gpt-6-luna-qualification-runtime-config-20260928`**.
These local-binary checks each used separately isolated HOME/XDG roots:

```bash
./build/ktx bench list --format json
./build/ktx bench show --format json gpt-6-luna-qualification-runtime-config-20260928
./build/ktx bench validate --format json gpt-6-luna-qualification-runtime-config-20260928
```

All exited 0. List found the new ID; show returned the pinned manifest and
runtime-config identity. Validate returned **`valid=true`, `target_met=true`,
1/1 valid target per side, 2/2 valid attempt checks, zero projection divergences,
`summary_structural_valid=true`, and `summary_byte_match=true`**. Manifest schema
is 1, attempt schema 3, methodology `v8-exact-match`, summary schema 8.
No summarize command or archive rewrite was performed. Additional SQLite
inspection was query-only and confined to the **two new** owner databases;
their hashes were unchanged after inspection. Earlier databases were not opened.

The [sanitized qualification artifact](normalization-qualification-20260928.json)
is the trackable result authority, including identities, counts, costs, and
manifest/attempt/summary hashes. Raw output and check receipts remain private at
`/tmp/opencode/ktx-configured-qualification-20260928`; sensitive archives remain
ignored rather than being copied into public documentation.

### Feasibility acceptance assessment and next-protocol constraints

**Recommendation: the bounded feasibility/evidence task can pass.** This is an
acceptance recommendation, not a lifecycle/evaluation write or an implementation
GO. Its criteria expressly allow application/update boundaries to be reported
unsupported. They do not require fabricating a general instruction candidate or
calling source-text duplication measured savings.

| Required criterion                                                                 | Final scoped assessment                                                                                                                                                                                                                                                                              |
| ---------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Supported facts, unexamined records, actual gaps distinguished                     | Covered: chronological failures, source evidence, catalog/registry observations, real qualification, and unexamined historical/private material remain separate                                                                                                                                      |
| Sanitized candidates with benefits, hazards, runtime redundancy                    | Covered: instruction prohibition is only a synthetic control; the conditional read-description candidate removes three source-duplicated bullets while preserving parameter wording; no measured normalization benefit is asserted                                                                   |
| Application/update boundary evidenced or unsupported for each family               | Covered through the permitted unsupported outcome: pinned hooks/order are source-supported, but executable instruction application/update and final post-transform schema/update visibility are **not observed**                                                                                     |
| Required model/container/collector/pricing qualify before dependent implementation | **PASS at the runtime execution/archive contract:** prescribed exact launch and registration, rebuilt pinned image, complete collector and usage, compatible retained prices, correct answers, and successful archive validation; independent provider model/variant attestation remains unavailable |
| Infeasibility stops dependent work for revision, architecture unresolved           | Covered: the earlier real blockers stopped work until the scoped prerequisite was implemented; no unsupported normalization implementation/campaign proceeded and no architecture choice was made                                                                                                    |

The next protocol must preserve these limits rather than treating this pair as
a normalization experiment:

1. There is still **no evidenced worthwhile general instruction normalizer**.
   Do not promote the synthetic control into a treatment merely to fill that
   family. Record an unsupported branch or establish a worthwhile candidate.
2. The read-description candidate remains **conditional**. Before attributing
   savings or preservation to it, establish a supported executable application
   boundary after runtime schema conversion and demonstrate changed-source/config
   invalidation. The corresponding instruction boundary is also unobserved.
3. Freeze recovery/preservation checks, correctness scoring, and cost evidence
   for any actual treatment. One tiny passthrough/passthrough pair establishes
   infrastructure, not treatment effectiveness, cache improvement, significance,
   or coverage across tasks/runtimes.
4. Carry the model/variant provenance limitation and the reported-versus-estimated
   cost distinction forward. Estimates remain API-equivalent calculations from
   observed usage, not provider billing evidence.

No next-protocol work or architecture decision is executed here. The prior
candidate/source research is reused without a new normalization candidate or
default. This qualification continuation changes only reports/evidence and
disposable tooling; task state, evaluation state, commits, pushes, and delegation
remain untouched.
