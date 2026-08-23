package db

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
)

// TestPrices_WithHTTPClient covers the WithHTTPClient acceptance
// contract: a custom *http.Client injected via WithHTTPClient is the
// client Refresh uses (not http.DefaultClient), the Refreshed cache
// reflects the response body the injected client saw, and the
// hard-fail contract (non-2xx status returns a non-nil error and leaves
// the in-memory cache unchanged) is preserved.
func TestPrices_WithHTTPClient(t *testing.T) {
	t.Run("injected client is used instead of DefaultClient", func(t *testing.T) {
		// Start a server that returns a models.dev-shaped JSON
		// payload containing exactly one (provider, model) entry.
		// We count requests on the server side: if the injected
		// client is honoured, the counter increments; if the
		// injected client is bypassed, the counter stays at zero
		// (DefaultClient hits models.dev instead, which we are not
		// stubbing).
		var calls int32
		body := `{"anthropic":{"models":{"claude-test":{"cost":{"input":1.0,"output":2.0}}}}}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)

		// Build a client whose Transport rewrites any request URL to
		// the test server. The refresh code targets ModelsDevURL
		// (https://models.dev/api.json) — we cannot change that, but
		// we can intercept the request at the Transport layer and
		// forward it to the httptest server. This is the canonical
		// pattern for testing code that hardcodes an upstream URL.
		client := &http.Client{Transport: redirectTransport{target: srv.URL}}

		p := NewPrices("").WithHTTPClient(client)
		if err := p.Refresh(); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Fatalf("injected client call count = %d, want 1 (Refresh bypassed WithHTTPClient)", got)
		}

		// And the parsed cache reflects the injected server's body —
		// not whatever DefaultClient would have returned. This
		// proves the response was read via the injected transport.
		in, out, ok := p.Lookup("anthropic", "claude-test")
		if !ok {
			t.Fatal("Lookup miss after Refresh against injected server")
		}
		if in != 1.0 || out != 2.0 {
			t.Fatalf("Lookup returned in=%v out=%v, want 1.0/2.0", in, out)
		}
	})

	t.Run("NewPrices defaults to http.DefaultClient", func(t *testing.T) {
		// Production paths must still use http.DefaultClient when
		// no WithHTTPClient call is made. The seam is opt-in:
		// NewPrices leaves httpClient == http.DefaultClient, and a
		// bare Refresh call (had we run it) would go through the
		// global client. We assert the default via the unexported
		// field — accessible because the test is in the same package.
		p := NewPrices("")
		if p.httpClient == nil {
			t.Fatal("NewPrices did not initialise httpClient")
		}
		if p.httpClient != http.DefaultClient {
			t.Fatalf("NewPrices httpClient = %v, want http.DefaultClient", p.httpClient)
		}
	})

	t.Run("hard-fail preserved: non-2xx returns error", func(t *testing.T) {
		// The manual-refresh contract: a non-2xx response is a hard
		// error. WithHTTPClient must not weaken that contract.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		client := &http.Client{Transport: redirectTransport{target: srv.URL}}
		p := NewPrices("").WithHTTPClient(client)
		err := p.Refresh()
		if err == nil {
			t.Fatal("Refresh returned nil error on 500 response")
		}
		if !strings.Contains(err.Error(), "status 500") {
			t.Fatalf("error %q does not mention status 500", err.Error())
		}
	})

	t.Run("hard-fail preserved: cache unchanged on error", func(t *testing.T) {
		// Seed the cache with a known (provider, model) entry, then
		// call Refresh against a 502-returning injected client. The
		// contract is: a failed Refresh leaves the in-memory cache
		// unchanged so the caller can retry or surface the stored
		// values. The seeded entry must still be Lookup-able after
		// the failed Refresh.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		t.Cleanup(srv.Close)

		client := &http.Client{Transport: redirectTransport{target: srv.URL}}

		p := NewPrices("").WithHTTPClient(client)
		// Seed under the mutex; Lookup uses the read-lock path.
		// We seed BEFORE the failed Refresh so the assertion is
		// "failed Refresh leaves cache unchanged", not "seed
		// survives a no-op".
		p.mu.Lock()
		p.raw = map[string]map[string]Price{
			"anthropic": {"claude-seed": {Input: 0.5, Output: 1.5}},
		}
		p.mu.Unlock()

		_ = p.Refresh() // expected to fail with 502

		in, out, ok := p.Lookup("anthropic", "claude-seed")
		if !ok {
			t.Fatal("seeded entry lost after failed Refresh")
		}
		if in != 0.5 || out != 1.5 {
			t.Fatalf("seeded entry mutated: in=%v out=%v, want 0.5/1.5", in, out)
		}
	})
}

func TestPricesParsesCacheRatesContextTiersAndLegacyFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	legacy := `{"openai":{"legacy":{"input":2.5,"output":15}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPrices(path)
	if err := p.Load(); err != nil {
		t.Fatal(err)
	}
	record, ok := p.LookupRecord("openai", "legacy")
	if !ok || record.Input != 2.5 || record.Output != 15 || record.CacheRead != nil || record.CacheWrite != nil {
		t.Fatalf("legacy record = %#v, ok=%v", record, ok)
	}

	body := `{"openai":{"models":{"gpt":{"cost":{"input":2.5,"output":15,"cache_read":0.25,"cache_write":3.125,"tiers":[{"tier":{"type":"context","size":272000},"input":5,"output":22.5,"cache_read":0.5,"cache_write":6.25}],"context_over_200k":{"input":4,"output":20,"cache_read":0.4,"cache_write":5}}}}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	p = NewPrices(path).WithHTTPClient(&http.Client{Transport: redirectTransport{target: server.URL}})
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	record, ok = p.LookupRecord("openai", "gpt")
	if !ok || record.CacheRead == nil || *record.CacheRead != .25 || record.CacheWrite == nil || *record.CacheWrite != 3.125 {
		t.Fatalf("cache-aware record = %#v, ok=%v", record, ok)
	}
	if len(record.Tiers) != 1 || record.Tiers[0].Tier.Type != "context" || record.Tiers[0].Tier.Size != 272000 {
		t.Fatalf("tiers = %#v", record.Tiers)
	}
	if record.ContextOver200K == nil || record.ContextOver200K.Input != 4 {
		t.Fatalf("context_over_200k = %#v", record.ContextOver200K)
	}
}

func TestPriceSelectRatesAtContextThreshold(t *testing.T) {
	read, write := .25, 3.125
	record := Price{Input: 2.5, Output: 15, CacheRead: &read, CacheWrite: &write, Tiers: []PriceTier{{Tier: PriceTierSelector{Type: "context", Size: 272000}, Input: 5, Output: 22.5}}}
	if got := record.SelectRates(271999); got.Input != 2.5 {
		t.Fatalf("below threshold input = %v", got.Input)
	}
	if got := record.SelectRates(272000); got.Input != 5 {
		t.Fatalf("at threshold input = %v", got.Input)
	}
}

func TestPricesActualSchemaReasoningRatesAndZero(t *testing.T) {
	body := `{"openai":{"models":{"gpt":{"cost":{"input":2,"output":10,"reasoning":3,"cache_read":0,"cache_write":0,"tiers":[{"tier":{"type":"context","size":272000},"input":4,"output":20,"reasoning":7,"cache_read":0,"cache_write":0}]}},"free":{"cost":{"input":0,"output":0,"reasoning":0,"cache_read":0,"cache_write":0}}}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	p := NewPrices(filepath.Join(t.TempDir(), "prices.json")).WithHTTPClient(&http.Client{Transport: redirectTransport{target: server.URL}})
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	record, ok := p.LookupRecord("openai", "gpt")
	if !ok || record.Reasoning == nil || *record.Reasoning != 3 || record.CacheRead == nil || *record.CacheRead != 0 {
		t.Fatalf("base record = %#v, ok=%v", record, ok)
	}
	if len(record.Tiers) != 1 || record.Tiers[0].Reasoning == nil || *record.Tiers[0].Reasoning != 7 || record.Tiers[0].CacheWrite == nil || *record.Tiers[0].CacheWrite != 0 {
		t.Fatalf("tier record = %#v", record.Tiers)
	}
	free, ok := p.LookupRecord("openai", "free")
	if !ok || !free.InputSet || !free.OutputSet || free.Reasoning == nil || *free.Reasoning != 0 || free.CacheRead == nil || *free.CacheRead != 0 {
		t.Fatalf("zero-rate record = %#v, ok=%v", free, ok)
	}
}

// redirectTransport is an http.RoundTripper that rewrites the request URL
// to the target httptest server URL before forwarding. This is the
// canonical pattern for testing code that hardcodes an upstream URL (in
// our case, models.dev) without standing up an actual mock DNS or
// TLS-mitm'd mirror.
//
// Path/query is preserved from the original request so the test server
// receives the same intent the production client would express; only
// scheme + host are rewritten.
type redirectTransport struct {
	target string
}

func (t redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Parse the target base so we can rebuild the URL with the
	// original path + query intact.
	base, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	// Clone the request so we do not mutate the caller's state.
	clone := req.Clone(req.Context())
	clone.URL.Scheme = base.Scheme
	clone.URL.Host = base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// TestPrices_LoadMalformedKeepsWarmCache covers AC 5: a Prices cache
// loaded from a malformed JSON file must report Cold() == false (warm
// cache semantic preserved) and the Load call must return a non-nil
// parse error. The warm-cache semantic is the load-bearing detail:
//
//   - The on-disk file exists, so the cache is "warm" from the
//     user's perspective — they have a price file, just a broken one.
//   - A cold cache would silently trigger a refetch from
//     PersistToolCall on every lookup miss; that would mask the
//     user's corruption behind a "first call" symptom.
//   - Warm means the write path will NOT refetch; cost derivation
//     stays NULL on lookup miss, and the user is forced to run
//     `ktx prices update` to recover (the only path that can write
//     the cache).
//
// The companion subtest ("absent file → cold=true") locks in the
// opposite direction: a missing file must report cold=true so the
// write path knows it can refetch on first miss.
func TestPrices_LoadMalformedKeepsWarmCache(t *testing.T) {
	t.Run("malformed JSON file → Cold() == false and Load returns parse error", func(t *testing.T) {
		testutil.WithTempXDG(t)

		// Resolve the cache path under XDG_DATA_HOME so the test
		// does not touch ~/.local/share/ktx/prices.json on a
		// developer machine.
		path, err := PricesPath()
		if err != nil {
			t.Fatalf("PricesPath: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll %q: %v", filepath.Dir(path), err)
		}
		// Write a syntactically broken JSON file: an unterminated
		// object. json.Unmarshal will return a parse error; the
		// Load function must keep cold=false so the write path
		// treats this as a warm cache the user must repair via
		// `ktx prices update`.
		if err := os.WriteFile(path, []byte(`{"anthropic":{"models":`), 0o644); err != nil {
			t.Fatalf("seed malformed cache file: %v", err)
		}

		p := NewPrices(path)
		err = p.Load()
		if err == nil {
			t.Fatal("Load returned nil error on malformed file")
		}
		if !strings.Contains(err.Error(), "parse") {
			t.Errorf("Load error %q does not mention parse failure", err.Error())
		}
		if p.Cold() {
			t.Error("Cold() = true after Load of malformed (existing) file; want false (warm cache semantic)")
		}

		// A lookup against the empty in-memory map returns ok=false.
		// The write path will treat this as "warm + miss → no
		// refetch → cost stays NULL" per the soft-fail contract.
		if _, _, ok := p.Lookup("anthropic", "anything"); ok {
			t.Error("Lookup returned ok=true against an empty in-memory map")
		}
	})

	t.Run("absent file → Cold() == true and Load returns nil", func(t *testing.T) {
		// Companion: a missing file is the cold case (first
		// invocation, no cache yet). The write path uses Cold() to
		// decide whether to refetch on lookup miss; a missing file
		// must be cold so a fresh ktx install can self-populate.
		testutil.WithTempXDG(t)

		path, err := PricesPath()
		if err != nil {
			t.Fatalf("PricesPath: %v", err)
		}
		// Pre-state: the file must not exist. WithTempXDG created
		// XDG_DATA_HOME=<tmp>/share but not <tmp>/share/ktx.
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cache file %q exists before Load; want absent", path)
		}

		p := NewPrices(path)
		if err := p.Load(); err != nil {
			t.Fatalf("Load(absent file): %v", err)
		}
		if !p.Cold() {
			t.Error("Cold() = false after Load of absent file; want true (cold cache)")
		}
	})

	t.Run("valid JSON file → Cold() == false and entries are lookup-able", func(t *testing.T) {
		// Companion: a valid file is the warm + populated case.
		// Cold() == false and a known (provider, model) entry
		// resolves through Lookup. This is the steady-state for
		// any ktx install that has run `ktx prices update` at
		// least once.
		testutil.WithTempXDG(t)

		path, err := PricesPath()
		if err != nil {
			t.Fatalf("PricesPath: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		// The on-disk format is the post-Refresh projection:
		// map[provider]map[model]Price{Input, Output}. Different
		// from the wire format (which carries a "models" wrapper
		// and a "cost" struct); Refresh's parser strips those
		// wrappers before writing.
		body := `{"anthropic":{"claude-sonnet-4-20250514":{"input":3.0,"output":15.0}}}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("seed cache file: %v", err)
		}

		p := NewPrices(path)
		if err := p.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if p.Cold() {
			t.Error("Cold() = true after Load of valid existing file; want false")
		}
		in, out, ok := p.Lookup("anthropic", "claude-sonnet-4-20250514")
		if !ok {
			t.Fatal("Lookup miss on seeded (provider, model) entry")
		}
		if in != 3.0 || out != 15.0 {
			t.Errorf("Lookup returned in=%v out=%v; want 3.0/15.0", in, out)
		}
	})
}

// TestPrices_WriteToDisk is a small companion covering the
// atomic-write contract: after WriteToDisk, the on-disk file is
// parseable by a fresh Prices.Load, the Cold flag flips to false,
// and Size reports the entry counts. This is the round-trip side of
// the load-must-survive-disk-write contract the manual-refresh path
// relies on.
func TestPrices_WriteToDisk(t *testing.T) {
	t.Run("WriteToDisk persists cache and flips cold to false", func(t *testing.T) {
		testutil.WithTempXDG(t)

		path, err := PricesPath()
		if err != nil {
			t.Fatalf("PricesPath: %v", err)
		}

		p := NewPrices(path)
		if err := p.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !p.Cold() {
			t.Fatal("Cold() = false after Load of absent file; want true")
		}

		// Seed one entry directly so WriteToDisk has something to
		// persist. Bypasses the Refresh path because the contract
		// under test is the disk-write step.
		p.mu.Lock()
		p.raw = map[string]map[string]Price{
			"anthropic": {"claude-test": {Input: 1.5, Output: 2.5}},
		}
		p.mu.Unlock()

		if err := p.WriteToDisk(); err != nil {
			t.Fatalf("WriteToDisk: %v", err)
		}
		if p.Cold() {
			t.Error("Cold() = true after WriteToDisk; want false (cache now has on-disk representation)")
		}

		// Re-Load from disk into a fresh Prices: must parse, must
		// be warm, must resolve the seeded entry.
		p2 := NewPrices(path)
		if err := p2.Load(); err != nil {
			t.Fatalf("second Load: %v", err)
		}
		if p2.Cold() {
			t.Error("Cold() = true after second Load of freshly written file; want false")
		}
		in, out, ok := p2.Lookup("anthropic", "claude-test")
		if !ok {
			t.Fatal("Lookup miss after WriteToDisk + reload")
		}
		if in != 1.5 || out != 2.5 {
			t.Errorf("Lookup returned in=%v out=%v; want 1.5/2.5", in, out)
		}
	})
}

func TestPricesConcurrentWritesPublishValidCache(t *testing.T) {
	testutil.WithTempXDG(t)
	path, err := PricesPath()
	if err != nil {
		t.Fatal(err)
	}
	p := NewPrices(path)
	p.raw = map[string]map[string]Price{"provider": {"model": {Input: 1, Output: 2}}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- p.WriteToDisk()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded := NewPrices(path)
	if err := loaded.Load(); err != nil {
		t.Fatalf("published cache is invalid: %v", err)
	}
	if _, _, ok := loaded.Lookup("provider", "model"); !ok {
		t.Fatal("published cache lost entry")
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".ktx-write-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files=%v err=%v", matches, err)
	}
}
