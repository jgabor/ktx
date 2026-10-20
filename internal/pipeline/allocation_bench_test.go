package pipeline_test

import (
	"bytes"
	"math"
	"runtime"
	"testing"

	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/pipeline/stages"
)

func TestLineWorkAllocationBudgets(t *testing.T) {
	reg := pipeline.NewRegistry()
	stages.RegisterBuiltins(reg)
	input := bytes.Repeat([]byte{'\n'}, 1<<20)
	for _, token := range []string{"empty", "head:10", "keep-ends:10:10", "dedup-blank", "normalize"} {
		t.Run(token, func(t *testing.T) {
			r := pipeline.Runner{Registry: reg}
			if token != "empty" {
				r.Pipeline = []string{token}
			}
			r.Run(input)
			// TotalAlloc is process-wide, so the race detector or other
			// goroutines can allocate inside one sample. Keep the smallest
			// of several samples: a real per-run allocation shows up in all.
			allocated := uint64(math.MaxUint64)
			for range 5 {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				result := r.Run(input)
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(result)
				allocated = min(allocated, after.TotalAlloc-before.TotalAlloc)
			}
			budget := uint64(8 << 20)
			if token == "empty" {
				budget = 0
			}
			if allocated > budget {
				t.Fatalf("allocated %d bytes, budget %d", allocated, budget)
			}
			t.Logf("allocated %d bytes, budget %d", allocated, budget)
		})
	}
}

func BenchmarkLineWork(b *testing.B) {
	inputs := map[string][]byte{
		"newlines":   bytes.Repeat([]byte("\n"), 1<<20),
		"plain":      bytes.Repeat([]byte("plain output line\n"), 1<<16),
		"structured": bytes.Repeat([]byte("{ \"value\": 123 }\n"), 1<<14),
	}
	reg := pipeline.NewRegistry()
	stages.RegisterBuiltins(reg)
	for name, input := range inputs {
		for _, token := range []string{"empty", "head:10", "keep-ends:10:10", "dedup-blank", "normalize"} {
			b.Run(name+"/"+token, func(b *testing.B) {
				r := pipeline.Runner{Registry: reg}
				if token != "empty" {
					r.Pipeline = []string{token}
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(input)))
				for b.Loop() {
					r.Run(input)
				}
			})
		}
	}
}
