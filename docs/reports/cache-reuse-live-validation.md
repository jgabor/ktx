# OpenCode 1.18.5 cache-reuse live validation

## Status

The retained `cache-reuse-luna-high-oc-1-18-5-20260726-v3` experiment passed
validation and supports this scoped statement:

> Across five complete pairs, the candidate had higher OpenCode-reported
> cached-input share under the retained protocol. Candidate-minus-baseline mean
> was 0.19121576876489477, with a two-sided 95% Student-t interval of
> [0.19057450330802284, 0.1918570342217667].

The interval is descriptive uncertainty and an authorization gate; it does not
authorize significance or causal wording. The evidence does not establish
provider-native requests, hits, misses, fills, evictions, eligibility, visible
prefixes, isolation, or TTL behavior, and it does not measure generic cache
effectiveness.

## Vehicle and scope

| Field                   | Retained value                                                                                         |
| ----------------------- | ------------------------------------------------------------------------------------------------------ |
| Date                    | 2026-07-26                                                                                             |
| Source commit           | `8f80d11b201eac269b85356c8bd0c02e6e2d51b8`; repository `dirty=false`                                   |
| Host executable         | `build/ktx`, version `dev`, SHA-256 `38f0bf18b74fa10644aabedd9a3f59cac6ccfd629d5438cc6f7a4c0612c7bb71` |
| Benchmark image         | `ktx-bench`                                                                                            |
| Image ID                | `sha256:1ff9ecdeffa099c1b5783bf9357096debc209eaa4fddc42ba730a47ce7291af6`                              |
| Image identity          | KTX `0.1.0-dev`; OpenCode `1.18.5`                                                                     |
| Event contract          | `opencode-event-v2`                                                                                    |
| Generated plugin digest | SHA-256 `5c238b7300475a63d52b2e214667c22316753ee122141d5dc62261423dd19582`                             |
| Model context           | `openai/gpt-5.6-luna`, variant `high`                                                                  |
| Cache mode              | `observed`                                                                                             |
| Methodology             | `v5-cache-aware`                                                                                       |
| Local artifact root     | `.bench-data/cache-reuse-luna-high-oc-1-18-5-20260726-v3`                                              |

Offline preflight returned `eligible=true`, with direct, controlled, and
secondary capabilities all true, no reasons, `model_attempts=0`, and
`artifact_creation=false`.

The experiment executed 14 attempts: one cold observation, one warm-up, and
five measured attempts per side. The manifest's safety ceiling was 24 attempts;
14 was the executed count, not the ceiling.

## Validation and collection integrity

| Check                       | Result                                                        |
| --------------------------- | ------------------------------------------------------------- |
| Validator                   | `valid=true`                                                  |
| Regeneration                | `summary_byte_match=true`                                     |
| Measured target             | `target_met=true`; baseline 5/5 and candidate 5/5             |
| Attempt checks              | 14/14 valid                                                   |
| Methodology reasons         | None                                                          |
| Projection divergences      | None                                                          |
| Controlled comparison       | Five complete pairs; protocol compliant                       |
| Measured execution ordering | Baseline first in three cycles; candidate first in two cycles |

Every attempt retained one complete interval with
`contradiction_count=0` and exactly three accepted model steps. No exact or
conflicting duplicate callback occurred in this live experiment. Duplicate
handling is regression-tested, but was not live-observed in this archive.

Source events omitted runtime-emitted model and provider fields, so source
identity is `configured_only`. There were no source identity conflicts, and
`configured_identity_backfills_source_events=false`: configured Luna/high
context describes the experiment but does not become source-event telemetry.

## Direct comparison

Only the five `measured_warm` attempts per side enter the paired result.

| Measure                                    | Baseline           | Candidate          |
| ------------------------------------------ | ------------------ | ------------------ |
| OpenCode-reported cached-input share, mean | 0.5299901044108349 | 0.7212058731757298 |
| Complete-pair quality count                | 5                  | 5                  |
| Full-pass rate                             | 1                  | 1                  |
| Mean fact score                            | 1                  | 1                  |

Candidate-minus-baseline cached-input share had mean
0.19121576876489477. Its two-sided 95% Student-t interval was
[0.19057450330802284, 0.1918570342217667]. Quality was preserved on the same
five complete pairs. These facts authorize only the scoped "higher
OpenCode-reported cached-input share under the retained protocol" wording above.

## Secondary estimate and cost views

The Pi-style `pi_consecutive_step_reusable_basis` view retained 28 eligible and
14 ineligible units. Every ineligible unit was `first_step`. This view is
estimated, descriptive, non-ranking, and has no effect on the direct result.

Runtime-reported cost mean was literal zero on both measured sides. Separately,
the retained price-derived estimated API cost means were 0.2590542 for baseline
and 0.0156608 for candidate, a baseline-minus-candidate difference of
0.2433934. Price-derived break-even after setup occurred at measured turn 1.
These values use the retained models.dev pricing snapshot; they are estimates,
not runtime billing telemetry.

## Retained hashes

| Artifact         | SHA-256                                                            |
| ---------------- | ------------------------------------------------------------------ |
| `manifest.json`  | `f6a76df62ca883274e7cf4c98b23cfed53bc8f3ac95bee008cab6033d250e9bb` |
| `attempts.jsonl` | `e662d81d11b48b78c56da6fc2874d462da49cbf8ccf8ede1db4adec55dc26cbc` |
| `summary.json`   | `814fd6753bb91b2059d794061e2d4a4fd64e800874ff9ce6b957d6e80b06d8a8` |
| `pricing.json`   | `a65bf830edb66cce0b66bff57c9139e8a0fe43a364707d8a4fcfd1f8fbac9c7c` |

## Preserved failed history

The successful v3 experiment is a new archive. It does not alter or upgrade the
two earlier failed archives.

### Unsuffixed archive

`.bench-data/cache-reuse-luna-high-oc-1-18-5-20260726`, from source commit
`e6ace6d2e425cd35840df557ba23eb46d950b6b4`, remains `valid=false` with
`summary_byte_match=true` and `target_met=false`. Measured valid counts were
baseline 2/5 and candidate 4/5. It had zero complete controlled pairs, zero
eligible Pi-style units, and five projection divergences. Its direct-only
historical observations remain unchanged and cannot inherit v3's authorization.

That run used host `build/ktx` version `dev`, SHA-256
`2f28fb238d373c62cc659b61dbe510e6068a47e26687df167c64dd1b025df381`, and
image `ktx-bench:oc-1.18.5-e6ace6d` with ID
`sha256:53c75e15e1b5be2bbf8b89007391e8bea4a6230d238e4f4814110dc2827cb237`.

| Unsuffixed artifact | SHA-256                                                            |
| ------------------- | ------------------------------------------------------------------ |
| `manifest.json`     | `e77e7287756740d148eaa626daf47cc9a553f5da071a6bc90a188813b668cc2b` |
| `attempts.jsonl`    | `697c3f8dba23370e727c85f6c7e9ccc3eebcc62f0cdf89972e82e9cace94456a` |
| `summary.json`      | `416666541479526e1086ff7053d4dbc38cf301b4f1d44b34e88e5fe79c9ed107` |
| `pricing.json`      | `a65bf830edb66cce0b66bff57c9139e8a0fe43a364707d8a4fcfd1f8fbac9c7c` |

### v2 archive

`.bench-data/cache-reuse-luna-high-oc-1-18-5-20260726-v2`, from source commit
`3883ab4`, used image
`sha256:e376c9d89703e0725f83615e352f3e084f046d07e3e4f595a92e28d80f02d4ae`.
It remains `valid=false` with `summary_byte_match=true`: 10/14 attempt checks
were valid, measured targets were baseline 3/5 and candidate 3/5, complete
controlled pairs were zero, Pi-style eligibility was 20, and projection
divergences were zero. The four invalid attempts were `candidate-003`,
`candidate-004`, `baseline-004`, and `baseline-006`, caused by the diagnosed
duplicate-callback queue bug.

| v2 artifact      | SHA-256                                                            |
| ---------------- | ------------------------------------------------------------------ |
| `manifest.json`  | `8b004c3f1221d71f01fbcf618196ecc8f7134747e949209dea0295f32d7b4511` |
| `attempts.jsonl` | `c7cdae90ca6064097b8288aba03e58163107efb5ce27bd672d40e79f28bceb92` |
| `summary.json`   | `4281c7c6d8a6383392a97789367c0f686802f12acd137a81b69c653c3acd6cdd` |
| `pricing.json`   | `a65bf830edb66cce0b66bff57c9139e8a0fe43a364707d8a4fcfd1f8fbac9c7c` |
