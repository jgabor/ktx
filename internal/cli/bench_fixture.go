package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

const replayFixtureMaxBytes = 64 << 20

// optionalNullable preserves the distinction between an omitted field, JSON
// null, and an explicit zero. Fixture validation requires Present while Value
// remains nil for explicit null.
type optionalNullable[T any] struct {
	Present bool
	Value   *T
}

func (v *optionalNullable[T]) UnmarshalJSON(data []byte) error {
	v.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		v.Value = nil
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Value = &value
	return nil
}

func (v optionalNullable[T]) MarshalJSON() ([]byte, error) {
	if v.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*v.Value)
}

type replayFixture struct {
	Version   *int                       `json:"version"`
	Benchmark *replayFixtureBenchmark    `json:"benchmark"`
	Stream    *replayFixtureStream       `json:"stream"`
	Expected  *replayFixtureExpectations `json:"expected"`
}

type replayFixtureBenchmark struct {
	MethodologyVersion string   `json:"methodology_version"`
	Agent              string   `json:"agent"`
	Model              string   `json:"model"`
	Task               string   `json:"task"`
	Score              *float64 `json:"score"`
}

type replayFixtureStream struct {
	Format  string  `json:"format"`
	Content *string `json:"content"`
	SHA256  string  `json:"sha256"`
}

type replayFixtureExpectations struct {
	Parser      *replayFixtureParser      `json:"parser"`
	Usage       *replayFixtureUsage       `json:"usage"`
	Compression *replayFixtureCompression `json:"compression"`
}

type replayFixtureParser struct {
	ToolUseCount *int64 `json:"tool_use_count"`
}

type replayFixtureUsage struct {
	TokensIn   optionalNullable[int64]   `json:"tokens_in"`
	TokensOut  optionalNullable[int64]   `json:"tokens_out"`
	Cost       optionalNullable[float64] `json:"cost"`
	CacheRead  optionalNullable[int64]   `json:"cache_read"`
	CacheWrite optionalNullable[int64]   `json:"cache_write"`
}

type replayFixtureCompression struct {
	Pipeline        []string `json:"pipeline"`
	BaselineBytes   *int64   `json:"baseline_bytes"`
	CompressedBytes *int64   `json:"compressed_bytes"`
	BytesSaved      *int64   `json:"bytes_saved"`
	Ratio           *float64 `json:"ratio"`
}

func invalidReplayFixture(message string, args ...any) *Error {
	err := usageError("ktx bench run --replay-file: "+message, args...)
	err.Extra = map[string]any{
		"failure_class": "invalid-replay-fixture",
		"correction":    "use a version 1 benchmark-parented fixture and regenerate its expectations and sha256 from the authoritative capture",
	}
	return err
}

func decodeReplayFixture(r io.Reader) (*replayFixture, error) {
	limited := io.LimitReader(r, replayFixtureMaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, invalidReplayFixture("read fixture: %v", err)
	}
	if len(data) > replayFixtureMaxBytes {
		return nil, invalidReplayFixture("fixture exceeds %d bytes", replayFixtureMaxBytes)
	}

	var fixture replayFixture
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fixture); err != nil {
		return nil, invalidReplayFixture("decode fixture: %v", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, invalidReplayFixture("decode fixture: %v", err)
	}
	if err := validateReplayFixture(&fixture); err != nil {
		return nil, err
	}
	return &fixture, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateReplayFixture(f *replayFixture) error {
	if f.Version == nil {
		return invalidReplayFixture("missing required field version")
	}
	if *f.Version != 1 {
		return invalidReplayFixture("unsupported version %d; supported versions: 1", *f.Version)
	}
	if f.Benchmark == nil {
		return invalidReplayFixture("missing required benchmark parent")
	}
	if f.Benchmark.MethodologyVersion == "" || f.Benchmark.Agent == "" || f.Benchmark.Model == "" || f.Benchmark.Task == "" || f.Benchmark.Score == nil {
		return invalidReplayFixture("benchmark requires methodology_version, agent, model, task, and score")
	}
	if !finite(*f.Benchmark.Score) {
		return invalidReplayFixture("benchmark.score must be finite")
	}
	if f.Stream == nil || f.Stream.Format == "" || f.Stream.Content == nil || f.Stream.SHA256 == "" {
		return invalidReplayFixture("stream requires format, content, and sha256")
	}
	if len(f.Stream.SHA256) != 64 || strings.ToLower(f.Stream.SHA256) != f.Stream.SHA256 {
		return invalidReplayFixture("stream.sha256 must be a lowercase SHA-256 hex digest")
	}
	if got := dbpkg.ContentHash(*f.Stream.Content); got != f.Stream.SHA256 {
		return invalidReplayFixture("stream integrity mismatch: computed sha256 %s, fixture has %s", got, f.Stream.SHA256)
	}
	if f.Expected == nil || f.Expected.Parser == nil || f.Expected.Parser.ToolUseCount == nil || *f.Expected.Parser.ToolUseCount < 0 {
		return invalidReplayFixture("expected.parser.tool_use_count is required and must be non-negative")
	}
	if f.Expected.Usage == nil {
		return invalidReplayFixture("expected.usage is required")
	}
	u := f.Expected.Usage
	if !u.TokensIn.Present || !u.TokensOut.Present || !u.Cost.Present || !u.CacheRead.Present || !u.CacheWrite.Present {
		return invalidReplayFixture("expected.usage requires tokens_in, tokens_out, cost, cache_read, and cache_write; each may be null")
	}
	if negative(u.TokensIn.Value) || negative(u.TokensOut.Value) || negative(u.CacheRead.Value) || negative(u.CacheWrite.Value) {
		return invalidReplayFixture("expected usage and cache counts must be non-negative when present")
	}
	if u.Cost.Value != nil && (!finite(*u.Cost.Value) || *u.Cost.Value < 0) {
		return invalidReplayFixture("expected.usage.cost must be finite and non-negative when present")
	}
	c := f.Expected.Compression
	if c == nil || c.Pipeline == nil || c.BaselineBytes == nil || c.CompressedBytes == nil || c.BytesSaved == nil || c.Ratio == nil {
		return invalidReplayFixture("expected.compression requires pipeline, baseline_bytes, compressed_bytes, bytes_saved, and ratio")
	}
	wantRatio := 1.0
	if *c.BaselineBytes > 0 {
		wantRatio = float64(*c.CompressedBytes) / float64(*c.BaselineBytes)
	}
	if *c.BaselineBytes < 0 || *c.CompressedBytes < 0 || *c.CompressedBytes > *c.BaselineBytes ||
		*c.BytesSaved != *c.BaselineBytes-*c.CompressedBytes || !finite(*c.Ratio) || *c.Ratio != wantRatio {
		return invalidReplayFixture("expected.compression measurements are malformed or inconsistent")
	}
	for _, stage := range c.Pipeline {
		if stage == "" {
			return invalidReplayFixture("expected.compression.pipeline contains an empty stage identity")
		}
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func negative(v *int64) bool { return v != nil && *v < 0 }

// runBenchReplayFile validates the portable contract. Offline replay and
// expectation evaluation are added separately; this path deliberately opens no
// database and runs no compression pipeline.
func runBenchReplayFile(path string, stdout, stderr io.Writer, format string) int {
	f, err := os.Open(path)
	if err != nil {
		printError(stderr, invalidReplayFixture("open %q: %v", path, err), format)
		return ExitUsage
	}
	defer f.Close()
	fixture, err := decodeReplayFixture(f)
	if err != nil {
		printError(stderr, err, format)
		return ExitUsage
	}
	if format == FormatText {
		fmt.Fprintf(stdout, "fixture: valid (version=%d, sha256=%s)\n", *fixture.Version, fixture.Stream.SHA256)
		return ExitOK
	}
	if err := json.NewEncoder(stdout).Encode(map[string]any{
		"fixture_validation": "PASS",
		"version":            *fixture.Version,
		"stream_sha256":      fixture.Stream.SHA256,
	}); err != nil {
		printError(stderr, internalError("ktx bench run --replay-file: write JSON: %v", err), format)
		return ExitInternal
	}
	return ExitOK
}
