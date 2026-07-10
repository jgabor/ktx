#!/usr/bin/env python3
"""Generate the deterministic planted-fact evaluation corpus.

The corpus is designed so different pipeline stages fail differently:
- head facts survive head:N and keep-ends
- mid facts are lost by head:N and narrow keep-ends windows
- tail facts survive keep-ends but not pure head:N
- log-tail errors survive keep-ends but not head:N
- grep needles are one matching line among noise

Run from repo root:
  python3 tools/bench/generate_corpus.py
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
CORPUS = ROOT / "corpus"
TARGET_CHARS = {
    "src_alpha.txt": 120_000,
    "src_beta.txt": 120_000,
    "src_gamma.txt": 120_000,
    "build_log.txt": 150_000,
    "search_hits.txt": 100_000,
}

# Planted facts. Values are stable identifiers; tasks quote them as ground truth.
FACTS = {
    "src_alpha.txt": {
        "head": "FACT_ALPHA_HEAD=alpha-head-7f3a91c2",
        "mid": "FACT_ALPHA_MID=alpha-mid-4b18e0d7",
        "tail": "FACT_ALPHA_TAIL=alpha-tail-c91f2a64",
    },
    "src_beta.txt": {
        "head": "FACT_BETA_HEAD=beta-head-22d8a1f0",
        "mid": "FACT_BETA_MID=beta-mid-9c2e71ab",
        "tail": "FACT_BETA_TAIL=beta-tail-0e55d3c8",
    },
    "src_gamma.txt": {
        "head": "FACT_GAMMA_HEAD=gamma-head-81af0c33",
        "mid": "FACT_GAMMA_MID=gamma-mid-5d77b2e1",
        "tail": "FACT_GAMMA_TAIL=gamma-tail-a3c9046f",
    },
    "build_log.txt": {
        "tail_error": "FACT_LOG_TAIL_ERROR=log-tail-error-e7b4c012",
    },
    "search_hits.txt": {
        "needle": "FACT_GREP_NEEDLE=grep-needle-6a19d8ef",
    },
}


def filler_line(file_name: str, index: int) -> str:
    digest = hashlib.sha256(f"{file_name}:{index}".encode()).hexdigest()[:16]
    return f"noise[{index:06d}] path=/{file_name}/chunk/{index % 97} id={digest}"


def write_source_file(path: Path, facts: dict[str, str], target_chars: int) -> dict:
    lines: list[str] = []
    lines.append(f"# synthetic source: {path.name}")
    lines.append(f"# {facts['head']}")
    lines.append("")
    # Grow body until near target, then plant mid, then finish with tail.
    while sum(len(line) + 1 for line in lines) < target_chars // 2:
        lines.append(filler_line(path.name, len(lines)))
    lines.append("")
    lines.append(f"# midpoint marker for {path.name}")
    lines.append(f"# {facts['mid']}")
    lines.append("")
    while sum(len(line) + 1 for line in lines) < target_chars - 400:
        lines.append(filler_line(path.name, len(lines)))
    lines.append("")
    lines.append(f"# tail marker for {path.name}")
    lines.append(f"# {facts['tail']}")
    lines.append("")
    text = "\n".join(lines) + "\n"
    path.write_text(text, encoding="utf-8")
    return {
        "path": path.name,
        "bytes": len(text.encode("utf-8")),
        "lines": text.count("\n"),
        "facts": facts,
        "sha256": hashlib.sha256(text.encode("utf-8")).hexdigest(),
    }


def write_build_log(path: Path, facts: dict[str, str], target_chars: int) -> dict:
    lines: list[str] = ["[info] synthetic build log start"]
    while sum(len(line) + 1 for line in lines) < target_chars - 300:
        idx = len(lines)
        level = "debug" if idx % 17 else "info"
        lines.append(f"[{level}] step={idx} ok hash={hashlib.md5(str(idx).encode()).hexdigest()[:8]}")
    lines.append("[error] build failed in package gamma")
    lines.append(f"[error] {facts['tail_error']}")
    lines.append("[error] exit_code=1")
    text = "\n".join(lines) + "\n"
    path.write_text(text, encoding="utf-8")
    return {
        "path": path.name,
        "bytes": len(text.encode("utf-8")),
        "lines": text.count("\n"),
        "facts": facts,
        "sha256": hashlib.sha256(text.encode("utf-8")).hexdigest(),
    }


def write_search_hits(path: Path, facts: dict[str, str], target_chars: int) -> dict:
    lines: list[str] = ["# synthetic grep corpus"]
    needle_at = None
    while sum(len(line) + 1 for line in lines) < target_chars:
        idx = len(lines)
        if needle_at is None and sum(len(line) + 1 for line in lines) > target_chars // 2:
            lines.append(f"match: {facts['needle']} file=src_gamma.txt line=mid")
            needle_at = idx
            continue
        lines.append(f"noise-hit[{idx:06d}] pattern=almost but not the needle id={idx}")
    text = "\n".join(lines) + "\n"
    path.write_text(text, encoding="utf-8")
    return {
        "path": path.name,
        "bytes": len(text.encode("utf-8")),
        "lines": text.count("\n"),
        "facts": facts,
        "needle_line": needle_at,
        "sha256": hashlib.sha256(text.encode("utf-8")).hexdigest(),
    }


def main() -> int:
    CORPUS.mkdir(parents=True, exist_ok=True)
    manifest = {
        "schema_version": 1,
        "description": (
            "Deterministic planted-fact corpus for pipeline evaluation. "
            "Regenerate with tools/bench/generate_corpus.py."
        ),
        "approx_tokens_chars_div_4": {},
        "files": [],
        "all_facts": [],
    }
    writers = {
        "src_alpha.txt": write_source_file,
        "src_beta.txt": write_source_file,
        "src_gamma.txt": write_source_file,
        "build_log.txt": write_build_log,
        "search_hits.txt": write_search_hits,
    }
    total_bytes = 0
    for name, writer in writers.items():
        meta = writer(CORPUS / name, FACTS[name], TARGET_CHARS[name])
        manifest["files"].append(meta)
        total_bytes += meta["bytes"]
        for fact in meta["facts"].values():
            manifest["all_facts"].append(fact)
    manifest["total_bytes"] = total_bytes
    manifest["approx_tokens_chars_div_4"] = {
        "total": total_bytes // 4,
        "note": "chars/4 estimate only; live tokens_in come from the model stream",
    }
    (CORPUS / "MANIFEST.json").write_text(
        json.dumps(manifest, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(f"wrote {len(manifest['files'])} files, {total_bytes} bytes, ~{total_bytes // 4} tokens")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
