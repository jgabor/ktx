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
strict negative cases through the built-in portable replay path.
