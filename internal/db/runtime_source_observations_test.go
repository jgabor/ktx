package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestExplicitCollectionJoinsRunsAndRetainsOnlyHookTiming(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "collection.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	collectionID := "collectionabcdef"
	base := time.Now().UTC().Add(-time.Minute)
	interval, err := d.EnsureCollectionInterval(collectionID, []string{"run"}, base.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if interval.ID != collectionID {
		t.Fatalf("interval id = %q", interval.ID)
	}
	if _, err := d.EnsureCollectionInterval(collectionID, []string{"hook"}, base.Format(time.RFC3339Nano)); !errors.Is(err, ErrContinuityInvalid) {
		t.Fatalf("mismatched ingress error = %v", err)
	}

	for i, pair := range [][2]time.Time{{base, base.Add(10 * time.Second)}, {base.Add(20 * time.Second), base.Add(30 * time.Second)}} {
		sessionID, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if err := d.InsertSession(&Session{ID: sessionID, Agent: "opencode", AgentAttributionAuthority: AgentAttributionIdentified, StartedAt: pair[0].Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"started", "stopped"} {
			at := pair[0]
			if state == "stopped" {
				at = pair[1]
			}
			if err := d.RetainIngressObservation(IngressObservation{IntervalID: collectionID, Ingress: "run", InstanceID: sessionID, State: state, ObservedAt: at.Format(time.RFC3339Nano), Authority: IngressProcessAuthority}); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			producer := ProducerHook
			runtime := "opencode"
			sourceID, _ := NewID()
			if err := d.PersistToolCall(&ToolCall{ID: sourceID, SessionID: sessionID, Tool: "bash", Input: `{}`, Output: "ok", Timestamp: pair[1].Format(time.RFC3339Nano), Runtime: &runtime, Producer: &producer}); err != nil {
				t.Fatal(err)
			}
			if err := d.RecordCompressionDisposition(&CompressionDisposition{SourceID: sourceID, Disposition: CompressionDispositionIntentionalPassthrough, ObservedAt: pair[1].Format(time.RFC3339Nano)}); err != nil {
				t.Fatal(err)
			}
			latency := int64(7)
			if err := d.RetainRuntimeSourceObservation(RuntimeSourceObservation{CollectionID: collectionID, SessionID: sessionID, SourceID: sourceID, LatencyMs: &latency, LatencyState: LatencyStateMeasured, LatencyUnit: LatencyUnitMilliseconds, LatencyAuthority: LatencyAuthorityRuntime, ObservedAt: pair[1].Format(time.RFC3339Nano)}, "ok"); err != nil {
				t.Fatal(err)
			}
			if err := d.RetainRuntimeSourceObservation(RuntimeSourceObservation{CollectionID: collectionID, SessionID: sessionID, SourceID: sourceID, LatencyMs: &latency, LatencyState: LatencyStateMeasured, LatencyUnit: LatencyUnitMilliseconds, LatencyAuthority: LatencyAuthorityRuntime, ObservedAt: pair[1].Format(time.RFC3339Nano)}, "ok"); !errors.Is(err, ErrContinuityInvalid) {
				t.Fatalf("duplicate timing error = %v", err)
			}
		}
	}

	result, err := d.EvaluateContinuity(collectionID, base, base.Add(31*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Gaps) != 2 || result.Gaps[0].Reason != "missing" || result.Gaps[1].Reason != "missing" {
		t.Fatalf("gaps = %#v, want both between-run and post-run gaps", result.Gaps)
	}
}

func TestRuntimeSourceObservationRejectsWrapperOwnedSource(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "collection.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	collectionID, sessionID, sourceID := "collectionabcdef", "sessionabcdefghi", "sourceabcdefghij"
	now := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	if _, err := d.EnsureCollectionInterval(collectionID, []string{"run"}, now); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&Session{ID: sessionID, Agent: "opencode", AgentAttributionAuthority: AgentAttributionIdentified, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	producer, runtime := ProducerRun, "opencode"
	if err := d.PersistToolCall(&ToolCall{ID: sourceID, SessionID: sessionID, Tool: "bash", Input: `{}`, Output: "ok", Timestamp: now, Runtime: &runtime, Producer: &producer}); err != nil {
		t.Fatal(err)
	}
	if err := d.RetainRuntimeSourceObservation(RuntimeSourceObservation{CollectionID: collectionID, SessionID: sessionID, SourceID: sourceID, LatencyState: LatencyStateUnknown, LatencyUnit: LatencyUnitMilliseconds, LatencyAuthority: LatencyAuthorityUnknown, ObservedAt: now}, "ok"); !errors.Is(err, ErrContinuityInvalid) {
		t.Fatalf("wrapper source error = %v", err)
	}
}

func TestRuntimeSourceObservationValidatesDeliveredOutputAuthority(t *testing.T) {
	type scenario struct {
		name                                                        string
		artifact, cue, later, mismatch, fakeCue, missingDisposition bool
		wantErr, wantCue                                            bool
	}
	for _, tt := range []scenario{{name: "passthrough"}, {name: "cue-free artifact", artifact: true}, {name: "acknowledged cue", artifact: true, cue: true, wantCue: true}, {name: "mismatched delivered bytes", artifact: true, mismatch: true, wantErr: true}, {name: "fake cue substring", fakeCue: true}, {name: "missing disposition", missingDisposition: true, wantErr: true}, {name: "missing acknowledgement", artifact: true, cue: false, mismatch: true, wantErr: true}, {name: "later artifact", artifact: true, later: true, wantErr: true}} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := Open(filepath.Join(t.TempDir(), "authority.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			now := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
			collection, session, source := "collectionabcdef", "sessionabcdefghi", "sourceabcdefghij"
			if _, err := d.EnsureCollectionInterval(collection, []string{"run"}, now); err != nil {
				t.Fatal(err)
			}
			if err := d.InsertSession(&Session{ID: session, Agent: "opencode", AgentAttributionAuthority: AgentAttributionIdentified, StartedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := d.RetainIngressObservation(IngressObservation{IntervalID: collection, Ingress: "run", InstanceID: session, State: "started", ObservedAt: now, Authority: IngressProcessAuthority}); err != nil {
				t.Fatal(err)
			}
			original := "original"
			if tt.fakeCue {
				original = "original `ktx read " + source + "`"
			}
			producer, runtime := ProducerHook, "opencode"
			if err := d.PersistToolCall(&ToolCall{ID: source, SessionID: session, Tool: "read", Input: `{}`, Output: original, ContentHash: ContentHash(original), Timestamp: now, Runtime: &runtime, Producer: &producer}); err != nil {
				t.Fatal(err)
			}
			delivered := original
			if tt.artifact {
				firstID := "artifactabcdefgh"
				if err := d.InsertCompressed(&Compressed{ID: firstID, SourceID: source, Output: "short", ContentHash: ContentHash("short"), Pipeline: `{}`, Timestamp: now, Producer: &producer}); err != nil {
					t.Fatal(err)
				}
				authorityID := firstID
				if tt.later {
					authorityID = "artifactbcdefghi"
					if err := d.InsertCompressed(&Compressed{ID: authorityID, SourceID: source, Output: "later", ContentHash: ContentHash("later"), Pipeline: `{}`, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Producer: &producer}); err != nil {
						t.Fatal(err)
					}
				}
				if err := d.RecordCompressionDisposition(&CompressionDisposition{SourceID: source, Disposition: CompressionDispositionArtifactPersisted, ArtifactID: firstID, ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
				if tt.later {
					if _, err := d.DB().Exec(`UPDATE compression_dispositions SET artifact_id=? WHERE source_id=?`, authorityID, source); err != nil {
						t.Fatal(err)
					}
				}
				delivered = "short"
				if tt.cue {
					if err := d.AcknowledgeRecoveryCue(RecoveryCueDelivery{CollectionID: collection, SourceID: source, ArtifactID: firstID, SessionID: session, RuntimeSessionID: "ses", RuntimeCallID: "call", DeliveredAt: now, Authority: RecoveryDeliveryAuthority}); err != nil {
						t.Fatal(err)
					}
					delivered += RecoveryFooter(source, len(original), len("short"))
				}
			} else if !tt.missingDisposition {
				if err := d.RecordCompressionDisposition(&CompressionDisposition{SourceID: source, Disposition: CompressionDispositionIntentionalPassthrough, ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			if tt.mismatch {
				if tt.artifact && !tt.cue {
					delivered = "short" + RecoveryFooter(source, len(original), len("short"))
				} else {
					delivered += "x"
				}
			}
			err = d.RetainRuntimeSourceObservation(RuntimeSourceObservation{CollectionID: collection, SessionID: session, SourceID: source, LatencyState: LatencyStateUnknown, LatencyUnit: LatencyUnitMilliseconds, LatencyAuthority: LatencyAuthorityUnknown, ObservedAt: now}, delivered)
			if tt.wantErr {
				if !errors.Is(err, ErrContinuityInvalid) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var cue bool
			if err := d.DB().QueryRow(`SELECT recovery_cue_observed FROM runtime_source_observations`).Scan(&cue); err != nil {
				t.Fatal(err)
			}
			if cue != tt.wantCue {
				t.Fatalf("cue=%v want %v", cue, tt.wantCue)
			}
		})
	}
}
