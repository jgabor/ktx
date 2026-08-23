package db

import (
	"errors"
	"sync"
	"testing"
)

func TestCompressionDispositionMonotonicProjection(t *testing.T) {
	d := openMemoryDB(t)
	sourceID := seedSessionAndCalls(t, d, []callFixture{{
		id: "dispositionsrcxx", tool: "bash", input: `{"command":"git status"}`, output: "long output",
	}})[0]

	record := func(disposition CompressionDisposition) {
		t.Helper()
		if err := d.RecordCompressionDisposition(&disposition); err != nil {
			t.Fatalf("RecordCompressionDisposition: %v", err)
		}
	}
	record(CompressionDisposition{
		SourceID: sourceID, Profile: "default", Route: "default_fallback",
		Disposition: CompressionDispositionIntentionalPassthrough, ObservedAt: "2026-07-21T12:00:00Z",
	})
	record(CompressionDisposition{
		SourceID: sourceID, Profile: "search", Route: "matched",
		Disposition: CompressionDispositionAntiWorseningRejected, ObservedAt: "2026-07-21T12:00:01Z",
	})

	got, err := d.GetCompressionDisposition(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Disposition != CompressionDispositionIntentionalPassthrough || got.Profile != "default" || got.Route != "default_fallback" || got.ObservedAt != "2026-07-21T12:00:00Z" {
		t.Fatalf("first non-artifact disposition changed: %+v", got)
	}

	seedCompressedWithID(t, d, "artifactfirstxxx", sourceID, "short", "2026-07-21T12:00:02Z")
	record(CompressionDisposition{
		SourceID: sourceID, Profile: "search", Route: "matched",
		Disposition: CompressionDispositionArtifactPersisted, ArtifactID: "artifactfirstxxx", ObservedAt: "2026-07-21T12:00:02Z",
	})
	seedCompressedWithID(t, d, "artifactsecondxx", sourceID, "later", "2026-07-21T12:00:03Z")
	record(CompressionDisposition{
		SourceID: sourceID, Profile: "later", Route: "default_fallback",
		Disposition: CompressionDispositionArtifactPersisted, ArtifactID: "artifactsecondxx", ObservedAt: "2026-07-21T12:00:03Z",
	})
	record(CompressionDisposition{
		SourceID: sourceID, Profile: "later", Route: "default_fallback",
		Disposition: CompressionDispositionIntentionalPassthrough, ObservedAt: "2026-07-21T12:00:04Z",
	})

	got, err = d.GetCompressionDisposition(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Disposition != CompressionDispositionArtifactPersisted || got.ArtifactID != "artifactfirstxxx" || got.Profile != "search" || got.Route != "matched" || got.ObservedAt != "2026-07-21T12:00:02Z" {
		t.Fatalf("artifact authority changed: %+v", got)
	}
}

func TestCompressionDispositionConcurrentPromotion(t *testing.T) {
	d, err := Open(t.TempDir() + "/ktx.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	sourceID := seedSessionAndCalls(t, d, []callFixture{{id: "concurrentdispos", output: "long output"}})[0]
	seedCompressedWithID(t, d, "concurrentartifx", sourceID, "x", "2026-07-21T12:00:02Z")
	if err := d.RecordCompressionDisposition(&CompressionDisposition{
		SourceID: sourceID, Profile: "default", Route: "default_fallback",
		Disposition: CompressionDispositionIntentionalPassthrough, ObservedAt: "2026-07-21T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	const workers = 12
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(artifact bool) {
			defer wg.Done()
			<-start
			disposition := &CompressionDisposition{
				SourceID: sourceID, Profile: "default", Route: "default_fallback",
				Disposition: CompressionDispositionAntiWorseningRejected, ObservedAt: "2026-07-21T12:00:01Z",
			}
			if artifact {
				disposition.Disposition = CompressionDispositionArtifactPersisted
				disposition.ArtifactID = "concurrentartifx"
				disposition.ObservedAt = "2026-07-21T12:00:02Z"
			}
			errs <- d.RecordCompressionDisposition(disposition)
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent RecordCompressionDisposition: %v", err)
		}
	}

	got, err := d.GetCompressionDisposition(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Disposition != CompressionDispositionArtifactPersisted || got.ArtifactID != "concurrentartifx" {
		t.Fatalf("concurrent projection = %+v, want authoritative artifact", got)
	}
	if _, err := d.GetCompressionDisposition("missingmissingxx"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing disposition error = %v, want ErrNotFound", err)
	}
}

func TestCompressionDispositionRequiresFirstArtifact(t *testing.T) {
	d := openMemoryDB(t)
	sourceID := seedSessionAndCalls(t, d, []callFixture{{id: "firstartifactsrc", output: "long output"}})[0]
	seedCompressedWithID(t, d, "firstartifactxxx", sourceID, "first", "2026-07-21T12:00:00.1Z")
	seedCompressedWithID(t, d, "laterartifactxxx", sourceID, "later", "2026-07-21T12:00:00.12Z")

	got, err := d.GetCompressedBySource(sourceID)
	if err != nil || got.ID != "firstartifactxxx" {
		t.Fatalf("GetCompressedBySource = %+v, %v; want firstartifactxxx", got, err)
	}
	later := &CompressionDisposition{
		SourceID: sourceID, Profile: "later", Route: "matched",
		Disposition: CompressionDispositionArtifactPersisted, ArtifactID: "laterartifactxxx", ObservedAt: "2026-07-21T12:00:02Z",
	}
	if err := d.RecordCompressionDisposition(later); err == nil {
		t.Fatal("later artifact unexpectedly became authoritative")
	}
	first := *later
	first.Profile = "first"
	first.ArtifactID = "firstartifactxxx"
	first.ObservedAt = "2026-07-21T12:00:01Z"
	if err := d.RecordCompressionDisposition(&first); err != nil {
		t.Fatal(err)
	}
	retained, err := d.GetCompressionDisposition(sourceID)
	if err != nil || retained.ArtifactID != first.ArtifactID || retained.Profile != "first" {
		t.Fatalf("retained disposition = %+v, %v", retained, err)
	}
}

func TestFirstArtifactEqualTimestampUsesID(t *testing.T) {
	d := openMemoryDB(t)
	sourceID := seedSessionAndCalls(t, d, []callFixture{{id: "equaltimestampsrc", output: "long output"}})[0]
	seedCompressedWithID(t, d, "zzzzzzzzzzzzzzzz", sourceID, "later id", "2026-07-21T12:00:00.1Z")
	seedCompressedWithID(t, d, "aaaaaaaaaaaaaaaa", sourceID, "first id", "2026-07-21T12:00:00.100Z")

	got, err := d.GetCompressedBySource(sourceID)
	if err != nil || got.ID != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("GetCompressedBySource = %+v, %v; want ID tie-break winner", got, err)
	}
}
