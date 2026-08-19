# Historical aggregate study — 2026-08-12 through 2026-08-19 UTC

## Scope and method

This is historical descriptive evidence, not a continuity or default-change result.

- Live source: `~/.local/share/ktx/ktx.db`; `sqlite3 -readonly` with `PRAGMA query_only=ON` reported schema 11 both before and after this study.
- A SQLite online `.backup` from that read-only/query-only connection created a disposable copy under `/tmp/opencode`. The copy reported schema 11, then only that copy was migrated by `./build/ktx db migrate` to schema 13. The copy was queried read-only/query-only for this report.
- Public `stats breakdown` only accepts a trailing `--window`; it obtains its end from `time.Now()` (`internal/cli/stats.go`, lines 360–362). It cannot express these fixed end times. A disposable Python analysis therefore reproduced its current grouping against the migrated copy and never retained raw input or output.
- Semantics reproduced from `internal/db/stats.go` and `internal/cli/stats.go`: output-bearing calls with RFC3339 timestamps in inclusive bounds; earliest artifact by timestamp then ID; `(characters+3)/4` estimates; normalized command/subcommand; profile, route, disposition, and raw-size band grouping; top 100 groups by delivered estimate; exact omitted totals.

Implementation identities: `./build/ktx` `d186bc7f1e1e6d74ec44407cb041057e5b47c29d675364721607621a612d3c88`; `internal/cli/stats.go` `9bbec0d6be5bd9e8cbefbf90cdd503c6d8f736d62bfd28a40aa96e88c54ba330`; `internal/db/stats.go` `3170d6b034b478193c23fb547f12b34f0d120a3c2ad0f5c4039df3ae4b451475`; `internal/router/router.go` `d94a2640f169f010d2d973101530ecbc05ea8db87a13142e1993c1bc414b6ef4`. The disposable analyzer digest was `d54f4199ca0865790672045a37e7332aff3efde4b07d237c607cddd8731c2331`.

No database checksum, raw input, output, content hash, or content-derived label is retained.

## Fixed aggregate results

| Window (inclusive UTC)                    |  Calls | Artifacts | Raw estimate | Delivered estimate | Distinct groups | Omitted groups |
| ----------------------------------------- | -----: | --------: | -----------: | -----------------: | --------------: | -------------: |
| 2026-08-15T00:00:00Z–2026-08-18T00:00:00Z | 25,146 |     6,886 |   37,517,002 |         25,592,434 |             143 |             43 |
| 2026-08-12T00:00:00Z–2026-08-19T00:00:00Z | 36,866 |    10,305 |   55,979,792 |         37,096,629 |             172 |             72 |

The 72-hour and seven-day call counts match the independent read-only preflight counts.

### Reconciliation

Each line reconciles `detailed + omitted = complete` for calls, artifacts, raw estimate, and delivered estimate.

| Window    | Detailed calls/artifacts/raw/delivered    | Omitted calls/artifacts/raw/delivered | Complete check |
| --------- | ----------------------------------------- | ------------------------------------- | -------------- |
| 72-hour   | 24,977 / 6,866 / 37,452,525 / 25,567,903  | 169 / 20 / 64,477 / 24,531            | pass           |
| seven-day | 36,194 / 10,239 / 55,743,460 / 36,997,374 | 672 / 66 / 236,332 / 99,255           | pass           |

Target-tool disposition totals below likewise reconcile their detailed and omitted portions. Fields are calls / artifacts / raw estimate / delivered estimate.

| Window    | Tool | Disposition             | Detailed                               | Omitted                   | Complete                               |
| --------- | ---- | ----------------------- | -------------------------------------- | ------------------------- | -------------------------------------- |
| 72-hour   | bash | anti_worsening_rejected | 7,308 / 0 / 3,668,379 / 3,668,379      | 21 / 0 / 1,997 / 1,997    | 7,329 / 0 / 3,670,376 / 3,670,376      |
| 72-hour   | bash | artifact_persisted      | 1,822 / 1,822 / 7,753,307 / 3,660,790  | 6 / 6 / 40,836 / 2,275    | 1,828 / 1,828 / 7,794,143 / 3,663,065  |
| 72-hour   | bash | intentional_passthrough | 813 / 0 / 901,533 / 901,533            | 66 / 0 / 7,422 / 7,422    | 879 / 0 / 908,955 / 908,955            |
| 72-hour   | grep | anti_worsening_rejected | 2,102 / 0 / 662,313 / 662,313          | 0 / 0 / 0 / 0             | 2,102 / 0 / 662,313 / 662,313          |
| 72-hour   | grep | artifact_persisted      | 1,197 / 1,197 / 3,778,844 / 1,291,734  | 0 / 0 / 0 / 0             | 1,197 / 1,197 / 3,778,844 / 1,291,734  |
| 72-hour   | read | anti_worsening_rejected | 5,087 / 0 / 4,276,104 / 4,276,104      | 0 / 0 / 0 / 0             | 5,087 / 0 / 4,276,104 / 4,276,104      |
| 72-hour   | read | artifact_persisted      | 2,327 / 2,327 / 8,632,406 / 4,086,893  | 0 / 0 / 0 / 0             | 2,327 / 2,327 / 8,632,406 / 4,086,893  |
| seven-day | bash | anti_worsening_rejected | 9,687 / 0 / 4,779,074 / 4,779,074      | 158 / 0 / 13,073 / 13,073 | 9,845 / 0 / 4,792,147 / 4,792,147      |
| seven-day | bash | artifact_persisted      | 2,654 / 2,654 / 11,836,361 / 5,038,070 | 41 / 41 / 87,674 / 12,859 | 2,695 / 2,695 / 11,924,035 / 5,050,929 |
| seven-day | bash | intentional_passthrough | 1,252 / 0 / 1,324,166 / 1,324,166      | 97 / 0 / 9,880 / 9,880    | 1,349 / 0 / 1,334,046 / 1,334,046      |
| seven-day | grep | anti_worsening_rejected | 3,540 / 0 / 1,066,152 / 1,066,152      | 0 / 0 / 0 / 0             | 3,540 / 0 / 1,066,152 / 1,066,152      |
| seven-day | grep | artifact_persisted      | 1,881 / 1,881 / 5,751,723 / 2,033,894  | 2 / 2 / 58,338 / 3,689    | 1,883 / 1,883 / 5,810,061 / 2,037,583  |
| seven-day | read | anti_worsening_rejected | 7,289 / 0 / 6,279,747 / 6,279,747      | 0 / 0 / 0 / 0             | 7,289 / 0 / 6,279,747 / 6,279,747      |
| seven-day | read | artifact_persisted      | 3,454 / 3,454 / 13,150,382 / 6,192,581 | 0 / 0 / 0 / 0             | 3,454 / 3,454 / 13,150,382 / 6,192,581 |

## Separate runtime-cache coverage

This is session-telemetry evidence, not tool-delivery evidence. The current summary contract selected `opencode` 1.18.5 observations from `event_hook` and `wrapper_stream` only.

| Window    | Contributing model steps | First capture                  | Last capture                   | Token-field aggregate       |
| --------- | -----------------------: | ------------------------------ | ------------------------------ | --------------------------- |
| 72-hour   |                   13,351 | 2026-08-15T08:33:14.067872171Z | 2026-08-17T21:42:40.914561522Z | measured 96.3025514356881%  |
| seven-day |                   18,187 | 2026-08-12T00:03:16.653528323Z | 2026-08-18T20:47:28.322748851Z | measured 95.85106173373704% |

Capture bounds show only the observed telemetry subset. They establish neither continuous collection nor provider-internal cache behavior, request boundaries, billing, isolation, hits, misses, fills, or eviction.

## Interpretation boundary

Continuity is unknown: schema 11 had no retained collection-continuity lifecycle evidence, and schema-13 migration creates empty metadata tables without backfill. These fixed historical aggregates therefore do not support a shipped-default claim. Defaults remain unchanged; any proposal requires a future fully elapsed post-deployment interval with authoritative continuity and closure evidence.
