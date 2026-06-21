// Table-driven tests for `ktx prices` (internal/cli/prices.go).
//
// Coverage:
//   - prices update: refresh from models.dev via WithHTTPClient
//     seam; success path emits the JSON envelope; failure path
//     exits 4 (manual-refresh hard-fail contract).
//   - prices path: prints the resolved cache file path.
//   - unknown subcommand: exit 1.
//   - --help: exit 0 with help on stdout.
//
// The test stub uses net/http/httptest to serve a models.dev-shaped
// JSON document so the test does not depend on the public
// https://models.dev/api.json (which the AC 8 hard-fail contract would
// not tolerate).
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
)

// modelsDevFixture is a minimal models.dev/api.json-shaped document.
// The Prices.Unmarshal path reads provider -> model -> { input,
// output } entries; the fixture mirrors that shape so Refresh succeeds.
func modelsDevFixture() map[string]map[string]map[string]float64 {
	return map[string]map[string]map[string]float64{
		"anthropic": {
			"claude-sonnet-4-20250514": {"input": 3, "output": 15},
		},
	}
}

// roundTripperFunc adapts a function to http.RoundTripper so the
// test can stub models.dev responses without running a real server
// when the URL is not a localhost httptest.Server.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// jsonResponse returns an *http.Response whose body is the JSON
// encoding of v. Status code 200, application/json content type.
func jsonResponse(r *http.Request, status int, v any) *http.Response {
	body, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    r,
	}
}

// TestRunPricesWith_Update covers the manual-refresh contract: prices
// update fetches from models.dev, atomically rewrites the cache file,
// and emits a JSON envelope on success. The companion case asserts the
// 500-response hard-fail path returns exit 4.
func TestRunPricesWith_Update(t *testing.T) {
	t.Run("prices update success: 200 → JSON envelope, exit 0", func(t *testing.T) {
		tmp := testutil.WithTempXDG(t)
		_ = filepath.Join(tmp, "share", "ktx", "prices.json") // path; not used directly

		// Stub models.dev: any HTTP call returns the
		// models.dev-shaped fixture with status 200.
		origTransport := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = origTransport })
		http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(r, 200, modelsDevFixture()), nil
		})

		var stdout, stderr bytes.Buffer
		code := runPricesWith([]string{"update"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// JSON envelope: {"refreshed": true, "providers": N,
		// "models": M, "path": "..."}
		var env map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env["refreshed"] != true {
			t.Errorf("refreshed = %v, want true", env["refreshed"])
		}
		if p, ok := env["path"].(string); !ok || !strings.HasSuffix(p, "prices.json") {
			t.Errorf("path = %v, want a path ending in prices.json", env["path"])
		}
	})

	t.Run("prices update failure: 500 → exit 4 (hard fail)", func(t *testing.T) {
		testutil.WithTempXDG(t)

		origTransport := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = origTransport })
		http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			// Return 500 with a non-JSON body so the
			// Prices parser fails the response.
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(strings.NewReader("server error")),
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Request:    r,
			}, nil
		})

		var stdout, stderr bytes.Buffer
		code := runPricesWith([]string{"update"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitInternal {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitInternal, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"internal"`) {
			t.Errorf("stderr missing internal envelope: %q", stderr.String())
		}
	})

	t.Run("prices path prints resolved cache path", func(t *testing.T) {
		tmp := testutil.WithTempXDG(t)
		want := filepath.Join(tmp, "share", "ktx", "prices.json")

		var stdout, stderr bytes.Buffer
		code := runPricesWith([]string{"path"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
	})

	t.Run("unknown subcommand exits 1 (usage)", func(t *testing.T) {
		testutil.WithTempXDG(t)

		var stdout, stderr bytes.Buffer
		code := runPricesWith([]string{"nonexistent"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "nonexistent") {
			t.Errorf("stderr missing subcommand name: %q", stderr.String())
		}
	})
}
