# Runtime outcome contract evidence

Scope: Agentera plan `caiaxiakry`, task `yoybjmqzxt`. This is a pinned
contract record, not a shipped product contract or an implementation change.

## Classification rules

- **Authoritative** means the pinned runtime or an authority-establishing caller
  defines the field's outcome meaning.
- **Unsupported** means the pinned contract does not expose that outcome signal.
- **Unknown** means a field can occur but its outcome meaning is not established,
  or authoritative evidence is absent, malformed, from an unsupported producer,
  or otherwise unusable. Unknown is neither success nor failure.

Provider, model, and variant labels do not change field meaning or strengthen
provenance. On an eligible retry, the first accepted metadata wins; later
outcome metadata does not enrich or override it
([implementation contract](../../internal/evidence/evidence.go#L19-L31),
[positive/conflicting example](../../internal/evidence/evidence_test.go#L574-L618)).

## OpenCode wrapper stream

Version authority: OpenCode 1.18.5, commit
[`e5cc278dec9294a627a7b05f47ce6a564408c1a2`](https://github.com/anomalyco/opencode/tree/e5cc278dec9294a627a7b05f47ce6a564408c1a2).
The same pin is retained locally with the rule that configured provider/model
context cannot supply runtime field meaning
([contract provenance](../../docs/cache-reuse-metric-contract.md#L20-L43)).

| Candidate signal                                                                       | Classification                               | Contract boundary                                                                                                                                                                                                                                                                                                                                                                                     |
| -------------------------------------------------------------------------------------- | -------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `part.state.status == "error"`                                                         | Authoritative failure                        | The `ToolState` union defines terminal `error`, and the JSON runner emits it through the tool-error path ([union](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/sdk/js/src/gen/types.gen.ts#L237-L301), [runner](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/cli/cmd/run.ts#L715-L727)). |
| `part.state.status == "completed"`                                                     | Authoritative terminal completion            | The same union and runner distinguish completed from error. Completion alone is success only after the precedence rule below.                                                                                                                                                                                                                                                                         |
| Numeric `part.state.metadata.exit` from the supported built-in terminal/shell producer | Authoritative diagnostic process exit        | The pinned shell producer returns numeric `exit` in result metadata ([shell result](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/tool/shell.ts#L575-L594)). Zero means process success; nonzero means process failure. This authority is producer-scoped, not a meaning assigned to arbitrary metadata.                                  |
| Missing or nonnumeric `metadata.exit`, or `metadata.exit` from another producer        | Unknown                                      | The generic `ToolState` metadata type is unconstrained. Presence or a runtime label cannot establish process-exit semantics.                                                                                                                                                                                                                                                                          |
| `pending`, `running`, missing, or malformed `state.status`                             | Unsupported as a terminal outcome or unknown | Nonterminal states do not decide an outcome; missing or malformed terminal evidence is unknown.                                                                                                                                                                                                                                                                                                       |

Canonical precedence is: terminal `status == "error"` means failure; otherwise a
valid numeric exit from a supported terminal/shell producer decides success or
failure; otherwise terminal `status == "completed"` means success; otherwise the
outcome is unknown. Thus a conflicting `status == "error"` and `exit == 0` is
failure. A valid nonzero supported exit prevents `completed` from being treated
as success.

Retained examples cover both sides. A completed shell result with exit zero is
retained in
[`tools/bench/fixtures/opencode-tool-use.jsonl:2`](../../tools/bench/fixtures/opencode-tool-use.jsonl#L2).
Parser cases retain exit 1, exit 0, and missing exit; their payloads also show
that a missing status must not be reconstructed
([`parser_test.go:494-534`](../../internal/parser/parser_test.go#L494-L534)).

## OpenCode `tool.execute.after` hook

Version authority is the same OpenCode 1.18.5 commit. The hook contract exposes
only `{title, output, metadata}` and does not expose `ToolState` or status
([hook type](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/plugin/src/index.ts#L274-L281)).

| Candidate signal                                                                           | Classification                        | Contract boundary                                                                                                             |
| ------------------------------------------------------------------------------------------ | ------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| Numeric `output.metadata.exit` from the pinned supported terminal/shell producer           | Authoritative diagnostic process exit | The shell producer defines the value; the generic after-hook merely carries it. Zero means success and nonzero means failure. |
| Hook status / `ToolState`                                                                  | Unsupported                           | The hook callback has no such field. Do not import wrapper-stream status semantics into this shape.                           |
| Missing or malformed `output.metadata.exit`, or arbitrary metadata from any other producer | Unknown                               | Generic hook metadata has no universal outcome semantics.                                                                     |

The generated adapter retains the exact generic hook output rather than a
`ToolState` ([adapter fixture](../../internal/cli/init_opencode_adapter_test.go#L296-L298)).
That fixture's `{kept:true}` metadata is an absent outcome case. The pinned shell
result above is the positive numeric-exit producer case. Together they show why
producer identity, valid type, and presence are all required.

## Codex PostToolUse

Version authority: Codex CLI 0.144.4, tag `rust-v0.144.4`, peeled commit
[`8c68d4c87dc54d38861f5114e920c3de2efa5876`](https://github.com/openai/codex/tree/8c68d4c87dc54d38861f5114e920c3de2efa5876).
The retained pin and source set are recorded at
[`runtime-identity-evidence.md:47-63`](runtime-identity-evidence.md#L47-L63):
the [input schema](https://github.com/openai/codex/blob/8c68d4c87dc54d38861f5114e920c3de2efa5876/codex-rs/hooks/schema/generated/post-tool-use.command.input.schema.json),
[request serialization](https://github.com/openai/codex/blob/8c68d4c87dc54d38861f5114e920c3de2efa5876/codex-rs/hooks/src/events/post_tool_use.rs#L139-L162),
[runtime success gate and construction](https://github.com/openai/codex/blob/8c68d4c87dc54d38861f5114e920c3de2efa5876/codex-rs/core/src/hook_runtime.rs#L258-L295),
and [successful-output dispatch](https://github.com/openai/codex/blob/8c68d4c87dc54d38861f5114e920c3de2efa5876/codex-rs/core/src/tools/registry.rs#L570-L598).

| Candidate signal                                     | Classification        | Contract boundary                                                                                                                                     |
| ---------------------------------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| Trusted, schema-conforming PostToolUse occurrence    | Authoritative success | The runtime computes success and invokes PostToolUse only after the tool produced successful output. Event occurrence is the outcome signal.          |
| Universal nested PostToolUse outcome field           | Unsupported           | The pinned schema defines `tool_response` as unconstrained JSON and gives no nested field universal outcome meaning.                                  |
| `tool_response.exit_code`                            | Unknown               | It is a possible response shape, not a contract-defined outcome meaning. A numeric value cannot override or weaken event-level authoritative success. |
| Any other nested `tool_response` error/success field | Unknown               | The same unconstrained boundary applies. Arbitrary nested content does not override or strengthen the event-level outcome.                            |

A trusted PostToolUse occurrence is therefore measured success even when
`tool_response` is a bare value, omits outcome-looking fields, or contains
misleading or malformed nested values. A missing or schema-invalid event is not
an authoritative occurrence and remains unknown. Nested response content never
changes either classification.

**Execute-or-supersede decision: EXECUTE.** Plan task `gzjilulvyv` must narrowly
normalize a trusted, schema-conforming PostToolUse occurrence to explicit
success. It must not read or infer outcome from arbitrary nested
`tool_response` fields. Missing or malformed PostToolUse input remains unknown.

## Flat and generic sidecar input

An explicit `is_error` is authoritative only as a caller assertion after the
caller has established its authority. `exit_code` is diagnostic and cannot
override explicit `is_error`; a `runtime` label does not strengthen either
field. Precedence is explicit authoritative `is_error`, then a valid diagnostic
exit only when the authority-establishing caller's contract permits that
derivation, otherwise unknown. Missing or malformed fields remain unknown.

Positive and absent examples are retained in the parser tests
([`parser_test.go:536-573`](../../internal/parser/parser_test.go#L536-L573)) and
the sidecar write round-trip
([`write_test.go:255-307`](../../internal/cli/write_test.go#L255-L307)). These
examples prove shape handling; they do not let an untrusted caller self-assert
authority by adding a runtime label.

## Implementation boundary for later tasks

Later ingestion and reporting work may normalize only the authoritative cases
above. It must preserve measured success, measured failure, and unknown as
distinct states. Absence, malformed data, unsupported producers, arbitrary
metadata, and nested Codex response fields remain unknown. A trusted,
schema-conforming Codex PostToolUse occurrence is explicit success regardless of
nested response content. No later retry may enrich first-accepted metadata, and
no provider/model selection may change these rules.

## Retained contract examples and offline check

[`runtime-outcome-examples.json`](runtime-outcome-examples.json) retains ten
bounded examples under the exact runtime pins above. They include:

- OpenCode wrapper `error` and completed terminal states with absent exit, plus
  absent status with valid shell exit;
- OpenCode after-hook valid and malformed shell exits, with status unsupported;
- schema-conforming Codex PostToolUse success with misleading nested fields and
  with no nested outcome field, plus malformed PostToolUse missing a required
  field;
- caller-authoritative sidecar `is_error` conflicting with diagnostic exit and
  malformed sidecar evidence.

The independent project-memory checker applies the precedence and schema
boundary in this record without invoking KTX, a runtime, model, network, or
database:

```sh
go run .agentera/verification/runtime_outcome_examples_check.go \
  .agentera/verification/runtime-outcome-examples.json
```
