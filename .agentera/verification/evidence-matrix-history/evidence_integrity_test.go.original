package pipeline_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTargetedResidualEvidenceHashes(t *testing.T) {
	read := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	hash := func(path string) string { return digest(read(path)) }
	namedHash := func(document, name, pattern string) string {
		t.Helper()
		matches := regexp.MustCompile(pattern).FindAllStringSubmatch(document, -1)
		if len(matches) != 1 {
			t.Fatalf("%s references = %d, want exactly 1", name, len(matches))
		}
		return matches[0][1]
	}
	wantNamedHash := func(document, name, pattern, path string) {
		t.Helper()
		if got, want := namedHash(document, name, pattern), hash(path); got != want {
			t.Errorf("%s hash = %s, want %s", name, got, want)
		}
	}

	matrixPath := "../../tools/bench/evidence/targeted-residual-evidence-matrix.json"
	matrixReportPath := "../../.agentera/verification/targeted-residual-evidence-matrix-20260819.md"
	evaluatorPath := "evidence_matrix_test.go"
	candidateReceiptPath := "../../tools/bench/evidence/targeted-residual-candidate-evaluation.json"
	candidateReportPath := "../../.agentera/verification/targeted-residual-candidate-evaluation-20260819.md"
	controlledReceiptPath := "../../tools/bench/evidence/targeted-residual-controlled-comparison.json"
	controlledEvaluatorPath := "controlled_comparison_test.go"
	controlledReportPath := "../../.agentera/verification/targeted-residual-controlled-comparison-20260819.md"
	sourcePaths := []string{
		"../../.agentera/verification/historical-aggregates-20260812-19.md",
		"../../.agentera/verification/multidimensional-residual-ranking-20260819.md",
		"../../.agentera/verification/targeted-residual-diagnosis-20260819.md",
	}

	var matrix struct {
		Provenance struct {
			SourceChain []struct {
				Source string `json:"source"`
				SHA256 string `json:"sha256"`
			} `json:"source_chain"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(read(matrixPath), &matrix); err != nil {
		t.Fatal(err)
	}
	if len(matrix.Provenance.SourceChain) != len(sourcePaths) {
		t.Fatalf("matrix source references = %d, want %d", len(matrix.Provenance.SourceChain), len(sourcePaths))
	}
	for i, path := range sourcePaths {
		wantSource := strings.TrimPrefix(path, "../../")
		if got := matrix.Provenance.SourceChain[i]; got.Source != wantSource || got.SHA256 != hash(path) {
			t.Errorf("matrix source[%d] = %s %s, want %s %s", i, got.Source, got.SHA256, wantSource, hash(path))
		}
	}

	matrixReport := string(read(matrixReportPath))
	wantNamedHash(matrixReport, "matrix report matrix JSON", `(?m)^The machine-readable matrix is .* SHA-256 `+"`"+`([0-9a-f]{64})`+"`"+`\.$`, matrixPath)
	wantNamedHash(matrixReport, "matrix report evaluator", `(?m)^Every case defines.*SHA-256 `+"`"+`([0-9a-f]{64})`+"`"+`\.$`, evaluatorPath)
	for _, path := range sourcePaths {
		base := regexp.QuoteMeta(strings.TrimSuffix(strings.TrimPrefix(path, "../../.agentera/verification/"), ".md"))
		wantNamedHash(matrixReport, "matrix report source "+path, `(?m)^- `+"`"+base+`\.md`+"`"+` — SHA-256 `+"`"+`([0-9a-f]{64})`+"`", path)
	}

	var candidate struct {
		MatrixSHA256       string `json:"matrix_sha256"`
		MatrixReportSHA256 string `json:"matrix_report_sha256"`
		EvaluatorSHA256    string `json:"evaluator_sha256"`
	}
	if err := json.Unmarshal(read(candidateReceiptPath), &candidate); err != nil {
		t.Fatal(err)
	}
	for name, hashes := range map[string][2]string{
		"matrix":        {candidate.MatrixSHA256, hash(matrixPath)},
		"matrix report": {candidate.MatrixReportSHA256, hash(matrixReportPath)},
		"evaluator":     {candidate.EvaluatorSHA256, hash(evaluatorPath)},
	} {
		if hashes[0] != hashes[1] {
			t.Errorf("%s hash = %s, want %s", name, hashes[0], hashes[1])
		}
	}

	candidateReport := string(read(candidateReportPath))
	wantNamedHash(candidateReport, "candidate receipt", `(?m)^The machine-readable receipt is .* SHA-256 `+"`"+`([0-9a-f]{64})`+"`"+`\.$`, candidateReceiptPath)
	wantNamedHash(candidateReport, "candidate matrix report", `(?m)^- Corrected matrix report: `+"`"+`([0-9a-f]{64})`+"`"+`\.$`, matrixReportPath)
	wantNamedHash(candidateReport, "candidate matrix JSON", `(?m)^- Matrix JSON: `+"`"+`([0-9a-f]{64})`+"`"+`\.$`, matrixPath)
	wantNamedHash(candidateReport, "candidate evaluator", `(?m)^- Evaluator test after candidate coverage: `+"`"+`([0-9a-f]{64})`+"`"+`\.$`, evaluatorPath)

	controlledReport := string(read(controlledReportPath))
	wantNamedHash(controlledReport, "controlled receipt", `(?m)^Machine receipt: .* \(SHA-256 `+"`"+`([0-9a-f]{64})`+"`"+`\)\. Evaluator:`, controlledReceiptPath)
	wantNamedHash(controlledReport, "controlled evaluator", `(?m)^Machine receipt: .* Evaluator: .* \(SHA-256 `+"`"+`([0-9a-f]{64})`+"`"+`\)\.$`, controlledEvaluatorPath)
	for name, path := range map[string]string{
		"matrix report": matrixReportPath, "matrix JSON": matrixPath,
		"corrected candidate report": candidateReportPath, "corrected candidate receipt": candidateReceiptPath,
		"corrected candidate evaluator": evaluatorPath,
	} {
		wantNamedHash(controlledReport, "controlled "+name, regexp.QuoteMeta(name)+" `([0-9a-f]{64})`", path)
	}
}
