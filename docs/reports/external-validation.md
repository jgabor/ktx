# Historical external campaign identity

The maintained boundary is a direct tracked lock and a Go standard-library
comparison. The check does not run an evaluator, authenticate, install software,
call a provider or model, access a database, or modify the repository.

## Maintained tracked check

[`tools/bench/external-identity/lock.json`](../../tools/bench/external-identity/lock.json)
records the exact historical campaign, evaluator, task, runtime, adapter, KTX,
execution, and digest identities. Its claim block limits the evidence to one
task, eight attempts, and four complete pairs. It establishes neither official
suite identity nor causal or general quality, provider cost, repeatable external
execution, or permission to execute.

Run the complete tracked check from the repository root:

```bash
go test -count=1 ./tools/bench/external-identity
```

To compare one observed identity directly:

```bash
identity_check="$(mktemp -d)/external-identity"
go build -o "$identity_check" ./tools/bench/external-identity
"$identity_check" --observed \
  tools/bench/external-identity/testdata/matching.json
```

The comparator embeds the lock and reads only the named regular, non-symlink
input. It rejects unreadable or oversized input, invalid UTF-8, duplicate JSON
members, non-finite numbers, trailing data, missing or extra fields, wrong
types, and excess nesting. A value mismatch names the field and required value
without printing the observed value.

| Exit | Meaning                                    |
| ---: | ------------------------------------------ |
|    0 | Identity matches or help was shown         |
|    2 | Identity value mismatch                    |
|   64 | Usage error                                |
|   65 | Observed identity unavailable or malformed |
|   70 | Tracked direct lock invalid                |

## Archived evidence

[`tools/bench/archive/skillsbench-campaign/`](../../tools/bench/archive/skillsbench-campaign/)
contains exact historical source, scripts, locks, and proof snapshots with inert
`.txt` suffixes. They are not packages, executable scripts, or routine checks.
The archive [manifest](../../tools/bench/archive/skillsbench-campaign/manifest.json)
maps former paths to archived bytes and unchanged SHA-256 digests.

Manual recovery is archival only. It requires a disposable checkout, restored
former paths, the exact ignored inputs required by that procedure, and any
required historical machine-local roots. The
[archive README](../../tools/bench/archive/skillsbench-campaign/README.md) records
that boundary.

Original checksum-bound proof snapshots remain immutable and retain a bounded
set of machine-local values. The
[sanitized derivative](../../tools/bench/evidence/skillsbench-proof-derivative.json)
links their source digests and exposes only bounded tracked facts. It does not
replace raw evidence, corrected metrics, or the snapshots' byte identity.
