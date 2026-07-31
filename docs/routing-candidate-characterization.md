# Routing candidate evidence characterization

This characterization freezes a finite set of retained product records,
benchmark fixtures, runtime observations, candidate signals, candidate
layer-to-profile mappings, ambiguity cases, and fallback cases. Its retained
manifest and findings are in
`tools/bench/evidence/routing-candidate-characterization.json`.

The artifact informs the later router-taxonomy deliberation. It does not choose
a taxonomy, profile mapping, threshold, semantic classifier, or implementation.
The five layer/profile pairs in the manifest are candidates copied from the
architectural question, not product defaults or recommendations.

## Evidence contract

| Evidence class                                                  | Interpretation                                                                                                                           |
| --------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `runtime_observation`                                           | A field emitted by a supported agent runtime, or an exact characteristic derived from a retained runtime record with runtime provenance. |
| `historical_product_record`                                     | A retained product row without current supported-runtime provenance; compatibility and provenance evidence only.                         |
| `benchmark_fixture`                                             | A committed synthetic input used to characterize exact bytes and deterministic lexical signals.                                          |
| `historical_runtime_fixture_unsupported_for_current_collection` | A committed runtime-format fixture retained for compatibility and provenance, not current telemetry.                                     |
| `estimate`                                                      | Character-count token arithmetic. It is never presented as a runtime token or spend observation.                                         |

The decision view available at startup was degraded: 5 of 33 entries were
returned and 28 were omitted by the serialized byte budget. The characterization
uses only the returned runtime-only telemetry decision and does not reconstruct
the omitted choices. Profile and vision were absent from orchestration startup
state, so neither is used as evidence here.

Provider documentation and pricing are not inputs. No result claims
provider-internal cache hits, misses, fills, eviction, request boundaries,
isolation, or prefix composition.

## Manifest scope

- Six named retained calls: five OpenCode-attributed runtime records and one
  older record with no runtime provenance.
- Four committed fixtures: build-log, grep-noise, synthetic-source, and a
  historical OpenCode tool-use stream.
- Four implemented routing signals: tool name, raw command prefix, subcommand,
  and exit status.
- Five deterministic content candidates: complete JSON, diff markers, paired
  log markers, instruction markers, and NDJSON record count.
- Exact output character and line counts, with no threshold attached.
- Five unselected layer-to-profile candidate pairs.
- Four ambiguity cases and four fallback cases.
- Six residual-spend groups frozen at explicit source and artifact cutoffs.

The read-only reproduction query emits no input or output bodies. It must run
only against an explicit generated fixture or operator-provided sanitized snapshot
that is known to be non-live. The snapshot must already exist; never
derive it from `ktx db path`, and never create it by copying or opening the
ambient live database.

```bash
snapshot=${KTX_ROUTING_SNAPSHOT:?set KTX_ROUTING_SNAPSHOT to a sanitized non-live snapshot}
test -f "$snapshot"
root=$(mktemp -d "${TMPDIR:-/tmp}/ktx-routing.XXXXXX")
trap 'rm -rf "$root"' EXIT HUP INT TERM
install -m 0600 "$snapshot" "$root/routing-characterization.db"
sqlite3 -readonly "$root/routing-characterization.db" \
  < tools/bench/evidence/routing-candidate-characterization.sql
```

The retained database is append-only for source records. Both source-call and
compressed-artifact cutoffs are part of the query so later records and later
artifacts cannot change this snapshot.

## Findings

Current routing remains a pure first-match rule over configured metadata. The
11 prescribed cases confirm tool, command-prefix, subcommand, and exit-status
matches; order-dependent overlap; default fallback; catch-all behavior; and
malformed-input handling.

Candidate content outcomes are less complete than implemented metadata routing:

| Outcome across six retained calls     | Calls |
| ------------------------------------- | ----: |
| No candidate content signal           |     3 |
| One candidate content signal          |     2 |
| Overlapping candidate content signals |     1 |

The overlap is a complete JSON document that also contains instruction markers.
A separate grep record contains diff markers. These observations show that
candidate signals can conflict with tool metadata or each other; they do not
decide which signal should win or whether either signal should ship.

The benchmark fixtures add one log-marker case and one six-record NDJSON case.
Fixture task metadata supplies candidate mappings for otherwise signal-free
synthetic source and grep fixtures. Memory/session has no prescribed supporting
case in this finite manifest and remains unevidenced here.

Residual-spend figures use `ceil(characters / 4)` per call and are estimates.
They place retained `read`, `grep`, and `bash git` groups ahead of the other
frozen groups by delivered estimated tokens. The separately retained runtime
events report literal zero cost for ordinals 2, 3, and 6; those zeros remain
runtime observations. Two zero-cost fields in the OpenCode fixture remain
historical fixture evidence, not current observations.

## Follow-up boundary

The characterization found one product-contract mismatch: configuration
documentation named `success`, `failure`, and `any` as the valid `exit_status`
values, while loading and matching accepted a misspelled value as a wildcard.
The subsequent validation task closed that gap at config load: empty or absent
values normalize to `any`, the three documented non-empty values remain valid,
and every other spelling rejects the configuration before route evaluation.

## Verification

Define `ktx_verify` exactly as shown in the canonical
[database safety contract](database-safety.md#hermetic-development-and-ci-verification),
then run both the focused and full checks through it. Each call gets a new
disposable application root and starts with `KTX_DB` unset.

```bash
ktx_verify go test ./internal/router ./internal/cli \
  -run 'Test(RoutingCandidateCharacterization|DocumentedContractReferenceValidation|PipelineContractDocumentationMatchesBehavior)$' \
  -count=2 -v
ktx_verify mage check
```

The retained manifest's `documented_contract` tuple records the exact
collection-time path, evidence class, and digest; verification requires that
historical tuple to remain unchanged. The evolving current `ktx.example.toml`
has a separate current-byte digest and semantic contract checks, so unrelated
byte drift and stale normalization or token-budget wording both fail without
rewriting retained evidence. Other references continue to verify their retained
hashes against current files. The focused tests also verify every fixture
measurement, run every routing case against current code, check all named
ambiguity and fallback cases, and print one findings digest. Repeating them must
print the same digest.
