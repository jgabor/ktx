package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func validReplayFixtureJSON(t *testing.T, usage map[string]any) []byte {
	t.Helper()
	stream := "{\"type\":\"step_start\",\"part\":{}}\n"
	fixture := map[string]any{
		"version": 1,
		"benchmark": map[string]any{
			"methodology_version": "v1", "agent": "opencode", "model": "model", "task": "task", "score": 1,
		},
		"stream": map[string]any{
			"format": "opencode-jsonl-1", "content": stream, "sha256": dbpkg.ContentHash(stream),
		},
		"expected": map[string]any{
			"parser": map[string]any{"tool_use_count": 0},
			"usage":  usage,
			"compression": map[string]any{
				"pipeline": []string{"strip-ansi"}, "baseline_bytes": 0, "compressed_bytes": 0, "bytes_saved": 0, "ratio": 1,
			},
		},
	}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func completeFixtureUsage() map[string]any {
	return map[string]any{
		"tokens_in": nil, "tokens_out": int64(0), "cost": nil, "cache_read": int64(0), "cache_write": nil,
	}
}

func TestDecodeReplayFixtureV1PreservesNullAndZero(t *testing.T) {
	fixture, err := decodeReplayFixture(bytes.NewReader(validReplayFixtureJSON(t, completeFixtureUsage())))
	if err != nil {
		t.Fatalf("decodeReplayFixture: %v", err)
	}
	u := fixture.Expected.Usage
	if !u.TokensIn.Present || u.TokensIn.Value != nil {
		t.Fatalf("tokens_in = %#v, want present null", u.TokensIn)
	}
	if !u.TokensOut.Present || u.TokensOut.Value == nil || *u.TokensOut.Value != 0 {
		t.Fatalf("tokens_out = %#v, want explicit zero", u.TokensOut)
	}
	if !u.CacheRead.Present || u.CacheRead.Value == nil || *u.CacheRead.Value != 0 {
		t.Fatalf("cache_read = %#v, want explicit zero", u.CacheRead)
	}
	if !u.CacheWrite.Present || u.CacheWrite.Value != nil {
		t.Fatalf("cache_write = %#v, want present null", u.CacheWrite)
	}
	roundTrip, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if !bytes.Contains(roundTrip, []byte(`"tokens_in":null`)) || !bytes.Contains(roundTrip, []byte(`"tokens_out":0`)) {
		t.Fatalf("nullable round trip lost null/zero distinction: %s", roundTrip)
	}
}

func TestDecodeReplayFixtureRejectsVersionAndRequiredFields(t *testing.T) {
	base := validReplayFixtureJSON(t, completeFixtureUsage())
	tests := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"missing version", func(f map[string]any) { delete(f, "version") }, "missing required field version"},
		{"unsupported version", func(f map[string]any) { f["version"] = 2 }, "unsupported version 2"},
		{"missing benchmark", func(f map[string]any) { delete(f, "benchmark") }, "missing required benchmark parent"},
		{"missing nullable evidence", func(f map[string]any) {
			u := f["expected"].(map[string]any)["usage"].(map[string]any)
			delete(u, "cache_write")
		}, "expected.usage requires"},
		{"missing compression identity", func(f map[string]any) {
			c := f["expected"].(map[string]any)["compression"].(map[string]any)
			delete(c, "pipeline")
		}, "expected.compression requires"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var candidate map[string]any
			if err := json.Unmarshal(base, &candidate); err != nil {
				t.Fatal(err)
			}
			tt.edit(candidate)
			b, _ := json.Marshal(candidate)
			_, err := decodeReplayFixture(bytes.NewReader(b))
			var ktxErr *Error
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			ktxErr = toKtxError(err)
			if ktxErr.Code != CodeUsage || ktxErr.Extra["failure_class"] != "invalid-replay-fixture" || ktxErr.Extra["correction"] == "" {
				t.Fatalf("structured error = %#v", ktxErr)
			}
		})
	}
}

func TestDecodeReplayFixtureDetectsIntegrityMismatch(t *testing.T) {
	b := validReplayFixtureJSON(t, completeFixtureUsage())
	b = bytes.Replace(b, []byte("step_start"), []byte("step_stop"), 1)
	_, err := decodeReplayFixture(bytes.NewReader(b))
	if err == nil || !strings.Contains(err.Error(), "stream integrity mismatch") {
		t.Fatalf("error = %v, want integrity mismatch", err)
	}
}

func TestDecodeReplayFixtureAcceptsReviewedContentAndHashChange(t *testing.T) {
	b := validReplayFixtureJSON(t, completeFixtureUsage())
	var fixture map[string]any
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	stream := fixture["stream"].(map[string]any)
	stream["content"] = "{\"type\":\"step_stop\",\"part\":{}}\n"
	stream["sha256"] = dbpkg.ContentHash(stream["content"].(string))
	b, _ = json.Marshal(fixture)
	if _, err := decodeReplayFixture(bytes.NewReader(b)); err != nil {
		t.Fatalf("decodeReplayFixture after synchronized review edit: %v", err)
	}
}

func TestRunBenchRunReplaySelectorsAreUnambiguous(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "session-looking.json")
	if err := os.WriteFile(fixturePath, []byte("not a fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_DB", filepath.Join(dir, "ktx.db"))

	t.Run("session selector never reads matching filename", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchRun([]string{"--replay", fixturePath}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound || strings.Contains(stderr.String(), "invalid-replay-fixture") {
			t.Fatalf("code=%d stderr=%q, want session not-found", code, stderr.String())
		}
	})

	t.Run("file selector validates file", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchRun([]string{"--replay-file", fixturePath}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage || !strings.Contains(stderr.String(), `"failure_class":"invalid-replay-fixture"`) {
			t.Fatalf("code=%d stderr=%q, want fixture validation failure", code, stderr.String())
		}
	})

	t.Run("selectors conflict", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchRun([]string{"--replay", "session", "--replay-file", fixturePath}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage || !strings.Contains(stderr.String(), "mutually exclusive") {
			t.Fatalf("code=%d stderr=%q, want selector conflict", code, stderr.String())
		}
	})
}

func TestRunBenchReplayFileValidV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, validReplayFixtureJSON(t, completeFixtureUsage()), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runBenchReplayFile(path, &stdout, &stderr, FormatJSON); code != ExitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"fixture_validation":"PASS"`) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
