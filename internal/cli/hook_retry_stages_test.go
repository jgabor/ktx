package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestHookRetainedArtifactSkipsStages(t *testing.T) {
	for _, runtime := range []string{"opencode", "codex"} {
		for _, original := range []string{strings.Repeat("retained line with padding\n", 100), "a\nb\n"} {
			t.Run(runtime+"/"+original[:1], func(t *testing.T) {
				d := hookTestDB(t)
				hookChdirTemp(t)
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				marker := filepath.Join(t.TempDir(), "stage-runs")
				command := filepath.Join(t.TempDir(), "count")
				if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf x >> '"+marker+"'\ncat\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				stageConfig := "[pipeline.default]\nstages = [\"count\", \"head:1\"]\n[[plugin]]\nname = \"count\"\ncommand = \"" + command + "\"\n"
				payload := openCodeHookPayload(t, "retained-session", "retained-call", "bash", `{"command":"true"}`, original)
				sourceID := mustOpenCodeID(t, "retained-session", "retained-call")
				if runtime == "codex" {
					payload = codexPayload(t, "retained-call", "Bash", original)
					sourceID = mustCodexID(t, "codex-test-session", "retained-call")
				}
				// A completed source-only delivery must not prevent a later artifact.
				if _, _, _, err := runObservedHook(t, d, payload); err != nil {
					t.Fatal(err)
				}
				assertHookEvidenceCounts(t, d, sourceID, 1, 0, 1)
				hookWriteUserConfig(t, stageConfig)
				_, firstErr, _, err := runObservedHook(t, d, payload)
				if err != nil || firstErr != "" {
					t.Fatalf("first error=%v stderr=%q", err, firstErr)
				}
				source, err := d.GetToolCall(sourceID)
				if err != nil {
					t.Fatal(err)
				}
				artifact, err := d.GetCompressedBySource(sourceID)
				if err != nil {
					t.Fatal(err)
				}
				disposition, err := d.GetCompressionDisposition(sourceID)
				if err != nil {
					t.Fatal(err)
				}
				conflict := strings.Replace(payload, "bash", "read", 1)
				if runtime == "codex" {
					conflict = strings.Replace(payload, "Bash", "Read", 1)
				}
				stdout, _, observations, err := runObservedHook(t, d, conflict)
				if exitCodeOf(err) != ExitConflict || stdout != "" || len(observations) != 0 {
					t.Fatalf("conflict error=%v stdout=%q observations=%v", err, stdout, observations)
				}
				for _, retry := range []struct {
					name, config      string
					footer, reconcile bool
				}{
					{"same", stageConfig, false, false},
					{"changed", strings.Replace(stageConfig, "head:1", "head:2", 1), false, false},
					{"bad stage", "[pipeline.default]\nstages = [\"head:not-a-number\"]\n", false, false},
					{"routed footer", "[pipeline.default]\nstages = []\nrecover_footer = true\n[pipeline.retry]\nstages = [\"head:not-a-number\"]\n[[route]]\nprofile = \"retry\"\n", true, false},
					{"malformed", "[pipeline.default\n", false, false},
					{"missing disposition", stageConfig, false, true},
				} {
					t.Run(retry.name, func(t *testing.T) {
						if retry.reconcile {
							if _, err := d.DB().Exec(`DELETE FROM compression_dispositions WHERE source_id = ?`, sourceID); err != nil {
								t.Fatal(err)
							}
						}
						hookWriteUserConfig(t, retry.config)
						stdout, stderr, observations, err := runObservedHook(t, d, payload)
						if err != nil || stderr != "" {
							t.Fatalf("retry error=%v stderr=%q", err, stderr)
						}
						if !retry.reconcile && len(observations) != 0 {
							t.Fatalf("complete retry wrote: %v", observations)
						}
						want := artifact.Output
						if retry.footer {
							want, _ = recoveryCueOutput(sourceID, original, want)
						}
						if runtime == "codex" {
							var got codexHookOutput
							if err := json.Unmarshal([]byte(stdout), &got); err != nil {
								t.Fatal(err)
							}
							if got.Decision != "block" || got.Reason != want {
								t.Fatalf("response=%+v want=%q", got, want)
							}
						} else {
							var got hookEnvelope
							if err := json.Unmarshal([]byte(stdout), &got); err != nil {
								t.Fatal(err)
							}
							if got.Output != want || got.CompressedID == nil || *got.CompressedID != artifact.ID || got.RecoveryCue != strings.Contains(want, "`ktx read ") {
								t.Fatalf("response=%+v want=%q", got, want)
							}
						}
						data, err := os.ReadFile(marker)
						if err != nil || string(data) != "x" {
							t.Fatalf("stage runs=%q err=%v; want one execution", data, err)
						}
						gotSource, err := d.GetToolCall(sourceID)
						if err != nil || !reflect.DeepEqual(source, gotSource) {
							t.Fatalf("source changed: %v", err)
						}
						gotArtifact, err := d.GetCompressedBySource(sourceID)
						if err != nil || !reflect.DeepEqual(artifact, gotArtifact) {
							t.Fatalf("artifact changed: %v", err)
						}
						gotDisposition, err := d.GetCompressionDisposition(sourceID)
						if err != nil || gotDisposition.ArtifactID != artifact.ID || gotDisposition.Disposition != dbpkg.CompressionDispositionArtifactPersisted {
							t.Fatalf("disposition=%+v err=%v", gotDisposition, err)
						}
						if !retry.reconcile && !reflect.DeepEqual(disposition, gotDisposition) {
							t.Fatal("complete retry changed disposition")
						}
						assertHookEvidenceCounts(t, d, sourceID, 1, 1, 1)
					})
				}
			})
		}
	}
}
