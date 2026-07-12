package stages

import (
	"bytes"
	"testing"

	"github.com/jgabor/ktx/internal/pipeline"
)

// TestReplace covers the replace stage: literal substitution,
// regex substitution, error cases, edge cases, and registration.
func TestReplace(t *testing.T) {
	t.Run("literal pattern is replaced", func(t *testing.T) {
		stage, err := replaceBuilder("DEBUG::")
		if err != nil {
			t.Fatalf("replaceBuilder error: %v", err)
		}
		input := []byte("[DEBUG] starting up\n[DEBUG] done\n")
		out, err := stage.Apply(input)
		if err != nil {
			t.Fatalf("Apply error: %v", err)
		}
		want := []byte("[] starting up\n[] done\n")
		if !bytes.Equal(out, want) {
			t.Errorf("got %q, want %q", out, want)
		}
	})

	t.Run("regex pattern is replaced", func(t *testing.T) {
		stage, err := replaceBuilder(`\d{4}-\d{2}-\d{2}::DATE`)
		if err != nil {
			t.Fatalf("replaceBuilder error: %v", err)
		}
		input := []byte("2024-01-15 hello\n2025-03-20 world\n")
		out, err := stage.Apply(input)
		if err != nil {
			t.Fatalf("Apply error: %v", err)
		}
		want := []byte("DATE hello\nDATE world\n")
		if !bytes.Equal(out, want) {
			t.Errorf("got %q, want %q", out, want)
		}
	})

	t.Run("empty pattern returns error", func(t *testing.T) {
		_, err := replaceBuilder("::replacement")
		if err == nil {
			t.Error("expected error for empty pattern")
		}
	})

	t.Run("missing separator returns error", func(t *testing.T) {
		_, err := replaceBuilder("DEBUG")
		if err == nil {
			t.Error("expected error for missing :: separator")
		}
	})

	t.Run("invalid regex returns error", func(t *testing.T) {
		_, err := replaceBuilder("[invalid::replacement")
		if err == nil {
			t.Error("expected error for invalid regex")
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		stage, err := replaceBuilder("DEBUG::")
		if err != nil {
			t.Fatalf("replaceBuilder error: %v", err)
		}
		out, err := stage.Apply([]byte{})
		if err != nil {
			t.Fatalf("Apply error: %v", err)
		}
		if out != nil {
			t.Errorf("got %v, want nil", out)
		}
	})

	t.Run("replacement containing :: survives", func(t *testing.T) {
		stage, err := replaceBuilder("DEBUG::a::b")
		if err != nil {
			t.Fatalf("replaceBuilder error: %v", err)
		}
		input := []byte("DEBUG here\n")
		out, err := stage.Apply(input)
		if err != nil {
			t.Fatalf("Apply error: %v", err)
		}
		want := []byte("a::b here\n")
		if !bytes.Equal(out, want) {
			t.Errorf("got %q, want %q", out, want)
		}
	})

	t.Run("pattern matching nothing returns input unchanged", func(t *testing.T) {
		stage, err := replaceBuilder("NOMATCH::replaced")
		if err != nil {
			t.Fatalf("replaceBuilder error: %v", err)
		}
		input := []byte("nothing to replace here\n")
		out, err := stage.Apply(input)
		if err != nil {
			t.Fatalf("Apply error: %v", err)
		}
		if !bytes.Equal(out, input) {
			t.Errorf("got %q, want %q (unchanged)", out, input)
		}
	})

	t.Run("replacement shorter than match reduces output", func(t *testing.T) {
		stage, err := replaceBuilder("LONG_PREFIX_::X:")
		if err != nil {
			t.Fatalf("replaceBuilder error: %v", err)
		}
		input := []byte("LONG_PREFIX_line one\nLONG_PREFIX_line two\n")
		out, err := stage.Apply(input)
		if err != nil {
			t.Fatalf("Apply error: %v", err)
		}
		want := []byte("X:line one\nX:line two\n")
		if !bytes.Equal(out, want) {
			t.Errorf("got %q, want %q", out, want)
		}
		if len(out) >= len(input) {
			t.Errorf("output should be shorter: got %d bytes, input %d bytes", len(out), len(input))
		}
	})

	t.Run("colon in pattern via regex escape", func(t *testing.T) {
		stage, err := replaceBuilder(`http\://[^\s]+::URL`)
		if err != nil {
			t.Fatalf("replaceBuilder error: %v", err)
		}
		input := []byte("see http://example.com for details\n")
		out, err := stage.Apply(input)
		if err != nil {
			t.Fatalf("Apply error: %v", err)
		}
		want := []byte("see URL for details\n")
		if !bytes.Equal(out, want) {
			t.Errorf("got %q, want %q", out, want)
		}
	})
}

// TestRegisterBuiltins_Replace verifies the replace stage is
// registered and resolvable through the Registry.
func TestRegisterBuiltins_Replace(t *testing.T) {
	t.Run("replace stage lookup succeeds", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)
		stage, ok, err := reg.Lookup("replace:DEBUG::")
		if !ok {
			t.Fatal("Lookup(replace:DEBUG::) reported not-registered")
		}
		if err != nil {
			t.Fatalf("Lookup returned error: %v", err)
		}
		if stage == nil {
			t.Fatal("Lookup returned nil stage")
		}
	})

	t.Run("replace with missing separator returns error", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)
		_, ok, err := reg.Lookup("replace:DEBUG")
		if !ok {
			t.Error("Lookup(replace:DEBUG) reported not-registered; want recognised-but-bad-arg")
		}
		if err == nil {
			t.Error("Lookup(replace:DEBUG) returned nil error; want parse error")
		}
	})

	t.Run("replace is in BuiltinStageNames", func(t *testing.T) {
		names := BuiltinStageNames()
		found := false
		for _, n := range names {
			if n == "replace" {
				found = true
				break
			}
		}
		if !found {
			t.Error("replace not found in BuiltinStageNames()")
		}
	})
}
