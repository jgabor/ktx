# Portable replay fixture provenance

`portable-replay-v1.json` is a deterministic synthetic, benchmark-parented
capture retained for the repository replay gate. It was created after reviewing
all six benchmark-parented captures in the available local database on
2026-07-17. Every candidate was rejected by `ktx bench export` because its
stream-derived usage did not match its benchmark parent, so none was complete
enough to retain.

The synthetic source contains two tool calls and two usage-bearing step
finishes. Its outputs deliberately exercise ANSI removal, repeated blank-line
deduplication, and whitespace normalization. The benchmark parent records 160
input tokens, 32 output tokens, USD 0.0025 cost, explicit zero cache reads, and
absent cache writes. The retained JSON is the canonical version 1 representation
of that source and contains no database, session, benchmark-run, invocation-time,
or machine-local path identifiers.

Privacy review covered every embedded input and output string. All names,
commands, paths, model labels, timestamps, usage, and costs are synthetic. The
fixture contains no user content, credentials, repository content, personal
data, or externally captured tool output.

Run `mage replayFixture` to evaluate this exact file twice and exercise its
strict negative cases through the built-in portable replay path. `mage verify`
is the authoritative repository and push/pull-request CI gate.

Refresh this fixture only from a consistent benchmark-parented source. Export
to a temporary path, verify it, review every embedded string and metadata value,
then replace the retained bytes and update this provenance record:

```bash
KTX_DB=/path/to/ktx.db ktx bench export \
  --output /tmp/portable-replay-v1.json <session-id>
ktx bench run --replay-file /tmp/portable-replay-v1.json
cp /tmp/portable-replay-v1.json internal/cli/testdata/portable-replay-v1.json
mage replayFixture
mage verify
```

Do not hand-edit hashes or expectations. The exporter derives canonical bytes
from the authoritative source; the manual review is required because export
validates evidence consistency but does not redact raw stream content. The
digest detects accidental drift, not coordinated edits to content and expected
values.
