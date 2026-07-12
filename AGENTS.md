# ktx — agent guide

ktx is a runtime-agnostic CLI that makes coding agents measurable and cheaper. One Go binary, no library import, no daemon.

## Key documents

| Document                                               | Role                                                        |
| ------------------------------------------------------ | ----------------------------------------------------------- |
| `[.agentera/vision.yaml](.agentera/vision.yaml)`       | North star, principles, identity                            |
| `[.agentera/decisions.yaml](.agentera/decisions.yaml)` | Recorded deliberation outcomes (reasoning trail)            |
| `[ARCHITECTURE.md](ARCHITECTURE.md)`                   | Aspirational architecture; open deliberations backlog       |
| `[TODO.md](TODO.md)`                                   | PoC/MVP blockers cross-linked to architecture deliberations |

**Read order for new work:** vision → architecture (intent) → decisions (what is already chosen) → todo.

`ARCHITECTURE.md` is the map of intent, not the blueprint. `.agentera/decisions.yaml` holds choices made in resonera. Do not treat aspirational architecture prose as settled design.

## Documentation maintenance

When an open deliberation item is resolved (in resonera or equivalent structured discussion):

1. **Record the choice** in `[.agentera/decisions.yaml](.agentera/decisions.yaml)` — append a decision entry with question, alternatives, choice, reasoning, and confidence.
2. **Update `[ARCHITECTURE.md](ARCHITECTURE.md)`** — remove the item from [Open deliberations](ARCHITECTURE.md#open-deliberations). No "Decided" entry is added; `decisions.yaml` is the single source of resolved reasoning. Only amend the conceptual sections above the Open-deliberations backlog when the decision changes the architectural map itself.
3. **Update `[TODO.md](TODO.md)`** — mark matching blockers complete; do not leave duplicate open items when the architecture backlog and todo list describe the same question.

When implementing before a deliberation is recorded, prefer opening a resonera session over encoding guesses in code comments or architecture prose.

When adding a new open question, add it to [ARCHITECTURE.md — Open deliberations](ARCHITECTURE.md#open-deliberations) first; add a cross-linked line in `[TODO.md](TODO.md)` only if it blocks PoC or MVP delivery.

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
mage install    # go install to GOPATH/bin
```

Always use `mage build` or `go build -o build/ktx ./cmd/ktx`. Never run bare `go build ./cmd/ktx` — it drops the binary in the repo root.

## Testing

### Live comparison

```bash
ktx bench compare \
  --model neuralwatt/glm-5.2-short-fast-flex \
  --task-file tools/bench/tasks/crossfile-nav-v1.toml \
  --rounds 3
```

**Model policy:** use `neuralwatt/*` models only. Never use `github-copilot/*` models — they are on an enterprise plan and not permitted for this repo.

### Evidence validation

```bash
ktx bench validate --experiment .bench-data/<experiment-id>
```

The validator checks retained artifacts and hashes, session-linked attempts,
target completion counts, and byte-for-byte summary regeneration.
