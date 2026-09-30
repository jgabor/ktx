# Read-description offline gate observations

2026-09-29. Native Linux verification of the
[premeasurement protocol clarification](normalization-experiment-protocol.md#premeasurement-clarification--2026-09-29-revision-2).
These are synthetic offline controls, not normalization measurements, live
Luna qualification, paid semantic results, or a savings recommendation.

## What changed and why

The earlier runtime probe resolved definitions and sent `noReply` messages. It
did not execute the built-in read. Changing the mounted candidate tested
fixture identity only; it did not test changed read-hook ingress.

`TestReadDescriptionRuntimeBoundary` now installs public plugin dependencies
in a disposable benchmark container, disconnects its Docker bridge, verifies
that no external networks remain, and uses a loopback Responses transport.
There is no auth mount or host configuration mount. The test-only synthetic
model is `openai/gpt-5-offline-read`, not the paid model registration. Its
transport emits typed read calls and completions derived from the real tool
output. It does not implement or mock read. SSE follows the
[`@ai-sdk/openai` 3.0.84 Responses schema](https://github.com/vercel/ai/blob/%40ai-sdk/openai%403.0.84/packages/openai/src/responses/openai-responses-api.ts),
the dependency pinned by OpenCode 1.18.5.

The full generated collector plugin is moved unchanged out of auto-discovery
and loaded between explicit test-only before/after hooks. Its byte hash is
checked before and after. No runtime fork, `--pure`, plugin splicing, or
production runtime-config expansion is used. The paid runtime-config validator
still rejects provider options including this loopback `baseURL`/synthetic key.

Two harness issues were corrected before accepting results:

- The required hook needs both the event interval and collection owner. The
  fixture now creates a fresh owned database and both records per case, using
  the existing database methods rather than weakening ownership checks.
- The pinned `--attach` client was observed exiting after a tool step while the
  server continued to the final answer. The fixture now uses standalone
  `opencode run --format json` and `--session` for the follow-up, as comparisons
  do. It waits for process exit and scores both complete retained CLI streams.

The real tool also exposed a scorer assumption: pre-delivery read rows have
`is_error=NULL`, not the mock fixture's invented false value. Success now needs
explicit terminal `completed` status plus matching ordered database arguments
and byte-exact output. NULL alone, failure, missing terminal completion,
conflicting callbacks and mismatched retained evidence cannot pass. This same
scorer is used by production and regeneration. The six case contracts, answer
keys and quality/cost thresholds are unchanged.

## Observed runtime results

| Case                                              | Result                                                                                                                                                                                                                        |
| ------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Baseline                                          | Two completed default reads in one session; exactly numbered lines 1–2000; last values `a-2000`, `b-2000`; continuation says lines 1–2000 of 2001, offset 2001; no byte-cap truncation. Both scored phases pass.              |
| Candidate                                         | Same results, byte-identical delivered output to baseline. Only read's description differs. Both scored phases pass.                                                                                                          |
| Unchanged candidate restart                       | Same definition identities and byte-identical output; new owned database/session. Both phases pass.                                                                                                                           |
| Changed retained description line at read ingress | Before-hook mutation observed, actual KTX guard reports `read-description source or parameter schema changed`; HTTP 500; no after-hook observation or valid read treatment; zero transport requests.                          |
| Changed limit-description AST at read ingress     | Same fail-closed result; observed AST identity changes, description identity does not; zero transport requests.                                                                                                               |
| Wrong-limit synthetic control                     | Actual read receives `limit:1999`; default-window oracle and both preservation phases fail. Full collector and hooks remain enabled.                                                                                          |
| Changed mounted candidate fixture                 | KTX reports `read-description fixture identity mismatch`; no KTX application records; zero transport requests. Runtime's untreated definition is not valid treatment evidence. This is fixture identity, not upstream reload. |

Each positive/restart run observed five read-definition applications: one
definition inspection, then two model steps in each standalone CLI turn. Every
read observation was ordered **before hook → KTX record → after hook**. The
observed KTX ordinals were `4, 4, 15, 4, 15`, resetting with each new plugin
epoch. Drift cases observed only the before hook and the KTX diagnostic. The
fixture mismatch observed before/after hooks but **no KTX ordinal**, so it
cannot masquerade as applied treatment.

Stable identities:

- OpenCode: `1.18.5`, source pin `e5cc278dec9294a627a7b05f47ce6a564408c1a2`.
- Benchmark image: `sha256:9994d6b408eb6c00849d5e70efc932f41be67b48abff34d57c04edc771476742`.
- Rebuilt/mounted KTX: `75205124f80dd7bdd67159381c411cc4addf2dd2f9a9d33a87765ac6b5a33d30`.
- Generated collector: `b45cafae09b084aefe6fd9d0d749e21e2fd177252bad40040021f6b12c09ff46`.
- Original description: `98ee843341c2dab2227add0019e48d4b2f0f00f9b042b853d1ee52bb34e6363d`.
- Candidate description: `d4ebb60002e2b98eb8337341e5572548ab283f7e44cad29aa5335f28e18d61a2`.
- Original parameter AST: `f90c59de1b9ad91f6d52892c3beae2e73fca5e290ec87f5999b74655633be355`.
- Changed ingress description: `ef51920d3f63fb7224ee849997e818bc8404b7b639e21f51203051c5b9f041f3`.
- Changed ingress AST: `1b0714d3b319fbf14e31f58795f7735e0a431e32665067f8fe98f0f9ff874ad8`.

## Verification and reproduction

Every outer invocation used a new `mktemp` root under `/tmp/opencode`, with
`KTX_DB` unset, distinct HOME/XDG paths, `TMPDIR=/tmp/opencode`, and explicit
external Go caches. For example, prefix each command independently with:

```bash
root=$(mktemp -d /tmp/opencode/ktx-read-gate.XXXXXX)
env -u KTX_DB \
  HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" TMPDIR=/tmp/opencode \
  GOCACHE=~/.cache/go-build GOMODCACHE=~/go/pkg/mod \
  <command>
```

Commands run from the checkout, in this order (allocate a fresh root each time):

```bash
mage build

env KTX_READ_DESCRIPTION_PROBE_IMAGE=ktx-bench:latest \
  go test ./internal/experiment -run '^TestReadDescriptionRuntimeBoundary$' -count=1 -v

env KTX_READ_DESCRIPTION_CLI=~/git/ktx/build/ktx \
  go test ./internal/experiment ./internal/cli \
  -run 'Test(ReadPreservation|ReadDescription|RuntimeTurn|RuntimeConfigurationInputScope)' -count=1 -v

env BUN_INSTALL_CACHE_DIR=~/.bun/install/cache \
  GOLANGCI_LINT_CACHE=~/.cache/golangci-lint mage check
```

Results:

- Runtime gate: **7/7 subtests passed**, 23.945 seconds, no remaining probe container.
- Focused checks: **passed**, including six-case broken controls, terminal
  authority pass/fail cases, generated-hook unit controls, producer/regeneration
  and local rebuilt CLI validate/summarize/validate. The Docker test is opt-in
  and skipped in this invocation; its separate result above is the authority.
- Full `mage check`: **passed** (race tests, vet, lint, build, Python tests and
  generated-adapter overlap gate). Audit: `forbidden=0`, external caches,
  `ktx_db=unset`. The first full run failed only gofumpt formatting of the new
  test table; after formatting, the entire check was rerun, not skipped.
- `git diff --check`: passed.

Local logs are disposable; these locations and SHA-256 values identify the
observations without making them campaign archives:

| Log                                                                                     | SHA-256                                                            |
| --------------------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `/tmp/opencode/ktx-read-gate-runtime.jJp1or/runtime.log`                                | `25d6b60aa3073de862a838b3fd6a169352d84a85d82137ddcd5ff0a59b1d4219` |
| `/tmp/opencode/ktx-read-gate-focused-final.vJlJkw/focused.log`                          | `4e66c165f872973baf94ffb8a54817cacdfc56c0aef72459d63e7222ef3904ee` |
| `/tmp/opencode/ktx-read-gate-check.Mv9u1X/check.log`                                    | `ce0314e473d555881cf854304866301674e6ff33dc5d17d5de65aaed38687b34` |
| Pre-correction scorer failure: `/tmp/opencode/ktx-read-gate-runtime.WmCyy7/runtime.log` | `7d55af1fa7c652d1c4dc8d806b06199d7e70860924f639a64574d29e64b04d1e` |
| Formatting failure: `/tmp/opencode/ktx-read-gate-check.e9EBGQ/check.log`                | `a36dbf26800a3be3ec5847a25aba6b0dac2cbe1b5d1f9af908b832a8eadb8854` |

## Limits

Upstream source-file reload is **untested**. Source/schema update coverage is
limited to actual read-hook ingress rejection. Post-hook provider schema and
provider-internal behavior remain unknown. The synthetic transport's answers,
usage and cost values provide no live-model semantic or economic evidence.
The live six-case schedule, exact paid model/runtime and 24-attempt budget
remain unchanged and unexecuted here. No production deployment, project
lifecycle change or architectural approval is implied.
