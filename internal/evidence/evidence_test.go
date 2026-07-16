// Evidence-layer coverage for eligible retry persistence includes concurrent
// and sequential convergence, conflict preservation, pipeline changes, fresh
// observations, canonical input equality, metadata authority, and session
// ownership.
package evidence

import (
	"errors"
	"sync"
	"testing"

	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// --- test helpers ---

func testDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	d, err := dbpkg.Open(t.TempDir() + "/ktx.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func mustSession(t *testing.T, d *dbpkg.DB, id string) {
	t.Helper()
	if err := d.InsertSession(&dbpkg.Session{ID: id, Agent: "codex", StartedAt: "2026-07-16T10:00:00Z", LastSeenAt: "2026-07-16T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
}

func eligibleSource(t *testing.T, runtime, sessionID, callID string) correlation.Source {
	t.Helper()
	src, err := correlation.Resolve(
		correlation.NativeTuple{Runtime: runtime, SessionID: sessionID, CallID: callID},
		correlation.Trusted(correlation.ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatal(err)
	}
	if !src.Eligible {
		t.Fatalf("want eligible source, got %+v", src)
	}
	return src
}

func freshSource(t *testing.T) correlation.Source {
	t.Helper()
	// An untrusted provenance always falls back to fresh, even with a
	// complete tuple: eligibility is provenance-driven, not text-driven.
	src, err := correlation.Resolve(
		correlation.NativeTuple{Runtime: "codex", SessionID: "ses", CallID: "call"},
		correlation.Untrusted())
	if err != nil {
		t.Fatal(err)
	}
	if !src.Fresh {
		t.Fatalf("want fresh source, got %+v", src)
	}
	return src
}

func toolCall(src correlation.Source, session, tool, input, output string) *dbpkg.ToolCall {
	rt := "codex"
	return &dbpkg.ToolCall{
		ID:        src.ID,
		SessionID: session,
		Tool:      tool,
		Input:     input,
		Output:    output,
		Timestamp: "2026-07-16T10:00:01Z",
		Runtime:   &rt,
	}
}

func compressedCandidate(src correlation.Source, output string) *dbpkg.Compressed {
	return &dbpkg.Compressed{
		SourceID:    src.ID,
		Output:      output,
		ContentHash: dbpkg.ContentHash(output),
		Pipeline:    `["head:3"]`,
		Timestamp:   "2026-07-16T10:00:02Z",
	}
}

func assertSourceCount(t *testing.T, d *dbpkg.DB, id string, want int) {
	t.Helper()
	var n int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Errorf("source rows for %q = %d, want %d", id, n, want)
	}
}

func assertCompressedCount(t *testing.T, d *dbpkg.DB, sourceID string, want int) {
	t.Helper()
	var n int
	if err := d.DB().QueryRow(`SELECT count(*) FROM compressed WHERE source_id = ?`, sourceID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Errorf("compressed rows for %q = %d, want %d", sourceID, n, want)
	}
}

func assertSessionCount(t *testing.T, d *dbpkg.DB, sessionID string, want int64) {
	t.Helper()
	s, err := d.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if s.ToolCallCount != want {
		t.Errorf("session %q tool_call_count = %d, want %d", sessionID, s.ToolCallCount, want)
	}
}

// --- concurrent exact eligible retries converge ---

func TestPersist_EligibleConcurrentExactRetries(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "concurrent-ses", "concurrent-call")

	const deliveries = 16
	type outcome struct {
		result Result
		err    error
	}
	start := make(chan struct{})
	out := make(chan outcome, deliveries)
	var wg sync.WaitGroup
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Each goroutine gets its own copies — realistic: each delivery
			// arrives independently with fresh structs. deriveCost mutates
			// the ToolCall, so sharing it across goroutines would race.
			tc := toolCall(src, "s1", "Bash", `{"cmd":"go test"}`, "ok\n")
			cand := compressedCandidate(src, "compressed-ok")
			r, err := Persist(d, src, tc, cand)
			out <- outcome{r, err}
		}()
	}
	close(start)
	wg.Wait()
	close(out)

	for o := range out {
		if o.err != nil {
			t.Errorf("delivery: %v", o.err)
		}
	}

	assertSourceCount(t, d, src.ID, 1)
	assertCompressedCount(t, d, src.ID, 1)
	assertSessionCount(t, d, "s1", 1)

	// Every concurrent caller saw the same authoritative compressed output.
	stored, err := d.GetCompressedBySource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Output != "compressed-ok" {
		t.Errorf("stored artifact = %q, want %q", stored.Output, "compressed-ok")
	}
}

func TestPersist_EligibleSequentialExactRetries(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "seq-ses", "seq-call")
	tc := toolCall(src, "s1", "Bash", `{"cmd":"echo hi"}`, "hi\n")
	cand := compressedCandidate(src, "compressed-hi")

	r1, err := Persist(d, src, tc, cand)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !r1.Transformed || r1.Output != "compressed-hi" {
		t.Fatalf("first result = %+v, want compressed-hi", r1)
	}

	r2, err := Persist(d, src, tc, cand)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !r2.Transformed || r2.Output != "compressed-hi" {
		t.Fatalf("second result = %+v, want compressed-hi", r2)
	}
	if r2.CompressedID != r1.CompressedID {
		t.Errorf("second compressed id %q != first %q", r2.CompressedID, r1.CompressedID)
	}

	assertSourceCount(t, d, src.ID, 1)
	assertCompressedCount(t, d, src.ID, 1)
	assertSessionCount(t, d, "s1", 1)
}

// --- changed authoritative evidence conflicts, first preserved ---

func TestPersist_EligibleChangedInputConflicts(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "ci-ses", "ci-call")
	first := toolCall(src, "s1", "Bash", `{"cmd":"first"}`, "out")
	if _, err := PersistSource(d, src, first); err != nil {
		t.Fatalf("first: %v", err)
	}

	changed := *first
	changed.Input = `{"cmd":"changed"}` // canonically different
	_, err := PersistSource(d, src, &changed)
	if !errors.Is(err, dbpkg.ErrToolCallConflict) {
		t.Fatalf("changed input error = %v, want ErrToolCallConflict", err)
	}
	got, _ := d.GetToolCall(src.ID)
	if got.Input != `{"cmd":"first"}` {
		t.Errorf("persisted input = %q, want first", got.Input)
	}
}

func TestPersist_EligibleChangedOutputConflicts(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "co-ses", "co-call")
	first := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "first-out")
	if _, err := PersistSource(d, src, first); err != nil {
		t.Fatalf("first: %v", err)
	}

	changed := *first
	changed.Output = "changed-out"
	_, err := PersistSource(d, src, &changed)
	if !errors.Is(err, dbpkg.ErrToolCallConflict) {
		t.Fatalf("changed output error = %v, want ErrToolCallConflict", err)
	}
	got, _ := d.GetToolCall(src.ID)
	if got.Output != "first-out" {
		t.Errorf("persisted output = %q, want first", got.Output)
	}
}

func TestPersist_EligibleChangedToolConflicts(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "ct-ses", "ct-call")
	first := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "out")
	if _, err := PersistSource(d, src, first); err != nil {
		t.Fatalf("first: %v", err)
	}

	changed := *first
	changed.Tool = "Read"
	_, err := PersistSource(d, src, &changed)
	if !errors.Is(err, dbpkg.ErrToolCallConflict) {
		t.Fatalf("changed tool error = %v, want ErrToolCallConflict", err)
	}
	got, _ := d.GetToolCall(src.ID)
	if got.Tool != "Bash" {
		t.Errorf("persisted tool = %q, want Bash", got.Tool)
	}
}

func TestPersist_EligibleChangedRuntimeConflicts(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "cr-ses", "cr-call")
	first := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "out")
	if _, err := PersistSource(d, src, first); err != nil {
		t.Fatalf("first: %v", err)
	}

	changed := *first
	other := "opencode"
	changed.Runtime = &other
	_, err := PersistSource(d, src, &changed)
	if !errors.Is(err, dbpkg.ErrToolCallConflict) {
		t.Fatalf("changed runtime error = %v, want ErrToolCallConflict", err)
	}
	got, _ := d.GetToolCall(src.ID)
	if *got.Runtime != "codex" {
		t.Errorf("persisted runtime = %q, want codex", *got.Runtime)
	}
}

// --- pipeline-change retry follows artifact authority ---

func TestPersist_EligiblePipelineChangeRetryReturnsFirstArtifact(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "pc-ses", "pc-call")
	tc := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "long output")

	// First delivery: pipeline produces artifact A.
	candA := compressedCandidate(src, "artifact-A")
	inserted, err := PersistSource(d, src, tc)
	if err != nil {
		t.Fatalf("first source: %v", err)
	}
	r1, err := PersistArtifact(d, src, tc, inserted, candA)
	if err != nil {
		t.Fatalf("first artifact: %v", err)
	}
	if r1.Output != "artifact-A" {
		t.Fatalf("first output = %q, want artifact-A", r1.Output)
	}

	// Exact retry: pipeline config changed and would produce artifact B.
	candB := compressedCandidate(src, "artifact-B")
	inserted2, err := PersistSource(d, src, tc)
	if err != nil {
		t.Fatalf("second source: %v", err)
	}
	if inserted2 {
		t.Fatal("second delivery should not insert (exact retry)")
	}
	r2, err := PersistArtifact(d, src, tc, inserted2, candB)
	if err != nil {
		t.Fatalf("second artifact: %v", err)
	}
	// Authority policy: return the first persisted artifact, not B.
	if r2.Output != "artifact-A" {
		t.Errorf("retry output = %q, want artifact-A (first persisted)", r2.Output)
	}
	if r2.CompressedID != r1.CompressedID {
		t.Errorf("retry compressed id = %q, want %q (first)", r2.CompressedID, r1.CompressedID)
	}
	assertCompressedCount(t, d, src.ID, 1)
	if r2.Inserted {
		t.Error("retry should not report artifact insertion")
	}
}

func TestPersist_EligibleNoTransformRetryReturnsPriorArtifact(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "nt-ses", "nt-call")
	tc := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "long output")

	// First delivery: pipeline transforms.
	cand := compressedCandidate(src, "first-artifact")
	inserted, err := PersistSource(d, src, tc)
	if err != nil {
		t.Fatalf("first source: %v", err)
	}
	r1, err := PersistArtifact(d, src, tc, inserted, cand)
	if err != nil {
		t.Fatalf("first artifact: %v", err)
	}
	if !r1.Transformed {
		t.Fatal("first delivery should be transformed")
	}

	// Exact retry: pipeline config changed so no transform (nil candidate).
	// The retry must still return the prior artifact.
	inserted2, err := PersistSource(d, src, tc)
	if err != nil {
		t.Fatalf("second source: %v", err)
	}
	r2, err := PersistArtifact(d, src, tc, inserted2, nil)
	if err != nil {
		t.Fatalf("second artifact: %v", err)
	}
	if !r2.Transformed {
		t.Error("retry should be transformed (prior artifact exists)")
	}
	if r2.Output != "first-artifact" {
		t.Errorf("retry output = %q, want first-artifact", r2.Output)
	}
	assertCompressedCount(t, d, src.ID, 1)
}

func TestPersist_ConcurrentTransformAndPassthroughObserveArtifactBoundary(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "boundary-ses", "boundary-call")

	type outcome struct {
		result Result
		err    error
	}
	sourceReady := make(chan struct{})
	allowArtifact := make(chan struct{})
	transformDone := make(chan outcome, 1)
	passthroughDone := make(chan outcome, 1)

	go func() {
		tc := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "source-output")
		inserted, err := PersistSource(d, src, tc)
		close(sourceReady)
		if err != nil {
			transformDone <- outcome{err: err}
			return
		}
		<-allowArtifact
		result, err := PersistArtifact(d, src, tc, inserted, compressedCandidate(src, "first-artifact"))
		transformDone <- outcome{result: result, err: err}
	}()

	go func() {
		<-sourceReady
		tc := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "source-output")
		inserted, err := PersistSource(d, src, tc)
		if err != nil {
			passthroughDone <- outcome{err: err}
			return
		}
		result, err := PersistArtifact(d, src, tc, inserted, nil)
		passthroughDone <- outcome{result: result, err: err}
	}()

	beforeArtifact := <-passthroughDone
	if beforeArtifact.err != nil {
		t.Fatalf("passthrough delivery: %v", beforeArtifact.err)
	}
	if beforeArtifact.result.Transformed || beforeArtifact.result.Output != "source-output" {
		t.Fatalf("passthrough before artifact = %+v, want source output", beforeArtifact.result)
	}
	assertCompressedCount(t, d, src.ID, 0)

	close(allowArtifact)
	transformed := <-transformDone
	if transformed.err != nil {
		t.Fatalf("transform delivery: %v", transformed.err)
	}
	if !transformed.result.Transformed || transformed.result.Output != "first-artifact" {
		t.Fatalf("transform result = %+v, want first artifact", transformed.result)
	}

	postTC := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "source-output")
	inserted, err := PersistSource(d, src, postTC)
	if err != nil {
		t.Fatalf("post-artifact source: %v", err)
	}
	post, err := PersistArtifact(d, src, postTC, inserted, nil)
	if err != nil {
		t.Fatalf("post-artifact retry: %v", err)
	}
	if !post.Transformed || post.Output != "first-artifact" {
		t.Fatalf("post-artifact retry = %+v, want authoritative artifact", post)
	}

	assertSourceCount(t, d, src.ID, 1)
	assertSessionCount(t, d, "s1", 1)
	assertCompressedCount(t, d, src.ID, 1)
}

// --- fresh NanoID deliveries remain distinct ---

func TestPersist_FreshDuplicatesAreDistinct(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")

	src1 := freshSource(t)
	src2 := freshSource(t)
	if src1.ID == src2.ID {
		t.Fatal("two fresh sources produced the same id; expected distinct NanoIDs")
	}

	tc1 := toolCall(src1, "s1", "Bash", `{"cmd":"x"}`, "out")
	tc2 := toolCall(src2, "s1", "Bash", `{"cmd":"x"}`, "out")

	r1, err := Persist(d, src1, tc1, nil)
	if err != nil {
		t.Fatalf("first fresh: %v", err)
	}
	r2, err := Persist(d, src2, tc2, nil)
	if err != nil {
		t.Fatalf("second fresh: %v", err)
	}
	if r1.SourceID == r2.SourceID {
		t.Fatal("fresh deliveries produced the same source id")
	}

	assertSourceCount(t, d, src1.ID, 1)
	assertSourceCount(t, d, src2.ID, 1)
	assertSessionCount(t, d, "s1", 2)
}

func TestPersist_FreshConcurrentDistinct(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")

	const deliveries = 12
	start := make(chan struct{})
	errs := make(chan error, deliveries)
	ids := make(chan string, deliveries)
	var wg sync.WaitGroup
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			src := freshSource(t)
			tc := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "out")
			r, err := Persist(d, src, tc, &dbpkg.Compressed{
				SourceID:    src.ID,
				Output:      "fresh-compressed",
				ContentHash: dbpkg.ContentHash("fresh-compressed"),
				Pipeline:    `["head:3"]`,
				Timestamp:   "2026-07-16T10:00:02Z",
			})
			ids <- r.SourceID
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("fresh delivery: %v", err)
		}
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("fresh delivery produced duplicate id %q", id)
		}
		seen[id] = true
	}
	if len(seen) != deliveries {
		t.Errorf("distinct fresh ids = %d, want %d", len(seen), deliveries)
	}
	assertSessionCount(t, d, "s1", int64(deliveries))

	var sourceRows int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls`).Scan(&sourceRows); err != nil {
		t.Fatal(err)
	}
	if sourceRows != deliveries {
		t.Errorf("total source rows = %d, want %d", sourceRows, deliveries)
	}
}

// --- canonical-equivalent input converges ---

func TestPersist_EligibleCanonicalEquivalentInput(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "ce-ses", "ce-call")
	first := toolCall(src, "s1", "Bash", `{"a":1,"b":2}`, "out")
	if _, err := PersistSource(d, src, first); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Same JSON, different member order and whitespace — canonically equal.
	reordered := *first
	reordered.Input = `{ "b" : 2 , "a" : 1 }`
	inserted, err := PersistSource(d, src, &reordered)
	if err != nil {
		t.Fatalf("canonical-equivalent retry: %v", err)
	}
	if inserted {
		t.Error("canonical-equivalent retry should not insert (exact match)")
	}
	// First evidence preserved.
	got, _ := d.GetToolCall(src.ID)
	if got.Input != `{"a":1,"b":2}` {
		t.Errorf("persisted input = %q, want first (canonical form not first)", got.Input)
	}
	assertSourceCount(t, d, src.ID, 1)
	assertSessionCount(t, d, "s1", 1)
}

// --- metadata first-wins, no enrichment ---

func TestPersist_EligibleMetadataFirstWins(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "mw-ses", "mw-call")

	cost1 := 1.5
	first := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "out")
	first.Cost = &cost1
	first.TokensIn = ptrInt64(100)
	first.ExitCode = ptrInt64(0)

	if _, err := PersistSource(d, src, first); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Same author evidence, different metadata.
	cost2 := 9.99
	retry := *first
	retry.Cost = &cost2
	retry.TokensIn = ptrInt64(999)
	retry.ExitCode = ptrInt64(1)
	retry.Timestamp = "2026-07-16T11:00:00Z"
	inserted, err := PersistSource(d, src, &retry)
	if err != nil {
		t.Fatalf("metadata-different retry: %v", err)
	}
	if inserted {
		t.Error("metadata-only retry should not insert")
	}
	got, _ := d.GetToolCall(src.ID)
	if *got.Cost != cost1 {
		t.Errorf("cost = %v, want first %v", *got.Cost, cost1)
	}
	if *got.TokensIn != 100 {
		t.Errorf("tokens_in = %d, want first 100", *got.TokensIn)
	}
	if *got.ExitCode != 0 {
		t.Errorf("exit_code = %d, want first 0", *got.ExitCode)
	}
	if got.Timestamp != "2026-07-16T10:00:01Z" {
		t.Errorf("timestamp = %q, want first", got.Timestamp)
	}
}

// --- first KTX session owns roll-up ---

func TestPersist_EligibleDifferentKTXSession(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	mustSession(t, d, "s2")
	src := eligibleSource(t, "codex", "ks-ses", "ks-call")
	first := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "out")
	if _, err := PersistSource(d, src, first); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Exact retry through a different KTX session.
	retry := *first
	retry.SessionID = "s2"
	inserted, err := PersistSource(d, src, &retry)
	if err != nil {
		t.Fatalf("different-session retry: %v", err)
	}
	if inserted {
		t.Error("different-session retry should not insert")
	}
	// First session keeps ownership and the sole roll-up increment.
	got, _ := d.GetToolCall(src.ID)
	if got.SessionID != "s1" {
		t.Errorf("session_id = %q, want first s1", got.SessionID)
	}
	assertSessionCount(t, d, "s1", 1)
	assertSessionCount(t, d, "s2", 0)
}

// --- no-transform exact retry passthrough ---

func TestPersist_EligibleNoTransformExactRetry(t *testing.T) {
	d := testDB(t)
	mustSession(t, d, "s1")
	src := eligibleSource(t, "codex", "pt-ses", "pt-call")
	tc := toolCall(src, "s1", "Bash", `{"cmd":"x"}`, "some output")

	// First delivery: no compression (passthrough).
	inserted, err := PersistSource(d, src, tc)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r1, err := PersistArtifact(d, src, tc, inserted, nil)
	if err != nil {
		t.Fatalf("first artifact: %v", err)
	}
	if r1.Transformed {
		t.Error("first should not be transformed (passthrough)")
	}
	if r1.Output != "some output" {
		t.Errorf("first output = %q, want source output", r1.Output)
	}

	// Exact retry: still passthrough.
	inserted2, err := PersistSource(d, src, tc)
	if err != nil {
		t.Fatalf("retry source: %v", err)
	}
	r2, err := PersistArtifact(d, src, tc, inserted2, nil)
	if err != nil {
		t.Fatalf("retry artifact: %v", err)
	}
	if r2.Transformed {
		t.Error("retry should not be transformed (passthrough, no prior artifact)")
	}
	if r2.Output != "some output" {
		t.Errorf("retry output = %q, want source output", r2.Output)
	}
	assertCompressedCount(t, d, src.ID, 0)
}

// --- helpers for pointer types ---

func ptrInt64(v int64) *int64 { return &v }
