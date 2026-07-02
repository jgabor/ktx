# ktx — agent guide

ktx is a runtime-agnostic CLI that makes coding agents measurable and cheaper. One Go binary, no library import, no daemon.

## Key documents

| Document                                               | Role                                                        |
| ------------------------------------------------------ | ----------------------------------------------------------- |
| `[.agentera/vision.yaml](.agentera/vision.yaml)`       | North star, principles, identity                            |
| `[.agentera/decisions.yaml](.agentera/decisions.yaml)` | Recorded deliberation outcomes (reasoning trail)            |
| `[ARCHITECTURE.md](ARCHITECTURE.md)`                   | Aspirational architecture; open deliberations backlog       |
| `[ROADMAP.md](ROADMAP.md)`                             | Milestone sequencing (M0–M5)                                |
| `[TODO.md](TODO.md)`                                   | PoC/MVP blockers cross-linked to architecture deliberations |

**Read order for new work:** vision → architecture (intent) → decisions (what is already chosen) → roadmap → todo.

`ARCHITECTURE.md` is the map of intent, not the blueprint. `.agentera/decisions.yaml` holds choices made in resonera. Do not treat aspirational architecture prose as settled design.

## Documentation maintenance

When an open deliberation item is resolved (in resonera or equivalent structured discussion):

1. **Record the choice** in `[.agentera/decisions.yaml](.agentera/decisions.yaml)` — append a decision entry with question, alternatives, choice, reasoning, and confidence.
2. **Update `[ARCHITECTURE.md](ARCHITECTURE.md)`** — remove the item from [Open deliberations](ARCHITECTURE.md#open-deliberations); add a short entry under a **Decided** subsection (one paragraph per decision). Keep implementation detail out of ARCHITECTURE unless the decision _is_ the implementation contract.
3. **Update `[ROADMAP.md](ROADMAP.md)`** — adjust milestone acceptance criteria if the decision changes scope or definition of done.
4. **Update `[TODO.md](TODO.md)`** — mark matching blockers complete; do not leave duplicate open items when the architecture backlog and todo list describe the same question.

When implementing before a deliberation is recorded, prefer opening a resonera session over encoding guesses in code comments or architecture prose.

When adding a new open question, add it to [ARCHITECTURE.md — Open deliberations](ARCHITECTURE.md#open-deliberations) first; add a cross-linked line in `[TODO.md](TODO.md)` only if it blocks PoC or MVP delivery.

## Working conventions

- **Boring before clever** — stdlib first, no ML deps, predictable behavior over maximal compression.
- **Cost is the contract** — evidence persisted to the database; numbers not adjectives.
- **Reversibility before aggression** — originals recoverable on demand before stronger compression.
- **Router wins, algorithm loses** — stable CLI surface; dispatch and measurement beat owning the best compressor in every category.
- **Do not relitigate** choices marked firm in `.agentera/decisions.yaml` unless the user explicitly requests a revisit.
- Deliberation outcomes belong in `.agentera/decisions.yaml`, not only in chat or commit messages.

## Testing

### Hook validation

`scripts/validate-hook.sh` runs a real OpenCode session with the ktx plugin, a configurable pipeline, and a pinned model, then queries the DB for A/B compression numbers.

```bash
# Default pipeline (strip-ansi + grep-v + keep-ends + token-budget)
scripts/validate-hook.sh

# Custom model and prompt
scripts/validate-hook.sh --model neuralwatt/glm-5.2-short-fast --prompt "Run: seq 1 500"

# Keep workdir for inspection
scripts/validate-hook.sh --keep-workdir

# Custom pipeline stages
scripts/validate-hook.sh --pipeline "strip-ansi,keep-ends:50:20,token-budget:2000"

# Use a pre-written ktx.toml
scripts/validate-hook.sh --ktx-toml path/to/ktx.toml
```

**Model policy:** use `neuralwatt/*` models only. Never use `github-copilot/*` models — they are on an enterprise plan and not permitted for this repo.

### Benchmark validation

`scripts/validate-bench.sh` validates wrapper capture against real opencode (D16/D17 five-layer gate). See the script header for usage.
