package experiment

import (
	"path/filepath"
	"testing"

	"github.com/jgabor/ktx/internal/db"
)

func TestAttemptOutcomesRetainLinkedRecoveryAndOpaqueExactRepeats(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, producer, runtime := "abcdefghijklmnop", db.ProducerHook, "opencode"
	if err := database.InsertSession(&db.Session{ID: owner, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	insertCall := func(id, tool, input, at string) {
		t.Helper()
		if err := database.PersistToolCall(&db.ToolCall{ID: id, SessionID: owner, Tool: tool, Input: input, Output: "result", Timestamp: at, Runtime: &runtime, Producer: &producer}); err != nil {
			t.Fatal(err)
		}
	}
	insertCall("sourceabcdefghij", "bash", `{"b":2,"a":1}`, "2026-08-26T00:00:00Z")
	insertCall("attemptaabcdefgh", "bash", `{"a":1,"b":2}`, "2026-08-26T00:00:02Z")
	insertCall("attemptabcdefghb", "bash", `{"a":1,"b":2}`, "2026-08-26T00:00:03Z")
	if err := database.InsertCompressed(&db.Compressed{ID: "artifactabcdefgh", SourceID: "sourceabcdefghij", Output: "short", Pipeline: `{}`, Timestamp: "2026-08-26T00:00:00Z", Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureCollectionInterval("collectionabcdef", []string{"run"}, "2026-08-26T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := database.AcknowledgeRecoveryCue(db.RecoveryCueDelivery{CollectionID: "collectionabcdef", SourceID: "sourceabcdefghij", ArtifactID: "artifactabcdefgh", SessionID: owner, RuntimeSessionID: "runtime", RuntimeCallID: "cue", DeliveredAt: "2026-08-26T00:00:01Z", Authority: db.RecoveryDeliveryAuthority}); err != nil {
		t.Fatal(err)
	}
	exact := true
	if err := database.RetainRecoveryAttempt(db.RecoveryAttempt{AttemptSourceID: "attemptaabcdefgh", CollectionID: "collectionabcdef", CueSourceID: "sourceabcdefghij", AttemptedAt: "2026-08-26T00:00:02Z", ResultState: "succeeded", ByteExact: &exact, Authority: db.RecoveryAttemptAuthority}); err != nil {
		t.Fatal(err)
	}
	if err := database.RetainRecoveryAttempt(db.RecoveryAttempt{AttemptSourceID: "attemptabcdefghb", CollectionID: "collectionabcdef", CueSourceID: "sourceabcdefghij", AttemptedAt: "2026-08-26T00:00:03Z", ResultState: "failed", Authority: db.RecoveryAttemptAuthority}); err != nil {
		t.Fatal(err)
	}

	got := materializeAttemptOutcomes(database.DB(), owner)
	if got == nil {
		t.Fatal("outcomes unavailable")
	}
	if got.Recovery["cue_count"] != 1 || got.Recovery["attempt_count"] != 2 || got.Recovery["repeated_attempt_count"] != 1 {
		t.Fatalf("recovery counts = %#v", got.Recovery)
	}
	if got.Recovery["outcomes"].(map[string]any)["succeeded"] != 1 || got.Recovery["outcomes"].(map[string]any)["failed"] != 1 || got.Recovery["successful_results"].(map[string]any)["byte_exact"] != 1 {
		t.Fatalf("recovery outcomes = %#v", got.Recovery)
	}
	if got.Repeats["call_count"] != 3 || got.Repeats["unique_call_count"] != 1 || got.Repeats["repeated_call_count"] != 2 {
		t.Fatalf("repeat counts = %#v", got.Repeats)
	}
	classes := got.Repeats["equality_class_counts"].([]int)
	if len(classes) != 1 || classes[0] != 3 {
		t.Fatalf("equality classes = %#v", classes)
	}
}

func TestAttemptOutcomesRejectPreCueAndCrossAttemptEvidence(t *testing.T) {
	for _, test := range []struct {
		name, attemptedAt, attemptOwner string
	}{
		{name: "pre-cue", attemptedAt: "2026-08-26T00:00:00Z", attemptOwner: "abcdefghijklmnop"},
		{name: "cross-attempt", attemptedAt: "2026-08-26T00:00:02Z", attemptOwner: "ponmlkjihgfedcba"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := db.Open(filepath.Join(t.TempDir(), "attempt.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			for _, owner := range []string{"abcdefghijklmnop", "ponmlkjihgfedcba"} {
				if err := database.InsertSession(&db.Session{ID: owner, Agent: "opencode"}); err != nil {
					t.Fatal(err)
				}
			}
			producer, runtime := db.ProducerHook, "opencode"
			for _, call := range []struct{ id, owner string }{{"sourceabcdefghij", "abcdefghijklmnop"}, {"attemptaabcdefgh", test.attemptOwner}} {
				if err := database.PersistToolCall(&db.ToolCall{ID: call.id, SessionID: call.owner, Tool: "bash", Input: `{}`, Output: "x", Timestamp: test.attemptedAt, Runtime: &runtime, Producer: &producer}); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.InsertCompressed(&db.Compressed{ID: "artifactabcdefgh", SourceID: "sourceabcdefghij", Output: "x", Pipeline: `{}`, Timestamp: "2026-08-26T00:00:00Z", Producer: &producer}); err != nil {
				t.Fatal(err)
			}
			if _, err := database.EnsureCollectionInterval("collectionabcdef", []string{"run"}, "2026-08-26T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			_, err = database.DB().Exec(`INSERT INTO recovery_cue_deliveries VALUES (?,?,?,?,?,?,?,?)`, "collectionabcdef", "sourceabcdefghij", "artifactabcdefgh", "abcdefghijklmnop", "runtime", "cue", "2026-08-26T00:00:01Z", db.RecoveryDeliveryAuthority)
			if err != nil {
				t.Fatal(err)
			}
			_, err = database.DB().Exec(`INSERT INTO recovery_attempts VALUES (?,?,?,?,?,?,?)`, "attemptaabcdefgh", "collectionabcdef", "sourceabcdefghij", test.attemptedAt, "succeeded", true, db.RecoveryAttemptAuthority)
			if err != nil {
				t.Fatal(err)
			}
			if got := materializeAttemptOutcomes(database.DB(), "abcdefghijklmnop"); got != nil {
				t.Fatalf("invalid evidence materialized: %#v", got)
			}
		})
	}
}

func TestAttemptOutcomesRejectMismatchedCueArtifact(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, producer, runtime := "abcdefghijklmnop", db.ProducerHook, "opencode"
	if err := database.InsertSession(&db.Session{ID: owner, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sourceabcdefghij", "sourcebcdefghijk"} {
		if err := database.PersistToolCall(&db.ToolCall{ID: id, SessionID: owner, Tool: "bash", Input: `{}`, Output: "x", Timestamp: "2026-08-26T00:00:00Z", Runtime: &runtime, Producer: &producer}); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.InsertCompressed(&db.Compressed{ID: "artifactabcdefgh", SourceID: "sourcebcdefghijk", Output: "x", Pipeline: `{}`, Timestamp: "2026-08-26T00:00:00Z", Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureCollectionInterval("collectionabcdef", []string{"run"}, "2026-08-26T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`INSERT INTO recovery_cue_deliveries VALUES (?,?,?,?,?,?,?,?)`, "collectionabcdef", "sourceabcdefghij", "artifactabcdefgh", owner, "runtime", "cue", "2026-08-26T00:00:01Z", db.RecoveryDeliveryAuthority); err != nil {
		t.Fatal(err)
	}
	if got := materializeAttemptOutcomes(database.DB(), owner); got != nil {
		t.Fatalf("mismatched evidence materialized: %#v", got)
	}
}

func TestAttemptOutcomesMarkOldArchiveCueAuthorityUnavailable(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, producer, runtime := "abcdefghijklmnop", db.ProducerHook, "opencode"
	if err := database.InsertSession(&db.Session{ID: owner, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	if err := database.PersistToolCall(&db.ToolCall{ID: "sourceabcdefghij", SessionID: owner, Tool: "read", Input: `{}`, Output: "original", Timestamp: "2026-08-26T00:00:00Z", Runtime: &runtime, Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	if err := database.InsertCompressed(&db.Compressed{ID: "artifactabcdefgh", SourceID: "sourceabcdefghij", Output: "short", Pipeline: `{}`, Timestamp: "2026-08-26T00:00:00Z", Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	got := materializeAttemptOutcomes(database.DB(), owner)
	if got == nil || got.Recovery["state"] != "unavailable" || got.Recovery["reason"] != "cue_delivery_authority_unavailable" || got.Recovery["cue_count"] != nil {
		t.Fatalf("recovery outcome=%#v", got)
	}
	if got.Repeats["state"] != "measured" {
		t.Fatalf("repeat outcome=%#v", got.Repeats)
	}
}
