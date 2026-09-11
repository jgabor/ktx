package experiment

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This opt-in probe uses generated evidence and an explicitly owned database.
// Setup is excluded from allocation measurement. Trace the process to count
// actual stream opens, reads and seeks rather than inferring them from time.
func TestMaterializationResourceProbe(t *testing.T) {
	if os.Getenv("KTX_MATERIALIZATION_PROBE") == "" {
		t.Skip("opt-in resource probe")
	}
	for _, mib := range []int{1, 8, 16} {
		t.Run(fmt.Sprintf("stream-%dMiB", mib), func(t *testing.T) {
			dir, manifest, attempt := writeAuthorityFixture(t)
			writeAttemptRecords(t, dir, attempt)
			baseline, err := WriteSummary(dir)
			if err != nil {
				t.Fatal(err)
			}
			baselineBytes, err := MarshalJSON(baseline)
			if err != nil {
				t.Fatal(err)
			}
			line := []byte(`{"type":"ignored","part":{"padding":"` + strings.Repeat("x", 4000) + `"}}` + "\n")
			payload := bytes.Repeat(line, mib*1024*1024/len(line))
			path := filepath.Join(dir, attempt.Artifacts["stream"])
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			payload = append(payload, original...)
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			attempt.ContentHashes = map[string]string{"stream": sha256Bytes(payload)}
			if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
				t.Fatal(err)
			}
			writeAttemptRecords(t, dir, attempt)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			result, err := RegenerateSummary(dir)
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			data, err := MarshalJSON(result.Summary)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, baselineBytes) {
				t.Fatal("ignored stream padding changed the current-source summary bytes")
			}
			if mib == 16 && after.TotalAlloc-before.TotalAlloc > 24*1024*1024 {
				t.Fatal("large-stream allocation regression")
			}
			t.Logf("stream=%dMiB allocated_bytes=%d allocations=%d summary_sha256=%s stream_path=%s", mib, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, sha256Bytes(data), path)
		})
	}
	for _, count := range []int{100, 1000, 10000} {
		t.Run(fmt.Sprintf("ledger-%d", count), func(t *testing.T) {
			dir, manifest, attempt := writeAuthorityFixture(t)
			// An incomplete current envelope needs no retained stream parse.
			attempt.Artifacts, attempt.ContentHashes = nil, nil
			attempt.Metrics = Metrics{}
			if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
				t.Fatal(err)
			}
			writeAttemptRecords(t, dir, attempt)
			baseline, err := WriteSummary(dir)
			if err != nil {
				t.Fatal(err)
			}
			baselineBytes, err := MarshalJSON(baseline)
			if err != nil {
				t.Fatal(err)
			}
			writeAttemptRecords(t, dir, makeRepeatedAttempts(attempt, count)...)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			result, err := RegenerateSummary(dir)
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			data, err := MarshalJSON(result.Summary)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, baselineBytes) {
				t.Fatal("duplicate ledger changed the current-source summary bytes")
			}
			if count == 10000 && after.TotalAlloc-before.TotalAlloc > 128*1024*1024 {
				t.Fatal("duplicate-ledger allocation regression")
			}
			t.Logf("records=%d allocated_bytes=%d allocations=%d summary_sha256=%s", count, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, sha256Bytes(data))
		})
	}
}

func makeRepeatedAttempts(attempt Attempt, count int) []Attempt {
	records := make([]Attempt, count)
	for i := range records {
		records[i] = attempt
	}
	return records
}
