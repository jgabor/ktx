package experiment

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var corpusTargets = map[string]int{
	"src_alpha.txt": 120_000, "src_beta.txt": 120_000, "src_gamma.txt": 120_000,
	"build_log.txt": 150_000, "search_hits.txt": 100_000,
}

var corpusFacts = map[string]map[string]string{
	"src_alpha.txt":   {"head": "FACT_ALPHA_HEAD=alpha-head-7f3a91c2", "mid": "FACT_ALPHA_MID=alpha-mid-4b18e0d7", "tail": "FACT_ALPHA_TAIL=alpha-tail-c91f2a64"},
	"src_beta.txt":    {"head": "FACT_BETA_HEAD=beta-head-22d8a1f0", "mid": "FACT_BETA_MID=beta-mid-9c2e71ab", "tail": "FACT_BETA_TAIL=beta-tail-0e55d3c8"},
	"src_gamma.txt":   {"head": "FACT_GAMMA_HEAD=gamma-head-81af0c33", "mid": "FACT_GAMMA_MID=gamma-mid-5d77b2e1", "tail": "FACT_GAMMA_TAIL=gamma-tail-a3c9046f"},
	"build_log.txt":   {"tail_error": "FACT_LOG_TAIL_ERROR=log-tail-error-e7b4c012"},
	"search_hits.txt": {"needle": "FACT_GREP_NEEDLE=grep-needle-6a19d8ef"},
}

var corpusOrder = []string{"src_alpha.txt", "src_beta.txt", "src_gamma.txt", "build_log.txt", "search_hits.txt"}

func fillerLine(name string, index int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", name, index)))
	return fmt.Sprintf("noise[%06d] path=/%s/chunk/%d id=%s", index, name, index%97, hex.EncodeToString(digest[:])[:16])
}

func textMetadata(name, text string, facts map[string]string) map[string]any {
	digest := sha256.Sum256([]byte(text))
	return map[string]any{"path": name, "bytes": len([]byte(text)), "lines": strings.Count(text, "\n"), "facts": facts, "sha256": hex.EncodeToString(digest[:])}
}

func sourceCorpus(name string, facts map[string]string, target int) (string, map[string]any) {
	lines := []string{"# synthetic source: " + name, "# " + facts["head"], ""}
	length := joinedLength(lines)
	for length < target/2 {
		line := fillerLine(name, len(lines))
		lines = append(lines, line)
		length += len(line) + 1
	}
	lines = append(lines, "", "# midpoint marker for "+name, "# "+facts["mid"], "")
	length = joinedLength(lines)
	for length < target-400 {
		line := fillerLine(name, len(lines))
		lines = append(lines, line)
		length += len(line) + 1
	}
	lines = append(lines, "", "# tail marker for "+name, "# "+facts["tail"], "")
	text := strings.Join(lines, "\n") + "\n"
	return text, textMetadata(name, text, facts)
}

func buildLogCorpus(name string, facts map[string]string, target int) (string, map[string]any) {
	lines := []string{"[info] synthetic build log start"}
	length := joinedLength(lines)
	for length < target-300 {
		index := len(lines)
		level := "debug"
		if index%17 == 0 {
			level = "info"
		}
		digest := md5.Sum([]byte(fmt.Sprintf("%d", index)))
		line := fmt.Sprintf("[%s] step=%d ok hash=%s", level, index, hex.EncodeToString(digest[:])[:8])
		lines = append(lines, line)
		length += len(line) + 1
	}
	lines = append(lines, "[error] build failed in package gamma", "[error] "+facts["tail_error"], "[error] exit_code=1")
	text := strings.Join(lines, "\n") + "\n"
	return text, textMetadata(name, text, facts)
}

func searchCorpus(name string, facts map[string]string, target int) (string, map[string]any) {
	lines := []string{"# synthetic grep corpus"}
	length := joinedLength(lines)
	needle := -1
	for length < target {
		index := len(lines)
		var line string
		if needle < 0 && length > target/2 {
			line = fmt.Sprintf("match: %s file=src_gamma.txt line=mid", facts["needle"])
			needle = index
		} else {
			line = fmt.Sprintf("noise-hit[%06d] pattern=almost but not the needle id=%d", index, index)
		}
		lines = append(lines, line)
		length += len(line) + 1
	}
	text := strings.Join(lines, "\n") + "\n"
	metadata := textMetadata(name, text, facts)
	metadata["needle_line"] = needle
	return text, metadata
}

func joinedLength(lines []string) int {
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	return total
}

func GenerateCorpus(directory string) (map[string]any, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	manifest := map[string]any{
		"schema_version":            1,
		"description":               "Deterministic planted-fact corpus for pipeline evaluation. Regenerate with ktx bench corpus generate.",
		"approx_tokens_chars_div_4": map[string]any{}, "files": []any{}, "all_facts": []string{},
	}
	files := make([]any, 0, len(corpusOrder))
	allFacts := []string{}
	total := 0
	for _, name := range corpusOrder {
		facts := corpusFacts[name]
		var text string
		var metadata map[string]any
		switch name {
		case "build_log.txt":
			text, metadata = buildLogCorpus(name, facts, corpusTargets[name])
		case "search_hits.txt":
			text, metadata = searchCorpus(name, facts, corpusTargets[name])
		default:
			text, metadata = sourceCorpus(name, facts, corpusTargets[name])
		}
		if err := os.WriteFile(filepath.Join(directory, name), []byte(text), 0o644); err != nil {
			return nil, err
		}
		files = append(files, metadata)
		total += len([]byte(text))
		for _, key := range []string{"head", "mid", "tail", "tail_error", "needle"} {
			if fact, ok := facts[key]; ok {
				allFacts = append(allFacts, fact)
			}
		}
	}
	manifest["files"] = files
	manifest["all_facts"] = allFacts
	manifest["total_bytes"] = total
	manifest["approx_tokens_chars_div_4"] = map[string]any{"total": total / 4, "note": "chars/4 estimate only; live tokens_in come from the model stream"}
	if err := WriteJSON(filepath.Join(directory, "MANIFEST.json"), manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}
