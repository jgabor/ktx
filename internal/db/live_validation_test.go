package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLiveValidationDataUsesHalfOpenSourceBounds(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "window.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	collection, session := "collectionabcdef", "sessionabcdefghi"
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	if _, err := d.EnsureCollectionInterval(collection, []string{"run"}, from.Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&Session{ID: session, Agent: "opencode", AgentAttributionAuthority: AgentAttributionIdentified, StartedAt: from.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := d.RetainIngressObservation(IngressObservation{IntervalID: collection, Ingress: "run", InstanceID: session, State: "started", ObservedAt: from.Format(time.RFC3339Nano), Authority: IngressProcessAuthority}); err != nil {
		t.Fatal(err)
	}
	producer, runtime, success := ProducerHook, "opencode", false
	for i, at := range []time.Time{from.Add(-time.Nanosecond), from, from.Add(time.Nanosecond), to.Add(-time.Nanosecond), to} {
		id := []string{"beforeboundaaaaa", "insideboundaaaaa", "afterfromaaaaaaa", "beforetoaaaaaaaa", "upperboundaaaaaa"}[i]
		if err := d.PersistToolCall(&ToolCall{ID: id, SessionID: session, Tool: "read", Input: `{}`, Output: "value", Timestamp: at.Format(time.RFC3339Nano), Runtime: &runtime, Producer: &producer, IsError: &success}); err != nil {
			t.Fatal(err)
		}
		if err := d.RecordCompressionDisposition(&CompressionDisposition{SourceID: id, Disposition: CompressionDispositionIntentionalPassthrough, ObservedAt: at.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		if err := d.RetainRuntimeSourceObservation(RuntimeSourceObservation{CollectionID: collection, SessionID: session, SourceID: id, LatencyState: LatencyStateUnknown, LatencyUnit: LatencyUnitMilliseconds, LatencyAuthority: LatencyAuthorityUnknown, ObservedAt: at.Add(time.Second).Format(time.RFC3339Nano)}, "value"); err != nil {
			t.Fatal(err)
		}
	}
	data, err := d.LiveValidationDataFor(collection, "run", from, to)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, source := range data.Sources {
		got[source.ID] = true
	}
	if len(data.Sources) != 3 || !got["insideboundaaaaa"] || !got["afterfromaaaaaaa"] || !got["beforetoaaaaaaaa"] {
		t.Fatalf("sources=%+v", data.Sources)
	}
}

func TestLiveValidationRecoveryAttemptsUseHalfOpenAttemptBounds(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "recovery-window.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	collection, session, source := "collectionabcdef", "sessionabcdefghi", "sourceabcdefghij"
	producer, runtime := ProducerHook, "opencode"
	if _, err := d.EnsureCollectionInterval(collection, []string{"run"}, from.Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&Session{ID: session, Agent: "opencode", AgentAttributionAuthority: AgentAttributionIdentified, StartedAt: from.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	sourceAt := from.Add(time.Minute).Format(time.RFC3339Nano)
	if err := d.PersistToolCall(&ToolCall{ID: source, SessionID: session, Tool: "read", Input: `{}`, Output: "original", ContentHash: ContentHash("original"), Timestamp: sourceAt, Runtime: &runtime, Producer: &producer, IsError: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	artifact := "artifactabcdefgh"
	if err := d.InsertCompressed(&Compressed{ID: artifact, SourceID: source, Output: "short", ContentHash: ContentHash("short"), Pipeline: `{}`, Timestamp: sourceAt, Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordCompressionDisposition(&CompressionDisposition{SourceID: source, Disposition: CompressionDispositionArtifactPersisted, ArtifactID: artifact, ObservedAt: sourceAt}); err != nil {
		t.Fatal(err)
	}
	if err := d.AcknowledgeRecoveryCue(RecoveryCueDelivery{CollectionID: collection, SourceID: source, ArtifactID: artifact, SessionID: session, RuntimeSessionID: "ses", RuntimeCallID: "call", DeliveredAt: sourceAt, Authority: RecoveryDeliveryAuthority}); err != nil {
		t.Fatal(err)
	}
	add := func(id string, at time.Time) {
		if err := d.PersistToolCall(&ToolCall{ID: id, SessionID: session, Tool: "bash", Input: `{}`, Output: "result", Timestamp: at.Format(time.RFC3339Nano), Runtime: &runtime, Producer: &producer}); err != nil {
			t.Fatal(err)
		}
		exact := true
		if err := d.RetainRecoveryAttempt(RecoveryAttempt{AttemptSourceID: id, CollectionID: collection, CueSourceID: source, AttemptedAt: at.Format(time.RFC3339Nano), ResultState: "succeeded", ByteExact: &exact, Authority: RecoveryAttemptAuthority}); err != nil {
			t.Fatal(err)
		}
	}
	add("attemptbeforeaaa", from.Add(-time.Second))
	add("attemptatfromaaa", from)
	add("attemptfromplusa", from.Add(time.Nanosecond))
	add("attempttominusaa", to.Add(-time.Nanosecond))
	data, err := d.LiveValidationDataFor(collection, "run", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Recoveries) != 3 {
		t.Fatalf("initial recoveries=%+v", data.Recoveries)
	}
	want := map[string]bool{"attemptatfromaaa": true, "attemptfromplusa": true, "attempttominusaa": true}
	for _, recovery := range data.Recoveries {
		delete(want, recovery.AttemptSourceID)
	}
	if len(want) != 0 {
		t.Fatalf("missing in-window attempts: %v; got %+v", want, data.Recoveries)
	}
	add("attemptattoaaaaa", to)
	add("attemptafteraaaa", to.Add(time.Second))
	again, err := d.LiveValidationDataFor(collection, "run", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Recoveries) != 3 {
		t.Fatalf("later attempts changed earlier window: %+v", again.Recoveries)
	}
}

func boolPtr(v bool) *bool { return &v }
