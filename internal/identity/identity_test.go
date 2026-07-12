package identity

import "testing"

func TestDeriveNormalizesOnlyContractedBytes(t *testing.T) {
	t.Parallel()

	got, err := Derive(" \tCoDeX\r\n", " session A ", " exec-Ab C ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := Derive("codex", "session A", "exec-Ab C")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("normalized derivation = %q, want %q", got, want)
	}
	if !IsCanonical(got) {
		t.Fatalf("derived ID %q is not canonical", got)
	}

	caseChanged, err := Derive("codex", "session A", "exec-ab C")
	if err != nil {
		t.Fatal(err)
	}
	if caseChanged == got {
		t.Fatal("source ID case was normalized; want opaque preservation")
	}
}

func TestDeriveRejectsUnusableTuple(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		runtime   string
		sessionID string
		sourceID  string
	}{
		{name: "empty runtime", sessionID: "session", sourceID: "exec-1"},
		{name: "whitespace session", runtime: "codex", sessionID: " \t\n", sourceID: "exec-1"},
		{name: "invalid source utf8", runtime: "codex", sessionID: "session", sourceID: string([]byte{0xff})},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Derive(tt.runtime, tt.sessionID, tt.sourceID); err == nil {
				t.Fatal("Derive succeeded; want error")
			}
		})
	}
}

func TestDeriveIsDeterministic(t *testing.T) {
	t.Parallel()

	first, err := Derive("codex", "session", "exec-d0a88822-65de-4d16-8c37-df6a0aae9a48")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Derive("codex", "session", "exec-d0a88822-65de-4d16-8c37-df6a0aae9a48")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("Derive returned %q then %q", first, second)
	}
	if first != "yeidpaaatyvvzrfo" {
		t.Fatalf("Derive returned %q, want stable cross-runtime vector %q", first, "yeidpaaatyvvzrfo")
	}
}
