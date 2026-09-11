package experiment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/usage"
)

type countedStream struct {
	io.Reader
	bytes int64
}

func (r *countedStream) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

func TestHashStreamCallsEquivalence(t *testing.T) {
	call := `{"type":"tool_use","sessionID":"session","part":{"callID":"call","state":{"status":"completed"}}}`
	for _, stream := range []string{
		"", call, call + "\r\n" + call + "\n",
		call + "\n" + strings.ReplaceAll(call, "completed", "error"),
		"broken\n" + call + "\n",
		`{"type":"tool_use"}` + "\n" + strings.Repeat("uninterpreted tail\n", 10000),
	} {
		reader := &countedStream{Reader: strings.NewReader(stream)}
		hash, calls, err := hashStreamCalls(reader)
		want := readStreamCallAuthority(strings.NewReader(stream))
		if err != nil || hash != sha256Bytes([]byte(stream)) || !reflect.DeepEqual(calls, want) || reader.bytes != int64(len(stream)) {
			t.Fatalf("hash=%s calls=%#v err=%v bytes=%d want=%#v", hash, calls, err, reader.bytes, want)
		}
	}
}

func TestHashStreamCallsPreservesRecordBoundary(t *testing.T) {
	for _, length := range []int{usage.MaxRecordSize - 1, usage.MaxRecordSize, usage.MaxRecordSize + 1} {
		// Whitespace exercises the scanner boundary without retaining a second
		// full payload in the test. A later call must remain unavailable after
		// an oversized record, but its bytes still belong to the stream hash.
		newReader := func() io.Reader {
			return io.MultiReader(io.LimitReader(spaceReader{}, int64(length)), strings.NewReader("\n"+`{"type":"tool_use","sessionID":"s","part":{"callID":"c"}}`))
		}
		digest := sha256.New()
		if _, err := io.Copy(digest, newReader()); err != nil {
			t.Fatal(err)
		}
		hash, got, err := hashStreamCalls(newReader())
		want := readStreamCallAuthority(newReader())
		if err != nil || hash != hex.EncodeToString(digest.Sum(nil)) || !reflect.DeepEqual(got, want) {
			t.Fatalf("length=%d got=%#v want=%#v hash=%s err=%v", length, got, want, hash, err)
		}
	}
}

type spaceReader struct{}

func (spaceReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

type onceReadError struct{ failed bool }

func (r *onceReadError) Read([]byte) (int, error) {
	if !r.failed {
		r.failed = true
		return 0, io.ErrUnexpectedEOF
	}
	return 0, io.EOF
}

func TestHashStreamCallsRetainsReadError(t *testing.T) {
	if hash, _, err := hashStreamCalls(&onceReadError{}); hash != "" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("hash=%q err=%v", hash, err)
	}
}

func TestStreamingLedgerMatchesMaterializedCollection(t *testing.T) {
	dir, manifest, first := writeAuthorityFixture(t)
	second := first
	second.AttemptID, second.Side = "candidate-001", Candidate
	drift := first
	drift.TokensIn, drift.AnswerExcerpt = float64Pointer(123), "projection drift"
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	writeAttemptRecords(t, dir, first, second, drift, second, first)
	a, err := openArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	loaded, err := a.loadAttempts()
	if err != nil {
		t.Fatal(err)
	}
	for i := range loaded {
		reasons, err := a.attemptArtifactReasons(&loaded[i])
		if err != nil {
			t.Fatal(err)
		}
		loaded[i].validationReasons = reasons
	}
	want, wantDivergences, err := a.materializeAttempts(manifest, loaded)
	if err != nil {
		t.Fatal(err)
	}
	_, got, divergences, err := loadExperimentEvidence(a)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(divergences, wantDivergences) {
		t.Fatal("streaming ledger changed authority or grouped divergence order")
	}
	wantBytes, err := MarshalJSON(BuildSummary(manifest, want))
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := MarshalJSON(BuildSummary(manifest, got))
	if err != nil || !bytes.Equal(gotBytes, wantBytes) {
		t.Fatal("summary bytes changed")
	}
}

func TestStreamingLedgerRejectsLateRecordsBeforePublication(t *testing.T) {
	for _, suffix := range []string{"null\n", "{\n", strings.Repeat(" ", 16*1024*1024) + "\n"} {
		dir, manifest, attempt := writeAuthorityFixture(t)
		if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
			t.Fatal(err)
		}
		writeAttemptRecords(t, dir, attempt)
		if _, err := RegenerateSummary(dir); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(filepath.Join(dir, "summary.json"))
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(dir, "attempts.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteString(suffix)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("write=%v close=%v", writeErr, closeErr)
		}
		if _, err := RegenerateSummary(dir); err == nil {
			t.Fatal("accepted invalid late record")
		}
		if _, err := ValidateExperiment(dir); err == nil {
			t.Fatal("validated invalid late record")
		}
		after, err := os.ReadFile(filepath.Join(dir, "summary.json"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("late error changed summary bytes")
		}
	}
}

func TestAttemptDecodePreservesOwnershipAndCompatibility(t *testing.T) {
	for _, text := range []string{
		`null`, `{"schema_version":1,"answer_excerpt":"first","answer_excerpt":"last","model_steps":"malformed optional"}`,
		`{"SCHEMA_VERSION":2,"schema_version":1,"artifacts":{"stream":"a"},"artifacts":{"database":"b"},"unknown":{"large":"ignored"}}`,
		`{"model_steps":null,"tokens_in":0,"cost":null,"answer_excerpt":"<escaped>"}`,
		`{"schema_version":"bad","model_steps":[]}`, `{"model_steps":[],"Model_Steps":"bad"}`,
	} {
		data := []byte(text)
		var got Attempt
		gotErr := json.Unmarshal(data, &got)
		// The compatibility decoder canonicalizes a top-level map first. Keep
		// duplicate/case-variant key and malformed optional-field behavior.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		steps := fields["model_steps"]
		delete(fields, "model_steps")
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		type plain Attempt
		var want plain
		wantErr := json.Unmarshal(raw, &want)
		if len(steps) != 0 {
			var events []SourceEvent
			if json.Unmarshal(steps, &events) == nil && ValidateSourceEvents(events) == nil {
				want.ModelSteps = events
			}
		}
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("errors got=%v want=%v", gotErr, wantErr)
		}
		if gotErr != nil {
			continue
		}
		for i := range data {
			data[i] = 'x'
		}
		if !reflect.DeepEqual(got, Attempt(want)) {
			t.Fatalf("decode changed: got=%#v want=%#v", got, want)
		}
	}
}
