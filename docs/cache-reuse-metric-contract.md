# Runtime-reported cache reuse metric contract

This contract defines two non-ranking views of retained OpenCode model-step
evidence. The primary view is a direct cached-input share. The secondary view
is a consecutive-step estimate adapted from Pi's cache-waste calculation. Both
describe runtime-reported token fields; neither describes provider requests,
cache hits or misses, eligible prefixes, isolation, expiry, or causality.

The machine-readable authority is
`tools/bench/cache-reuse-contract-v1.json`. Future collection, derivation, and
rendering work must implement that versioned contract rather than changing an
existing summary schema or historical artifact in place.

The active benchmark context is `openai/gpt-5.6-luna` with CLI variant
`--variant high`. This configured context is retained as provenance but never
backfills runtime-emitted source identity or changes the accounting rules.

## Accounting boundary and direct share

OpenCode 1.18.5 commit
`e5cc278dec9294a627a7b05f47ce6a564408c1a2` gives the three input fields a
disjoint meaning. Its `Usage` contract states this invariant:

```text
nonCachedInputTokens + cacheReadInputTokens + cacheWriteInputTokens = inputTokens
```

The pinned event publisher independently maps those three fields to
`tokens.input`, `tokens.cache.read`, and `tokens.cache.write`. Therefore, for
OpenCode 1.18.5, cache-write tokens are a disjoint input bucket and are included
in logical input. The retained local OpenCode 1.18.4 observations are historical
corroboration: they agree with the invariant but all have measured cache write
zero, so they neither constitute a 1.18.5 live run nor independently demonstrate
a positive cache-write case. This conclusion is intentionally scoped to the
pinned runtime contract. Another runtime or version is ineligible until an
equivalent disjoint accounting invariant is pinned; configured provider or
model context cannot supply it.

The 1.18.5 accounting and event-mapping blobs are byte-identical to 1.18.4. The
machine contract also pins the generated event union, plugin event hook, run
JSON formatter, OpenAI cached-token mapping, and Codex adapter blobs. Together
these authorities preserve the sequence, wrapper-filtering, session-identity,
retry/reset-availability, and OpenAI mapping conclusions under the new tag.

For attempt `a`, include every contributing model step, not only a selected or
last step:

```text
U_a = sum(step.uncached_input)
R_a = sum(step.cache_read)
W_a = sum(step.cache_write)
L_a = U_a + R_a + W_a
direct_cached_input_share_a = R_a / L_a
```

Every contributing value must be measured. A measured zero participates in the
sums. Missing or null is unknown, malformed remains malformed, and neither is
reconstructed. The share is not applicable when `L_a <= 0`. Reports must show
`U_a`, `R_a`, `W_a`, `L_a`, the formula, runtime/version, and identity
provenance beside the share.

The direct view is emitted only when a cache-aware experiment identifies both
its manifest runtime and cache-control runtime as OpenCode 1.18.5. Such a
summary uses additive schema 4 and retains one row per attempt under
`direct_cached_input_share.attempts`. Each row retains its attempt ID, side,
phase, and cycle along with the three bucket sums, logical input, and share as
state/value records. Incomplete records carry exact
field and source-event-ordinal gaps; an attempt with no retained model steps
carries a missing gap for each required bucket with a null ordinal. A complete
zero logical input has share state `not_applicable` and reason
`logical_input_not_positive`, while a zero numerator with positive logical
input remains a measured zero share. The same direct view is included in the
validation report.

Bucket and logical-input sums use arbitrary-precision integer arithmetic and
serialize as exact JSON integers, including values beyond signed 64-bit range.
The share is formed as an exact reduced rational before its numeric
token-fraction value is rendered; the reduced fraction is retained beside it
as `ratio` (for example, `1/3`). Classification therefore never depends on a
wrapped or rounded logical-input sum.

Each row also keeps the retained stream path and accepted model-step count,
configured attempt and manifest identity context, runtime contract source, and
the provider/model/runtime identity actually present on every source event.
Configured identity is labeled as context and a false
`configured_identity_backfills_source_events` marker makes the non-backfill
rule machine-readable. Runtime/version mismatches remain on their existing
schema 1–3 output and gain no direct field. Regeneration and validation also
honor an already-retained schema 1–3 summary as the historical boundary, so a
pinned runtime identity does not rewrite an older summary into schema 4.

This is an OpenCode-reported cached-input share, not the previously rejected
generic cache-miss ratio. It is descriptive, non-ranking, and valid only under
the named accounting contract.

## Consecutive-step estimate

One estimate unit is the ordered pair `(p, c)` of adjacent accepted model steps
within one positively covered runtime segment. Adjacency, retry absence, reset
absence, and native runtime-session continuity must each be established by
retained runtime events. Silence is not coverage. A reset, retry, interruption,
restart, compaction boundary, concurrent interleaving, unavailable coverage, or
contradiction makes the pair ineligible.

The prior step must have measured cache-read and cache-write fields whose values
establish positive cache activity (`R_p + W_p > 0`). With complete measured
input buckets and positive logical inputs:

```text
P_i = min(L_p, L_c)
shortfall_i = P_i - R_c
estimated_reuse_share_i = R_c / P_i
```

`P_i` is the estimated reusable-token basis, not a reconstructed provider
prefix. `R_c > P_i` is inconsistent with this estimate and makes the pair
ineligible rather than being clamped. Otherwise zero and all positive
shortfalls are retained exactly. A shortfall from 0 through 1,024 tokens is
labeled `within_noise_floor`; a larger value is `above_noise_floor`. The label
affects interpretation only: it never censors a value or changes eligibility.
No cost or TTL cause is estimated.

### Pinned sequence availability

The OpenCode 1.18.5 event union resolves the collection unknown without a live
provider call. The plugin `event` hook receives `message.part.updated` events
whose part union includes `step-finish`, `retry`, and `compaction`; it also
receives retry `session.status`, `session.compacted`, `session.error`, and
`server.instance.disposed`. Those runtime events can positively establish local
callback order or an observed boundary when the
collector serializes and retains the complete enclosing interval. Native
`part.sessionID` supplies runtime-session identity. Process restart continuity
still requires an explicit retained collector-lifecycle fact; a connection or
new session is not relabeled as a restart.

OpenCode's pinned `run --format json` formatter emits step starts/finishes,
completed text/tool records, and errors, but filters retry parts, compaction
parts/events, and events from sessions other than the active session. Therefore
the wrapper can retain step order and native session identity, but it must mark
retry, reset/compaction, restart, and concurrent-session coverage unavailable
unless an independent explicit collector fact covers the exact interval. An
error is an observed interruption boundary, but absence of an error is not
interruption coverage. Neither path derives a boundary from token movement,
IDs sorted after capture, or silence.

The machine contract defines the predicate inputs and the following ordered
rules. Evaluate them from top to bottom and retain the first true rule; no true
rule means the pair is eligible. Field states are exactly `measured`, `unknown`,
or `malformed`. Order coverage is `covered`, `unavailable`, or `contradictory`;
retry and reset coverage are `covered_no_boundary`, `unavailable`, or
`boundary_observed`. Segment and concurrent-session coverage are `covered`,
`unavailable`, or `contradictory`. Coverage is positive only when a retained
runtime event stream or explicit collector coverage fact names the runtime,
version, `event_hook` or `wrapper_stream` source, and exact enclosing event
ordinals. Every coverage class must match both pair members on runtime, version,
collection source, and enclosing ordinal window. A missing declaration, a
declaration that starts late or ends early, or a cross-runtime, cross-version,
cross-source, or otherwise conflicting declaration cannot establish a clean
interval. A recognized in-window retry, compaction, interruption, restart, or
other-session event with incomplete provenance makes its applicable class
unavailable or contradictory; it is not discarded as silence. Every rule after
`first_step` also requires that a prior step exists.

| Reason                                                 | Exact match condition                                                                                                                                         |
| ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `first_step`                                           | No prior accepted model step exists.                                                                                                                          |
| `non_consecutive_order`                                | A prior step exists, order coverage is `covered`, and the two accepted ordinals are not adjacent.                                                             |
| `order_coverage_unavailable`                           | A prior step exists and order coverage is `unavailable`.                                                                                                      |
| `order_coverage_contradictory`                         | A prior step exists and order coverage is `contradictory`.                                                                                                    |
| `retry_coverage_unavailable`                           | A prior step exists and retry coverage is `unavailable`.                                                                                                      |
| `retry_boundary_observed`                              | A prior step exists and retry coverage is `boundary_observed`.                                                                                                |
| `reset_coverage_unavailable`                           | A prior step exists and reset/compaction coverage is `unavailable`.                                                                                           |
| `reset_or_compaction_boundary_observed`                | A prior step exists and reset/compaction coverage is `boundary_observed`.                                                                                     |
| `segment_coverage_unavailable`                         | A prior step exists and interruption/restart segment coverage is `unavailable`.                                                                               |
| `segment_coverage_contradictory`                       | A prior step exists and interruption/restart segment coverage is `contradictory`.                                                                             |
| `interruption_boundary_observed`                       | A prior step exists and positively covered segment evidence contains an interruption between the pair members.                                                |
| `restart_boundary_observed`                            | A prior step exists and positively covered segment evidence contains a restart between the pair members.                                                      |
| `concurrent_coverage_unavailable`                      | A prior step exists and concurrent-session coverage is `unavailable`.                                                                                         |
| `concurrent_coverage_contradictory`                    | A prior step exists and concurrent-session coverage is `contradictory`.                                                                                       |
| `concurrent_interleaving`                              | A prior step exists and a retained interleaving event occurs between the pair members.                                                                        |
| `runtime_session_identity_unavailable`                 | A prior step exists and either native runtime-session identity state is not `measured`.                                                                       |
| `runtime_session_identity_changed`                     | Both native runtime-session identities are measured and unequal.                                                                                              |
| `runtime_contract_mismatch`                            | Either pair member's runtime name, version, and accounting-contract tuple differs from the required tuple.                                                    |
| `prior_cache_activity_absent`                          | **Both** prior cache-read and cache-write states are `measured`, and their values sum to zero. Unknown or malformed activity fields cannot match this reason. |
| `prior_uncached_input_unknown`                         | Prior uncached-input state is `unknown`.                                                                                                                      |
| `prior_uncached_input_malformed`                       | Prior uncached-input state is `malformed`.                                                                                                                    |
| `prior_cache_read_unknown`                             | Prior cache-read state is `unknown`.                                                                                                                          |
| `prior_cache_read_malformed`                           | Prior cache-read state is `malformed`.                                                                                                                        |
| `prior_cache_write_unknown`                            | Prior cache-write state is `unknown`.                                                                                                                         |
| `prior_cache_write_malformed`                          | Prior cache-write state is `malformed`.                                                                                                                       |
| `current_uncached_input_unknown`                       | Current uncached-input state is `unknown`.                                                                                                                    |
| `current_uncached_input_malformed`                     | Current uncached-input state is `malformed`.                                                                                                                  |
| `current_cache_read_unknown`                           | Current cache-read state is `unknown`.                                                                                                                        |
| `current_cache_read_malformed`                         | Current cache-read state is `malformed`.                                                                                                                      |
| `current_cache_write_unknown`                          | Current cache-write state is `unknown`.                                                                                                                       |
| `current_cache_write_malformed`                        | Current cache-write state is `malformed`.                                                                                                                     |
| `prior_logical_input_not_positive`                     | All prior input buckets are measured and their sum is at most zero.                                                                                           |
| `current_logical_input_not_positive`                   | All current input buckets are measured and their sum is at most zero.                                                                                         |
| `estimated_reusable_tokens_not_positive`               | All six buckets are measured and `min(L_p, L_c)` is at most zero.                                                                                             |
| `current_cache_read_exceeds_estimated_reusable_tokens` | All six buckets are measured, both logical inputs are positive, and current cache read exceeds `min(L_p, L_c)`.                                               |

The prior-activity rule intentionally precedes other prior-field rules but can
match only from two measured activity fields. Thus measured read/write zero plus
unknown prior uncached input selects `prior_cache_activity_absent`, while an
unknown cache-read field selects `prior_cache_read_unknown`. Reports include
eligible and per-reason ineligible counts.

New eligible output is additive under the same schema-4 OpenCode 1.18.5 gate
as the direct view, at `estimated_consecutive_step_reuse`. It is labeled
`estimated`, names `pi_consecutive_step_reusable_basis`, and retains each
eligible pair's prior and current logical input, current cache read, exact
reusable-token basis, exact shortfall, exact reduced reuse-share ratio, and
noise-floor interpretation. The summary and validation report also retain the
eligible total, ineligible total, and a count for every declared reason. Human
rendering identifies the method and source stream and states that ranking has
no effect and TTL and expiry causes are unsupported. Existing schema 1–3
summaries remain byte-stable.

## Paired comparison and uncertainty

Only measured attempts paired by cycle enter a comparison. Setup observations
remain separate. A pair is complete for the primary comparison only when both
attempts have an available direct share and stable, non-conflicting
runtime/version/configuration provenance under the same protocol. Runtime-
emitted provider/model identity, when present, must agree; configured identity
may describe experiment context but never backfills a source event.

For each complete cycle, define
`d_i = candidate_share_i - baseline_share_i`. Positive direction means the
candidate has the larger runtime-reported cached-input share. With at least five
complete pairs, report the arithmetic mean and the two-sided 95% Student-t
interval:

```text
mean(d) +/- t(0.975, n-1) * sample_standard_deviation(d) / sqrt(n)
```

Sample standard deviation uses denominator `n-1`; zero variance produces the
degenerate interval `[mean(d), mean(d)]`. Fewer than five pairs is descriptive
only. An interval wholly above zero supports “higher”; wholly below zero
supports “lower”; an interval containing zero is inconclusive. No significance
wording or causal wording is authorized.

Quality is preserved only when, over those same complete pairs, candidate mean
fact score and candidate full-pass rate are each at least the corresponding
baseline value. A direct success or improvement statement additionally requires
protocol compliance, at least five complete pairs, an interval wholly above
zero, stable identity provenance, and preserved quality. Otherwise valid values
remain descriptive and the recorded blocking reasons explain why no success
claim is authorized.

Secondary outcomes are always labeled `estimated`, name the method and
eligibility counts, and cannot authorize or rescue a primary claim. Provider-
native request, hit, miss, fill, eviction, eligibility, prefix, isolation, TTL-
cause, missed-cost, cross-provider pooled, and causal wording is permanently
unsupported.

New schema-4 summaries expose this descriptive view at
`cached_input_share_comparison`. `setup.observations` retains cold and warm-up
attempts but marks them excluded. `pairs` contains only cycles with exactly one
comparable measured-warm attempt per side, including exact reduced direct-share
fractions, candidate-minus-baseline fractions, raw buckets, execution order,
quality, cost, delivery/artifact, configuration, pipeline, and identity
provenance. Incomplete or confounded cycles remain under `observations` with
machine-readable reasons. Direct distributions, paired uncertainty, and quality
use those same complete pairs. The separately labeled `secondary_estimated`
view reports the Pi-style method and eligibility counts over measured-warm
attempts and has no effect on direct direction, quality, or ranking.

Comparison eligibility applies the retained experiment identity and full
evaluation predicate, role/phase agreement, cache mode, source, idle gap and
side-specific namespace, configuration and pipeline identity, runtime and
configured identity, and source-event identity rules through one attempt path.
Retry, reset, segment, and concurrency coverage must be positively retained for
each attempt; silence is unavailable even for a single model step. Execution
order is then assessed over exactly the provisionally eligible pair subset. If
that subset is unbalanced, every provisional pair is moved to descriptive
observations with protocol reasons; pairs are never selectively removed to
manufacture balance. Per-cycle order must also match the retained alternating
policy.

For at least five complete pairs, an interval containing zero adds
`two_sided_95_percent_interval_contains_zero`; worse candidate same-pair quality
adds `quality_not_preserved`. These reasons follow the machine contract's
declared precedence and remain descriptive inputs rather than statement
authorization.

Schema-4 summaries derive `statement_authorization` only from the retained
`direct_cached_input_share`, `estimated_consecutive_step_reuse`, and
`cached_input_share_comparison` objects. Each outcome records `authorized`,
ordered machine reasons, evaluated gates, evidence references and basis, scope,
and a statement that is null unless authorized. Validation includes the same
object and regeneration derives it deterministically from those evidence views.

Authorization fails closed on a contradictory projection. It derives complete
pair count only from rows with a unique positive cycle, one correctly labeled
measured-warm row per side, and distinct globally unreused attempt IDs. Each ID
must resolve exactly once under `direct_cached_input_share.attempts`; the direct
row and nested pair row must agree on side, phase, cycle, exact measured share,
and paired identity. It then rederives every candidate-minus-baseline fraction,
paired statistic, and same-pair quality value from that verified set. Declared
counts, differences, statistics, direction, and aggregate quality must agree
with that derivation. Direct authorization also requires the named contract and
OpenCode 1.18.5 runtime scope. Secondary authorization requires the exact estimated
method, no ranking effect, and eligible and ineligible totals consistent with
the retained attempt and pair rows. Missing, malformed, non-finite, or
contradictory projections add `evidence_projection_inconsistent`; malformed
pair shares remain `direct_evidence_incomplete`. Neither state can produce an
authorized statement.

The direct outcome may describe available attempt shares. The comparative
descriptive outcome may report the exact complete-pair count, per-pair
candidate-minus-baseline fractions, mean, and interval without directional,
significance, or causal interpretation. Directional outcomes apply their own
gates rather than requiring every comparison blocker to be absent. Foundational
runtime, direct-evidence, cycle, protocol, and identity blockers prevent every
directional statement. Fewer than five pairs prevent higher, lower, and
inconclusive wording. A wholly positive interval authorizes the higher wording
only when same-pair mean fact score and full-pass rate are each no lower for the
candidate. `quality_not_preserved` does not erase the descriptive comparison. A
wholly negative interval may authorize only the observed lower wording,
regardless of quality preservation. An interval containing zero authorizes only
the explicit no-direction statement.

Authorization normalizes foundational reasons before evaluating any comparison
outcome. Missing or duplicate cycle sides become `cycle_pair_incomplete`.
Temporal, retry, reset, order, and execution-policy confounds become
`protocol_noncompliant`. Direct incompleteness and identity conflicts retain
their declared taxonomy. Raw observed reasons remain in the evidence basis for
diagnosis, but unknown detail is never emitted as an authorization reason or
allowed to displace the declared precedence.

Authorization identity provenance separately classifies configured context and
runtime-emitted source identity from all deduplicated direct attempt rows, even
when a cycle is incomplete or blocked. A separate comparison scope covers only
the attempt IDs in complete pair rows. Source identity is `configured_only`, `absent`,
`partial`, or `present`; per-class event counts and conflicts remain visible.
Configured-only identity is not promoted to source evidence, and
`configured_identity_backfills_source_events=false` remains explicit. Present
runtime-emitted identity must agree with the configured comparison context;
source identity may remain absent when the controlled comparison otherwise
qualifies.

The secondary outcome is authorized only with at least one eligible pair. Its
wording says `estimated`, names `pi_consecutive_step_reusable_basis`, reports
eligible and ineligible counts, and remains descriptive and non-ranking. It has
no input to any direct outcome. Unsupported claim categories remain available
as machine policy only and are never emitted as supported statements.

## Historical boundary

This contract is additive. Existing methodologies, retained summaries,
rankings, cost-per-correct behavior, schemas, and bytes keep their historical
meaning. Historical direct-provider fixtures remain compatibility/provenance
material only and cannot authorize current collection or provider-native
claims.
