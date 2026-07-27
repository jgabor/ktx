package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/binarycap"
)

const testBinaryDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestBinaryCapabilitiesJSONAndComparison(t *testing.T) {
	original := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{
			Schema: binarycap.Schema, ExecutablePath: "/canonical/ktx",
			ExecutableSHA256: testBinaryDigest, BuildVersion: "test-version", MaxDatabaseSchema: 8,
		}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = original })

	reg, ok := dispatchRegistry["binary-capabilities"]
	if !ok {
		t.Fatal("binary-capabilities is not registered")
	}
	if reg.opts.NeedsDB {
		t.Fatal("binary-capabilities must not resolve or open a database")
	}

	for _, args := range [][]string{{"binary-capabilities"}, {"binary-capabilities", "--expect-sha256", testBinaryDigest}} {
		var stdout, stderr bytes.Buffer
		if code := runWith(args, &stdout, &stderr); code != ExitOK {
			t.Fatalf("runWith(%v) code=%d stderr=%q", args, code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("runWith(%v) stderr=%q", args, stderr.String())
		}
		var got map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["schema"] != binarycap.Schema || got["executable_path"] != "/canonical/ktx" ||
			got["executable_sha256"] != testBinaryDigest || got["build_version"] != "test-version" ||
			got["max_database_schema"] != float64(8) {
			t.Fatalf("response=%#v", got)
		}
		if len(args) > 1 {
			comparison := got["comparison"].(map[string]any)
			if comparison["status"] != "equal" || comparison["expected_sha256"] != testBinaryDigest {
				t.Fatalf("comparison=%#v", comparison)
			}
		}
	}
}

func TestBinaryCapabilitiesMismatchAndUnverifiableErrors(t *testing.T) {
	original := currentBinaryCapabilities
	t.Cleanup(func() { currentBinaryCapabilities = original })

	t.Run("mismatch", func(t *testing.T) {
		currentBinaryCapabilities = func() (binarycap.Identity, error) {
			return binarycap.Identity{Schema: binarycap.Schema, ExecutablePath: "/canonical/ktx", ExecutableSHA256: testBinaryDigest, BuildVersion: "test-version", MaxDatabaseSchema: 8}, nil
		}
		expected := strings.Repeat("a", 64)
		var stdout, stderr bytes.Buffer
		if code := runWith([]string{"binary-capabilities", "--expect-sha256", expected}, &stdout, &stderr); code != ExitConflict {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout=%q", stdout.String())
		}
		for _, want := range []string{`"code":"conflict"`, `"expected_sha256":"` + expected, `"actual_sha256":"` + testBinaryDigest, `"executable_path":"/canonical/ktx"`, "install or select the verified executable"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr missing %q: %s", want, stderr.String())
			}
		}
	})

	t.Run("invalid expected digest", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runWith([]string{"binary-capabilities", "--expect-sha256", "ABC"}, &stdout, &stderr); code != ExitUsage {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "64 lowercase hexadecimal") {
			t.Fatalf("stderr=%q", stderr.String())
		}
	})

	t.Run("unverifiable", func(t *testing.T) {
		currentBinaryCapabilities = func() (binarycap.Identity, error) { return binarycap.Identity{}, errors.New("fixture unreadable") }
		var stdout, stderr bytes.Buffer
		if code := runWith([]string{"binary-capabilities"}, &stdout, &stderr); code != ExitInternal {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "reinstall the executable at a readable stable path") {
			t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})
}

func TestBinaryCapabilitiesTextAndHelp(t *testing.T) {
	original := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{Schema: binarycap.Schema, ExecutablePath: "/canonical/ktx", ExecutableSHA256: testBinaryDigest, BuildVersion: "test-version", MaxDatabaseSchema: 8}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = original })

	var stdout, stderr bytes.Buffer
	if code := runWith([]string{"binary-capabilities", "--format", "text"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{binarycap.Schema, "/canonical/ktx", testBinaryDigest, "test-version", "8"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("text output missing %q: %s", want, stdout.String())
		}
	}

	stdout.Reset()
	if code := runWith([]string{"binary-capabilities", "--help"}, &stdout, &stderr); code != ExitOK || !strings.Contains(stdout.String(), "--expect-sha256") {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
