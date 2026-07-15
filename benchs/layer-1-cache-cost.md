# Layer 1 cache and cost benchmark

## Question

Does the Layer 1 shell-output compression used in the live ktx configuration reduce provider-reported cache-read token volume or make model calls more expensive?

## Setup

The benchmark compared no compression with the full live configuration. The live default pipeline was:

```toml
[pipeline.default]
timeout = "2s"
recover_footer = true
stages = ["unwrap-result", "strip-ansi", "dedup-blank", "normalize", "keep-ends:100:50"]
```

The candidate also included the live `git`, `ls`, `find`, and search routes. The exact candidate file and its hash are retained with the experiment.

- Task: `ephemeral-midfact-v1`
- Baseline: `tools/bench/pipelines/passthrough.toml`
- Candidate: `~/.config/ktx/ktx.toml`
- Model: `openai/gpt-5.6-luna`, variant `high`
- Runtime: OpenCode 1.18.0
- Methodology: `v5-cache-aware`
- Cache control: isolated baseline and candidate namespaces
- Attempts per side: one cold observation, one warm-up, and three measured runs

The task prints about 5,000 lines through the shell. It places one required fact in the middle and another at the end. This makes compression run and reveals retries or recovery work.

## Command

```bash
ktx prices update

ktx bench compare \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task-file tools/bench/tasks/ephemeral-midfact-v1.toml \
  --baseline-config tools/bench/pipelines/passthrough.toml \
  --candidate-config "$HOME/.config/ktx/ktx.toml" \
  --methodology tools/bench/methodology-v5-cache-aware.yaml \
  --cache-control isolated \
  --warmup 1 \
  --rounds 3 \
  --max-attempts 16
```

## Results

These are means from the three measured runs per side.

| Metric                            | No compression | Live configuration |       Change |
| --------------------------------- | -------------: | -----------------: | -----------: |
| Cache-read tokens                 |         11,264 |             18,261 |       +62.1% |
| Cache-read share of logical input |          34.7% |              81.9% | +47.2 points |
| Uncached input tokens             |         21,227 |              4,043 |       -81.0% |
| Total logical input tokens        |         32,491 |             22,305 |       -31.4% |
| Estimated API cost                |      $0.024755 |          $0.009943 |       -59.8% |
| Output tokens                     |            180 |                211 |       +17.2% |
| Reasoning tokens                  |            220 |                468 |      +112.4% |
| Tool calls                        |              1 |                  2 |        +100% |
| Fact score                        |            1.0 |                1.0 |    no change |
| Pass rate                         |           100% |               100% |    no change |

All three paired runs favored compression. Estimated savings per run were $0.016598, $0.014032, and $0.013804.

The live configuration had a larger cold-to-warm estimated-cost difference: about $0.003930, compared with $0.000173 for the baseline. This difference can include changes in reasoning and output, so it is not solely a cache-rebuild cost.

Including cold and warm-up calls, the candidate was already cheaper before measured runs began: $0.028103 versus $0.049675. It remained cheaper after the first measured turn: $0.036315 versus $0.074485.

## Conclusion

This experiment did not reduce total provider-reported cache-read token volume per completed task. The compressed runs read more tokens from cache, and the cache-read share increased from 34.7% to 81.9%. The experiment does not measure cache-hit count or a cache hit/miss ratio.

Compression also did not raise estimated cost. Estimated API cost fell by about 60%, while answer quality stayed unchanged. The candidate used one extra tool call and more reasoning tokens, but that overhead was smaller than the input-token savings.

This is an exploratory result because there were three measured runs per side. It is a useful indication, not a general claim about every task or model.

## Evidence and validation

- Experiment: `20260715T135419Z-3012214-1979487`
- Evidence: `.bench-data/20260715T135419Z-3012214-1979487/`
- Repository commit used by the run: `3012214`

Validate the retained files with:

```bash
ktx bench validate \
  --experiment .bench-data/20260715T135419Z-3012214-1979487
```

Validation result: all ten attempts valid, summary bytes matched, and the measured target was met for both sides.
