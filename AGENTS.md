# ktx — agent guide

ktx is a runtime-agnostic CLI that makes coding agents measurable and cheaper. One Go binary, no library import, no daemon.

## Key documents

| Document                                               | Role                                                           |
| ------------------------------------------------------ | -------------------------------------------------------------- |
| `[.agentera/vision.yaml](.agentera/vision.yaml)`       | North star, principles, identity                               |
| `[.agentera/decisions.yaml](.agentera/decisions.yaml)` | Recorded deliberation outcomes (reasoning trail)               |
| `[ARCHITECTURE.md](ARCHITECTURE.md)`                   | Conceptual architecture and unresolved architectural questions |
| `[TODO.md](TODO.md)`                                   | Canonical executable backlog, including deliberation work      |

**Read order for new work:** vision → architecture (intent) → decisions (what is already chosen) → todo.

`ARCHITECTURE.md` is the map of intent, not an implementation backlog or blueprint. `.agentera/decisions.yaml` holds choices made in resonera. `TODO.md` owns all executable implementation, evidence, benchmark, documentation, verification, and deliberation work. Do not treat conceptual architecture prose or unresolved questions as settled design.

## Documentation maintenance

When adding an unresolved architectural question:

1. **Add the question** to [ARCHITECTURE.md — Open discussions](ARCHITECTURE.md#open-discussions) as a named heading and neutral question block. Do not encode a preferred implementation.
2. **Add one discussion task** to [TODO.md — Architectural discussions](TODO.md#architectural-discussions) with a stable explicit HTML anchor and reciprocal links between the question and task.

When an open question is resolved in resonera or equivalent structured discussion:

1. **Record the choice** in `[.agentera/decisions.yaml](.agentera/decisions.yaml)` — append a decision entry with question, alternatives, choice, reasoning, and confidence.
2. **Update `[ARCHITECTURE.md](ARCHITECTURE.md)`** — remove the resolved question. No "Decided" entry is added; `decisions.yaml` is the single source of resolved reasoning. Amend conceptual sections only when the choice changes the architectural map.
3. **Update `[TODO.md](TODO.md)`** — complete the matching deliberation task and add or unblock only the implementation, evidence, benchmark, documentation, or verification work selected by the choice.

Do not implement an unresolved architectural choice. Run its deliberation task rather than encoding a guess in code comments, TODO wording, or architecture prose.

## Working conventions

- **Boring before clever** — stdlib first, no ML deps, predictable behavior over maximal compression.
- **Cost is the contract** — evidence persisted to the database; numbers not adjectives.
- **Reversibility before aggression** — originals recoverable on demand before stronger compression.
- **Router wins, algorithm loses** — stable CLI surface; dispatch and measurement beat owning the best compressor in every category.
- **Do not relitigate** choices marked firm in `.agentera/decisions.yaml` unless the user explicitly requests a revisit.
- Deliberation outcomes belong in `.agentera/decisions.yaml`, not only in chat or commit messages.

## Project-memory boundary

Internal task-tracking markers belong ONLY in project-memory artifacts: `.agentera/decisions.yaml`, `.agentera/plan.yaml`, `.agentera/progress.yaml`, `.agentera/vision.yaml`, `.agentera/objective.yaml`, `ARCHITECTURE.md`, `TODO.md`, `AGENTS.md`, and other files under `.agentera/` or at the repo root dedicated to project state.

They MUST NOT appear in shipped product code, scripts, TOML configs, user-facing strings, help text, error messages, test fixtures, the Dockerfile, or any artifact outside the project-memory layout. Markers covered by this rule:

- Decision IDs: bare tokens like `D14`, `D22` and expansions like `Decision 1`, `Decision 12`.
- Milestone IDs: `M1`–`M7` and equivalents.
- Phase tokens: `Phase 1`, `Phase 2`, `Phase 3` when referring to project phases (not technical "two-phase commit").
- Acceptance-criteria tokens: `AC1`–`AC9`, `acceptance criteria N`.
- Plan/Task refs: `Plan Task 4`, `plan Task 5`, `Task 1` when used as plan tracking.
- `.agentera/` artifact path references: `decisions.yaml`, `progress.yaml`, `plan.yaml`, `health.yaml`, `vision.yaml`, `objective.yaml`, `experiments.yaml`, `docs.yaml` cited from product code.
- Decision-mode labels: `firm decision`, `provisional decision`, `exploratory decision`.
- Migration-vintage labels in user-facing errors: `pre-M3 database`, `pre-D20 database`, and similar.
- The word `agentera` in shipped code or user-facing strings.

Comments should describe the actual behavior. If the reader needs rationale, they consult `.agentera/decisions.yaml`. When implementing changes for a tracked decision, write code that reads naturally on its own; cross-reference the decision only in deliverables in `.agentera/` (e.g. `decisions.yaml` satisfaction review) — never inside a Go comment, help text, or shell-script header.

## Building

```bash
mage build      # builds to build/ktx
mage check      # test + vet + lint + build (default target)
```

Always use `mage build` or `go build -o build/ktx ./cmd/ktx`. Never run bare `go build ./cmd/ktx` — it drops the binary in the repo root.

## Database safety

Follow [`docs/database-safety.md`](docs/database-safety.md) for the canonical
DB-free, readiness, post-readiness read-only, isolated-owner, and forbidden
ambient-live command classes. During development, tests, CI reproduction, and
migration rehearsal:

- never access the ambient live database;
- unset `KTX_DB` for each verification invocation;
- allocate a new disposable `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`,
  `XDG_CACHE_HOME`, and `XDG_STATE_HOME` per invocation; and
- rehearse migration only against an explicitly seeded temporary database.

Build and verify `./build/ktx`, then rehearse migration against an explicitly
seeded temporary database. Every database-backed surface automatically prepares
a supported older schema; query and inspection operations are read-only after
readiness. `ktx db migrate` remains a diagnostic and rehearsal command, not a
normal prerequisite. `KTX_DB` selects an explicit path but does not prove
disposability. A database newer than the binary is recovered by rolling the
runtime forward, never by lowering `user_version`.

Schema releases also require the production-scale gate outside routine
`mage check`. Prepare the disposable 3.2 GB fixture as documented in
`docs/schema-readiness.md`, then run:

```bash
env -u KTX_DB \
  KTX_SCHEMA_RELEASE_WORK_ROOT="$root/work" \
  KTX_SCHEMA_RELEASE_REPORT="docs/notes/schema-readiness-release.json" \
  mage schemaReleaseGate
```

That path is passing authority only after a complete pass;
the `.attempt.failed.json` sibling is the latest failed attempt, not authority.

## Testing

### Live comparison

The explicit per-attempt owner and fail-closed event collector pass offline
preflight. The retained OpenCode 1.18.5 Luna/high v3 experiment also passes
retained-artifact validation with 14/14 valid attempt checks, five complete
measured pairs, and 5/5 valid measured targets per side. Its scoped result and
the two unchanged failed historical archives are recorded in
`docs/cache-reuse-live-validation.md`. Duplicate handling is regression-tested;
no duplicate callback occurred live in v3.

Current prompt-cache comparisons use methodology v6 and emit summary schema 5
under `cache-reuse-v2`. Claims about provider-internal behavior remain forbidden.
Methodology v5, summary schemas 1–4, `cache-reuse-v1`, and the retained v3 result
remain historical evidence.

```bash
./build/ktx bench compare --preflight --build --format json

./build/ktx bench compare \
  --build \
  --methodology tools/bench/specs/prompt-cache-token-hit-rate.yaml \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task-file tools/bench/tasks/crossfile-nav.toml \
  --cache-control observed \
  --warmup 1 \
  --rounds 5
```

**Model policy:** any provider is allowed except `github-copilot/*` models — they are on an enterprise plan and not permitted for this repo. The CLI rejects `github-copilot/*` at validation time.

### Evidence validation

```bash
./build/ktx bench validate --experiment .bench-data/<experiment-id>
```

The validator checks retained artifacts and hashes, session-linked attempts,
target completion counts, and byte-for-byte summary regeneration.
