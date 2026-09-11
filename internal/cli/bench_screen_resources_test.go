package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBenchScreenOversizeAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "population.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(4 * screenMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var stdout, stderr bytes.Buffer
	code := runBenchScreen([]string{"--population", path, "--config", "unused", "--anchor-config", "unused"}, Options{Format: FormatJSON}, &stdout, &stderr)
	runtime.ReadMemStats(&after)
	if code != ExitUsage || !strings.Contains(stderr.String(), "population exceeds 67108864 bytes") {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("oversized regular file allocated %d bytes before rejection", allocated)
	}
}

type screenCountingReader struct{ read int64 }

func (r *screenCountingReader) Read(p []byte) (int, error) {
	clear(p)
	r.read += int64(len(p))
	return len(p), nil
}

func TestScreenPopulationReadBoundary(t *testing.T) {
	for _, size := range []int64{screenMaxBytes - 1, screenMaxBytes, screenMaxBytes + 1} {
		r := &screenCountingReader{}
		b, err := readScreenPopulationBytes(io.LimitReader(r, size))
		if size <= screenMaxBytes {
			if err != nil || int64(len(b)) != size {
				t.Fatalf("size=%d len=%d err=%v", size, len(b), err)
			}
		} else if err == nil || b != nil {
			t.Fatal("oversized reader accepted")
		}
	}
	r := &screenCountingReader{}
	b, err := readScreenPopulationBytes(r)
	if err == nil || b != nil || r.read != screenMaxBytes+1 {
		t.Fatalf("unbounded reader: read=%d len=%d err=%v", r.read, len(b), err)
	}
}
