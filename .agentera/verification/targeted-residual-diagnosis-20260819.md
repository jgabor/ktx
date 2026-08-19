# Targeted residual diagnosis — 2026-08-19

## Scope

This correction retains the diagnosis for the exact historical subgroup:

`read / command absent / subcommand absent / default / default_fallback / anti_worsening_rejected / 1k-10k`

The historical disposition is anti-worsening rejection. `default_fallback` identifies how the default profile was selected; by itself it does not prove that a different supported route is safe. No ambient database was accessed or copied. Every command below ran with `KTX_DB` unset and a newly allocated disposable `HOME` plus `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, and `XDG_STATE_HOME`.

## Commands

The local binary was rebuilt before controlled execution:

```sh
env -u KTX_DB HOME="$fresh/home" XDG_DATA_HOME="$fresh/data" \
  XDG_CONFIG_HOME="$fresh/config" XDG_CACHE_HOME="$fresh/cache" \
  XDG_STATE_HOME="$fresh/state" mage build
```

Each controlled case used a separate Python-created `/tmp/ktx-*-*` root, wrote only its generated `ktx.toml` when applicable, and invoked the local build with a flat sidecar event:

```sh
env -u KTX_DB HOME="$fresh/home" XDG_DATA_HOME="$fresh/data" \
  XDG_CONFIG_HOME="$fresh/config" XDG_CACHE_HOME="$fresh/cache" \
  XDG_STATE_HOME="$fresh/state" \
  ~/git/ktx/build/ktx hook < generated-event.json
```

The configured cases also ran `~/git/ktx/build/ktx config show --json` in the same isolated environment to confirm the generated configuration was loaded. Focused validation used another fresh root:

```sh
env -u KTX_DB HOME="$fresh/home" XDG_DATA_HOME="$fresh/data" \
  XDG_CONFIG_HOME="$fresh/config" XDG_CACHE_HOME="$fresh/cache" \
  XDG_STATE_HOME="$fresh/state" \
  go test ./internal/pipeline/... ./internal/router ./internal/config ./internal/cli/...
```

## Controlled inventory and results

All inputs were generated and exactly 6,400 bytes.

| Case                     | Configuration and event                                                                                                                                    |   Delivered | Compressed artifact | Result                                                                                        |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------: | ------------------- | --------------------------------------------------------------------------------------------- |
| No config                | `tool=read`, `is_error=false`, 6,400 `x` bytes                                                                                                             | 6,400 bytes | none                | Fallback passthrough; no compressed artifact.                                                 |
| Default `strip-ansi`     | `[pipeline.default] stages=["strip-ansi"]`; plain 6,400-byte input                                                                                         | 6,400 bytes | none                | Configuration load confirmed; no net reduction, so no compressed artifact.                    |
| Supported metadata route | `tool=read`, `is_error=false`; route with `tool="read"`, `exit_status="success"`, profile `read-head`; profile stage `head:1`; 200 generated 32-byte lines |    32 bytes | present             | Configuration load and route entry confirmed; the first generated line was preserved exactly. |

Observed structured summaries were:

```text
{"case":"no_config","compressed_id":null,"input_bytes":6400,"output_bytes":6400,"stderr":""}
{"case":"default_strip_ansi","compressed_id":null,"config_has_strip_ansi":true,"input_bytes":6400,"output_bytes":6400,"stderr":""}
{"case":"read_success_head1","compressed_id_present":true,"first_line_preserved":true,"input_bytes":6400,"output_bytes":32,"route_config_present":true,"stderr":""}
```

`mage build` exited successfully. Focused pipeline, stage, router, config, CLI, and CLI test-utility packages passed.

## Supported-predicate analysis

Current routing can use retained metadata such as tool name and authoritative outcome. The controlled `tool=read` plus explicit successful flat-sidecar outcome therefore proves supported metadata routability. It also proves that `head:1` can mechanically reduce this generated input.

Current predicates cannot isolate the selected `1k-10k` size band. The subgroup's absent command and subcommand provide no command or subcommand predicate. Content-based routing is prohibited. Consequently the controlled match is broader than the historical subgroup.

The `head:1` result is **not a safety result**. It does not establish that required facts, middle or tail information, structured output, occurrence counts, or failure behavior survive. It also does not establish a recurring opportunity in retained calls.

## Classification and limits

- Historical observed classification: anti-worsening rejection under the default fallback route.
- Controlled no-config classification: fallback passthrough.
- Controlled default `strip-ansi` classification: no net reduction, with the compressed artifact correctly absent.
- Controlled `read`/success route classification: supported metadata route match with mechanical reduction only.

These observations do not convert the historical subgroup into a route miss. They do not justify an intervention, a profile or default change, a rollout, or a later comparison. No historical content examples were examined, and no claim is made about safety, recurring opportunity, continuity, or decision satisfaction.

## Outcome and recovery condition

**Outcome: unknown / recovery required.** This is neither intervention nor no-change.

The exact recovery condition is controlled evidence covering representative **head, middle, tail, structured, and failure** cases for the proposed supported-metadata route and transform, with required facts and failure behavior preserved. Until those cases exist and pass, no route or transform may be selected from this diagnosis.
