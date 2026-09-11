package db

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
)

type pricingFixtureTransport struct {
	calls  int
	status int
	body   string
	err    error
}

func (f *pricingFixtureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader(f.body)), Header: make(http.Header)}, nil
}

const discoveryPrices = `{"provider":{"models":{"model":{"cost":{"input":3,"output":15,"cache_read":0.3}}}}}`

func TestCostDeriverColdDiscovery(t *testing.T) {
	for _, requested := range []string{"model", "missing"} {
		t.Run(requested, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prices.json")
			fixture := &pricingFixtureTransport{status: http.StatusOK, body: discoveryPrices}
			load := func() *Prices {
				p := NewPrices(path).WithHTTPClient(&http.Client{Transport: fixture})
				if err := p.Load(); err != nil {
					t.Fatal(err)
				}
				return p
			}
			p := load()
			derive := func(p *Prices, model string) *float64 {
				provider, tokens := "provider", int64(1_000_000)
				return (&costDeriver{}).Derive(CostInputs{Provider: &provider, Model: &model, TokensIn: &tokens, TokensOut: &tokens}, p)
			}
			got := derive(p, requested)
			if requested == "model" && (got == nil || *got != 18) || requested == "missing" && got != nil {
				t.Fatalf("cold derivation = %v for %s", got, requested)
			}
			if p.Cold() {
				t.Fatal("successful discovery left cache cold")
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			original, _ := p.LookupRecord("provider", "model")
			// A new Prices has no shared in-memory state with the first lookup.
			fresh := load()
			reloaded, ok := fresh.LookupRecord("provider", "model")
			if !ok || !reflect.DeepEqual(original, reloaded) || reloaded.SourceURL != ModelsDevURL || reloaded.RetrievedAt == "" {
				t.Fatalf("pricing record/provenance did not round-trip: %+v", reloaded)
			}
			for _, cache := range []*Prices{p, fresh} {
				if got := derive(cache, "model"); got == nil || *got != 18 {
					t.Fatalf("warm cost = %v, want 18", got)
				}
				if got := derive(cache, "missing"); got != nil {
					t.Fatalf("missing price became known: %v", *got)
				}
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || fixture.calls != 1 {
				t.Fatalf("warm lookup changed cache or refetched: calls=%d err=%v", fixture.calls, err)
			}
		})
	}
}

func TestCostDeriverColdDiscoveryFailure(t *testing.T) {
	for _, failure := range []string{"timeout", "status", "json", "mkdir", "rename"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "cache", "prices.json")
			fixture := &pricingFixtureTransport{status: http.StatusOK, body: discoveryPrices}
			p := NewPrices(path).WithHTTPClient(&http.Client{Transport: fixture})
			if err := p.Load(); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "timeout":
				fixture.err = context.DeadlineExceeded
			case "status":
				fixture.status = http.StatusBadGateway
			case "json":
				fixture.body = `{`
			case "mkdir":
				if err := os.WriteFile(filepath.Dir(path), []byte("blocker"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "rename":
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			provider, model, tokens := "provider", "model", int64(1_000_000)
			inputs := CostInputs{Provider: &provider, Model: &model, TokensIn: &tokens, TokensOut: &tokens}
			if got := (&costDeriver{}).Derive(inputs, p); got != nil {
				t.Fatalf("failed discovery returned cost %v, want unknown", *got)
			}
			if !p.Cold() || fixture.calls != 1 {
				t.Fatalf("failed discovery: cold=%v calls=%d", p.Cold(), fixture.calls)
			}
			publicationFailure := failure == "mkdir" || failure == "rename"
			if _, ok := p.LookupRecord(provider, model); ok != publicationFailure {
				t.Fatalf("in-memory fetched record retained=%v, want %v", ok, publicationFailure)
			}
			if publicationFailure {
				blocker := path
				if failure == "mkdir" {
					blocker = filepath.Dir(path)
				}
				if _, err := os.Stat(blocker); err != nil {
					t.Fatalf("publication destroyed blocker: %v", err)
				}
				if err := os.Remove(blocker); err != nil {
					t.Fatal(err)
				}
				// Retained valid fetched data can be published without another fetch.
				if err := p.WriteToDisk(); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failed fetch published a cache: %v", err)
				}
				fixture.err, fixture.status, fixture.body = nil, http.StatusOK, discoveryPrices
				if got := (&costDeriver{}).Derive(inputs, p); got == nil || *got != 18 {
					t.Fatalf("retry cost = %v, want 18", got)
				}
			}
			fresh := NewPrices(path).WithHTTPClient(&http.Client{Transport: fixture})
			if err := fresh.Load(); err != nil || fresh.Cold() {
				t.Fatalf("recovered cache: cold=%v err=%v", fresh.Cold(), err)
			}
			if got := (&costDeriver{}).Derive(inputs, fresh); got == nil || *got != 18 {
				t.Fatalf("recovered cost = %v, want 18", got)
			}
			inputs.Model = new(string)
			calls := fixture.calls
			if got := (&costDeriver{}).Derive(inputs, fresh); got != nil || fixture.calls != calls {
				t.Fatalf("recovered warm miss: cost=%v calls=%d", got, fixture.calls)
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".ktx-write-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("temporary files=%v err=%v", matches, err)
			}
		})
	}
}

func TestPersistToolCallColdDiscoveryReload(t *testing.T) {
	testutil.WithTempXDG(t)
	resetPricesForTest()
	t.Cleanup(resetPricesForTest)
	fixture := &pricingFixtureTransport{status: http.StatusOK, body: discoveryPrices}
	load := func() {
		p, err := LoadPrices()
		if err != nil {
			t.Fatal(err)
		}
		p.WithHTTPClient(&http.Client{Transport: fixture})
	}
	load()
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	session := mustNewID(t)
	if err := d.InsertSession(&Session{ID: session, Agent: "test", StartedAt: "t", LastSeenAt: "t"}); err != nil {
		t.Fatal(err)
	}
	for i, model := range []string{"model", "model", "missing"} {
		if i == 1 {
			// Discard the entire process cache, including its injected client.
			resetPricesForTest()
			load()
			fixture.err = context.DeadlineExceeded
		}
		provider, tokens := "provider", int64(1_000_000)
		tc := &ToolCall{ID: mustNewID(t), SessionID: session, Tool: "test", Timestamp: "t", Model: &model, Provider: &provider, TokensIn: &tokens, TokensOut: &tokens}
		if err := d.PersistToolCall(tc); err != nil {
			t.Fatal(err)
		}
		stored, err := d.GetToolCall(tc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if model == "model" && (stored.Cost == nil || *stored.Cost != 18) || model == "missing" && stored.Cost != nil {
			t.Fatalf("persisted %s cost = %v", model, stored.Cost)
		}
	}
	if fixture.calls != 1 {
		t.Fatalf("fetch count across process-cache reload = %d, want 1", fixture.calls)
	}
}
