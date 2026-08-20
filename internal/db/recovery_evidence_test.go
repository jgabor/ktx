package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryCueAndRepeatedAttemptsRemainAuditable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	collectionID, sessionID, sourceID, artifactID := "collectionabcdef", "sessionabcdefghi", "sourceabcdefghij", "artifactabcdefgh"
	if _, err := d.EnsureCollectionInterval(collectionID, []string{"run"}, now); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&Session{ID: sessionID, Agent: "opencode", AgentAttributionAuthority: AgentAttributionIdentified, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	producer, runtime := ProducerHook, "opencode"
	if err := d.PersistToolCall(&ToolCall{ID: sourceID, SessionID: sessionID, Tool: "bash", Input: `{}`, Output: "original", ContentHash: ContentHash("original"), Timestamp: now, Runtime: &runtime, Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertCompressed(&Compressed{ID: artifactID, SourceID: sourceID, Output: "short", ContentHash: ContentHash("short"), Pipeline: `{}`, Timestamp: now, Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	secondArtifact := "artifactbcdefghi"
	if err := d.InsertCompressed(&Compressed{ID: secondArtifact, SourceID: sourceID, Output: "later", ContentHash: ContentHash("later"), Pipeline: `{}`, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	delivery := RecoveryCueDelivery{CollectionID: collectionID, SourceID: sourceID, ArtifactID: artifactID, SessionID: sessionID, RuntimeSessionID: "ses_1", RuntimeCallID: "call_1", DeliveredAt: now, Authority: RecoveryDeliveryAuthority}
	if err := d.AcknowledgeRecoveryCue(delivery); err != nil {
		t.Fatal(err)
	}
	nonAuthoritative := delivery
	nonAuthoritative.ArtifactID = secondArtifact
	if err := d.AcknowledgeRecoveryCue(nonAuthoritative); !errors.Is(err, ErrContinuityInvalid) {
		t.Fatalf("later artifact acknowledgement error = %v", err)
	}
	if err := d.AcknowledgeRecoveryCue(delivery); err != nil {
		t.Fatalf("idempotent acknowledgement: %v", err)
	}
	conflict := delivery
	conflict.RuntimeCallID = "call_2"
	if err := d.AcknowledgeRecoveryCue(conflict); !errors.Is(err, ErrContinuityInvalid) {
		t.Fatalf("conflict error = %v", err)
	}
	for i, attemptID := range []string{"attemptaabcdefgh", "attemptabcdefghb"} {
		if err := d.PersistToolCall(&ToolCall{ID: attemptID, SessionID: sessionID, Tool: "bash", Input: `{}`, Output: "result", Timestamp: now, Runtime: &runtime, Producer: &producer}); err != nil {
			t.Fatal(err)
		}
		exact := i == 0
		attempt := RecoveryAttempt{AttemptSourceID: attemptID, CollectionID: collectionID, CueSourceID: sourceID, AttemptedAt: now, ResultState: "succeeded", ByteExact: &exact, Authority: RecoveryAttemptAuthority}
		if err := d.RetainRecoveryAttempt(attempt); err != nil {
			t.Fatal(err)
		}
		if err := d.RetainRecoveryAttempt(attempt); err != nil {
			t.Fatalf("idempotent attempt: %v", err)
		}
	}
	var attempts, cues int
	if err := d.DB().QueryRow(`SELECT count(*) FROM recovery_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := d.DB().QueryRow(`SELECT count(DISTINCT collection_id || ':' || cue_source_id) FROM recovery_attempts`).Scan(&cues); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || cues != 1 {
		t.Fatalf("attempts=%d distinct cues=%d", attempts, cues)
	}
}
