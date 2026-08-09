// Price cache for the cost-measurement mechanics.
//
// The cache lives at $XDG_DATA_HOME/ktx/prices.json and is the local source
// of truth for per-token pricing across runs. ktx fetches from
// https://models.dev/api.json on cache miss and caches the parsed result
// in memory; the on-disk file is the long-lived source of truth between
// fetches. Fetch failures are soft: the write path surfaces stored cost
// when the cache cannot supply a price, with no error returned (the
// soft-fail contract).
//
// File format (JSON, two-level map keyed by (provider, model)):
//
//	{
//	  "<provider>": {
//	    "<model>": {"input": <float>, "output": <float>,
//	                 "cache_read": <float>, "cache_write": <float>,
//	                 "tiers": [...]}
//	  }
//	}
//
// Rates are USD per million tokens and use models.dev's singular cost field
// names. Legacy input/output-only records remain valid. The format is a faithful
// projection of models.dev's per-model pricing — a third party can read
// the file without ktx and reconstruct the per-call cost the same way
// ktx does (the reconstructability contract).
//
// Lifecycle:
//
//   - Cold cache (file does not exist at Load time): the first derivation
//     attempt triggers a fetch from models.dev and an atomic write of the
//     cache file. Parent directory created with 0o755 if missing.
//   - Warm cache (file exists at Load time): the in-memory copy is the
//     only source consulted. No refetch from the write path; cost stays
//     NULL on lookup miss. Mtime preserved (idempotent write path).
//   - Manual refresh (`ktx prices update`): forces a refetch regardless
//     of cache state and rewrites the cache atomically. A fetch failure
//     here is a hard error (exit code 4) because the user explicitly
//     requested the refresh.
//
// The cache fetches on first use and refreshes only when requested.
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// pricesFilePerm is the file mode for the price cache. World-readable
// because the file contains only public price information; group/other
// bits set to read-only. No executable bit.
const pricesFilePerm = 0o644

// pricesDirPerm is the directory mode for the cache directory. Standard
// XDG_DATA_HOME subtree mode.
const pricesDirPerm = 0o755

// PricesPath resolves the default price cache path:
// $XDG_DATA_HOME/ktx/prices.json. Falls back to ~/.local/share/ktx/prices.json
// when XDG_DATA_HOME is unset (XDG Base Directory spec — same fallback
// as DefaultPath — see the package's primary file for the default location.
func PricesPath() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "ktx", "prices.json"), nil
}

// ModelsDevURL is the canonical models.dev JSON endpoint. The
// cost contract requires a fetch from this URL; the
// conventional endpoint is /api.json (confirmed by HTTP/2 200 +
// content-type: application/json from `curl -I https://models.dev/api.json`).
const ModelsDevURL = "https://models.dev/api.json"

// modelsDevTimeout bounds the network fetch so a hung connection does
// not stall a write path. Five seconds is well above the typical
// models.dev response time and well below the user's patience for a
// tool call persistence. The cache file is the source of truth between
// fetches; a slow fetch only blocks the current derivation, not the
// whole tool.
const modelsDevTimeout = 5 * time.Second

// modelsDevMaxBody caps the JSON response size to defend against a
// misbehaving or malicious server. The live endpoint is ~3 MB; 64 MB
// provides headroom for schema growth without unbounded memory.
const modelsDevMaxBody = 64 * 1024 * 1024

// Price is the per-(provider, model) price record persisted to disk.
// USD per million tokens for ordinary input/output, reasoning, and prompt-cache traffic. The JSON
// tags are the canonical field names so a third party reading the
// cache file directly sees the same shape ktx uses internally.
type PriceRates struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	Reasoning  *float64 `json:"reasoning,omitempty"`
	InputSet   bool     `json:"-"`
	OutputSet  bool     `json:"-"`
}

func (r PriceRates) hasOrdinaryPrices() bool {
	return (r.InputSet || r.Input != 0) && (r.OutputSet || r.Output != 0)
}

type PriceTierSelector struct {
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type PriceTier struct {
	Tier       PriceTierSelector `json:"tier"`
	Input      float64           `json:"input"`
	Output     float64           `json:"output"`
	CacheRead  *float64          `json:"cache_read,omitempty"`
	CacheWrite *float64          `json:"cache_write,omitempty"`
	Reasoning  *float64          `json:"reasoning,omitempty"`
	InputSet   bool              `json:"-"`
	OutputSet  bool              `json:"-"`
}

type Price struct {
	Input           float64     `json:"input"`
	Output          float64     `json:"output"`
	CacheRead       *float64    `json:"cache_read,omitempty"`
	CacheWrite      *float64    `json:"cache_write,omitempty"`
	Reasoning       *float64    `json:"reasoning,omitempty"`
	InputSet        bool        `json:"-"`
	OutputSet       bool        `json:"-"`
	Tiers           []PriceTier `json:"tiers,omitempty"`
	ContextOver200K *PriceRates `json:"context_over_200k,omitempty"`
	SourceURL       string      `json:"source_url,omitempty"`
	RetrievedAt     string      `json:"retrieved_at,omitempty"`
}

func (p *Price) UnmarshalJSON(data []byte) error {
	var wire struct {
		Input           *float64    `json:"input"`
		Output          *float64    `json:"output"`
		CacheRead       *float64    `json:"cache_read"`
		CacheWrite      *float64    `json:"cache_write"`
		Reasoning       *float64    `json:"reasoning"`
		Tiers           []PriceTier `json:"tiers"`
		ContextOver200K *PriceRates `json:"context_over_200k"`
		SourceURL       string      `json:"source_url"`
		RetrievedAt     string      `json:"retrieved_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Input != nil {
		p.Input, p.InputSet = *wire.Input, true
	}
	if wire.Output != nil {
		p.Output, p.OutputSet = *wire.Output, true
	}
	p.CacheRead, p.CacheWrite, p.Reasoning, p.Tiers, p.ContextOver200K = wire.CacheRead, wire.CacheWrite, wire.Reasoning, wire.Tiers, wire.ContextOver200K
	p.SourceURL, p.RetrievedAt = wire.SourceURL, wire.RetrievedAt
	return nil
}

func (p Price) MarshalJSON() ([]byte, error) {
	var input, output *float64
	if p.InputSet || p.Input != 0 {
		value := p.Input
		input = &value
	}
	if p.OutputSet || p.Output != 0 {
		value := p.Output
		output = &value
	}
	return json.Marshal(struct {
		Input           *float64    `json:"input,omitempty"`
		Output          *float64    `json:"output,omitempty"`
		CacheRead       *float64    `json:"cache_read,omitempty"`
		CacheWrite      *float64    `json:"cache_write,omitempty"`
		Reasoning       *float64    `json:"reasoning,omitempty"`
		Tiers           []PriceTier `json:"tiers,omitempty"`
		ContextOver200K *PriceRates `json:"context_over_200k,omitempty"`
		SourceURL       string      `json:"source_url,omitempty"`
		RetrievedAt     string      `json:"retrieved_at,omitempty"`
	}{input, output, p.CacheRead, p.CacheWrite, p.Reasoning, p.Tiers, p.ContextOver200K, p.SourceURL, p.RetrievedAt})
}

func (t *PriceTier) UnmarshalJSON(data []byte) error {
	var wire struct {
		Tier       PriceTierSelector `json:"tier"`
		Input      *float64          `json:"input"`
		Output     *float64          `json:"output"`
		CacheRead  *float64          `json:"cache_read"`
		CacheWrite *float64          `json:"cache_write"`
		Reasoning  *float64          `json:"reasoning"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	t.Tier = wire.Tier
	if wire.Input != nil {
		t.Input, t.InputSet = *wire.Input, true
	}
	if wire.Output != nil {
		t.Output, t.OutputSet = *wire.Output, true
	}
	t.CacheRead, t.CacheWrite, t.Reasoning = wire.CacheRead, wire.CacheWrite, wire.Reasoning
	return nil
}

func (t PriceTier) MarshalJSON() ([]byte, error) {
	var input, output *float64
	if t.InputSet || t.Input != 0 {
		value := t.Input
		input = &value
	}
	if t.OutputSet || t.Output != 0 {
		value := t.Output
		output = &value
	}
	return json.Marshal(struct {
		Tier       PriceTierSelector `json:"tier"`
		Input      *float64          `json:"input,omitempty"`
		Output     *float64          `json:"output,omitempty"`
		CacheRead  *float64          `json:"cache_read,omitempty"`
		CacheWrite *float64          `json:"cache_write,omitempty"`
		Reasoning  *float64          `json:"reasoning,omitempty"`
	}{t.Tier, input, output, t.CacheRead, t.CacheWrite, t.Reasoning})
}

// PriceRateSelection records which retained price record applies to a token
// count. The base, a supported context tier, and the legacy context-over-200K
// record are distinct provenance cases.
type PriceRateSelection struct {
	Rates             PriceRates
	Basis             string
	ContextTierSize   *int64
	ContextTokenCount int64
}

// SelectRatesWithProvenance applies the same supported tier selection as
// SelectRates and retains the selected record's provenance.
func (p Price) SelectRatesWithProvenance(contextTokens int64) PriceRateSelection {
	rates := PriceRates{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite, Reasoning: p.Reasoning, InputSet: p.InputSet, OutputSet: p.OutputSet}
	selectedTier := false
	var selectedSize int64
	for _, tier := range p.Tiers {
		if tier.Tier.Type == "context" && contextTokens >= tier.Tier.Size && (!selectedTier || tier.Tier.Size > selectedSize) {
			rates = PriceRates{Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite, Reasoning: tier.Reasoning, InputSet: tier.InputSet, OutputSet: tier.OutputSet}
			selectedTier = true
			selectedSize = tier.Tier.Size
		}
	}
	selection := PriceRateSelection{Rates: rates, Basis: "base", ContextTokenCount: contextTokens}
	if selectedTier {
		size := selectedSize
		selection.Basis, selection.ContextTierSize = "context_tier", &size
		return selection
	}
	if !selectedTier && contextTokens > 200000 && p.ContextOver200K != nil {
		selection.Rates, selection.Basis = *p.ContextOver200K, "legacy_context_over_200k"
	}
	return selection
}

// SelectRates applies only models.dev context tiers. A tier begins at its
// declared context size; unsupported tier types are ignored.
func (p Price) SelectRates(contextTokens int64) PriceRates {
	return p.SelectRatesWithProvenance(contextTokens).Rates
}

// Prices is the parsed price cache plus its on-disk path. The in-memory
// representation is map[provider]map[model]Price; Lookup hides the
// two-level navigation.
//
// Concurrency: Prices is safe for concurrent use by multiple goroutines
// within a single process. The mutex guards both the in-memory map and
// the on-disk write so a Refresh + WriteToDisk pair from one goroutine
// cannot interleave with a Lookup from another. Each ktx invocation is
// a single process, so the only realistic concurrency is from a
// parallel caller (e.g. the wrapper teeAndParse loop is single-goroutine
// by construction, but defensive locking keeps the type honest).
type Prices struct {
	mu   sync.RWMutex
	raw  map[string]map[string]Price
	path string
	// cold reports whether the file did not exist at Load time. When
	// cold is true, the first lookup miss triggers a fetch + disk
	// write. When cold is false (warm cache), lookup misses do
	// not refetch — the on-disk file is treated as the source
	// of truth.
	cold bool
	// httpClient is the client used by Refresh to fetch from
	// models.dev. The default is http.DefaultClient; tests inject
	// httptest.NewServer clients via WithHTTPClient so the
	// hard-fail manual-refresh contract is exercisable without a
	// live network or a manual refresh against production models.dev.
	httpClient *http.Client
}

// NewPrices returns a Prices cache bound to path. The cache is empty
// until Load is called. Callers that want the default path should use
// LoadPrices() which also resolves the path and loads from disk.
func NewPrices(path string) *Prices {
	return &Prices{path: path, raw: map[string]map[string]Price{}, httpClient: http.DefaultClient}
}

// WithHTTPClient returns a *Prices whose Refresh uses the supplied
// *http.Client instead of http.DefaultClient. The seam exists so tests
// can substitute an httptest.NewServer (or any other transport) and
// exercise the manual-refresh hard-fail contract without a live network
// call. The method mutates the receiver in place AND returns it so
// callers can chain or assign. The hard-fail contract (a non-2xx
// status, network error, or malformed JSON returns a non-nil error and
// leaves the in-memory cache unchanged) is preserved — only the client
// is swapped.
//
// Production callers do not use this method: NewPrices and LoadPrices
// default to http.DefaultClient. Tests inject a custom client before
// calling Refresh or RefreshAndWrite.
func (p *Prices) WithHTTPClient(c *http.Client) *Prices {
	p.mu.Lock()
	p.httpClient = c
	p.mu.Unlock()
	return p
}

// Load reads the cache file from disk into memory. A missing file
// returns an empty cache, cold=true, and a nil error — the "cold cache"
// case (first invocation triggers fetch + write). A malformed
// file returns the parse error and an empty cache; the caller can
// choose to refresh without ambiguity. Per the soft-fail contract, the
// write path (PersistToolCall) does NOT propagate parse errors; only
// `ktx prices update` surfaces them.
func (p *Prices) Load() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			p.raw = map[string]map[string]Price{}
			p.cold = true
			return nil
		}
		return fmt.Errorf("prices: read %q: %w", p.path, err)
	}
	p.cold = false
	var raw map[string]map[string]Price
	if err := json.Unmarshal(data, &raw); err != nil {
		// Reset to empty but keep cold=false so the warm-cache
		// semantic holds: a malformed existing file does not
		// trigger a refetch from the write path (the user's cache
		// is corrupt; they need `ktx prices update` to fix it).
		p.raw = map[string]map[string]Price{}
		return fmt.Errorf("prices: parse %q: %w", p.path, err)
	}
	if raw == nil {
		raw = map[string]map[string]Price{}
	}
	p.raw = raw
	return nil
}

// Lookup returns the legacy (input, output) USD-per-million-token view for
// (provider, model) and ok=true when present in the cache; (0, 0,
// false) otherwise. The provider/model match is exact: a missing
// provider, a missing model, or a missing cost record (rare in
// models.dev but possible — e.g. "anyapi" entries with no cost) all
// surface as ok=false so the caller can distinguish "price known"
// from "price unknown".
func (p *Prices) Lookup(provider, model string) (input, output float64, ok bool) {
	price, ok := p.LookupRecord(provider, model)
	if !ok {
		return 0, 0, false
	}
	return price.Input, price.Output, true
}

// LookupRecord returns the complete cache-aware model pricing record.
func (p *Prices) LookupRecord(provider, model string) (Price, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.raw == nil {
		return Price{}, false
	}
	byModel, ok := p.raw[provider]
	if !ok {
		return Price{}, false
	}
	price, ok := byModel[model]
	if !ok {
		return Price{}, false
	}
	return price, true
}

// Cold reports whether the cache was loaded from a non-existent file.
// PersistToolCall uses this to decide whether a lookup miss should
// trigger a fetch (cold → fetch + write) or be treated as "price
// unknown, no inference" (warm → no fetch, cost stays NULL).
func (p *Prices) Cold() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cold
}

// Path returns the on-disk cache path. Used by the manual refresh
// command's "wrote N entries to PATH" output and by tests.
func (p *Prices) Path() string {
	return p.path
}

// Size returns the number of (provider, model) entries in the cache.
// Used for diagnostics and the manual refresh output.
func (p *Prices) Size() (providers, models int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	providers = len(p.raw)
	for _, byModel := range p.raw {
		models += len(byModel)
	}
	return providers, models
}

// WriteToDisk atomically writes the in-memory cache to disk. The write
// is write-to-temp + fsync + rename so a partial cache file cannot be
// observed by a concurrent reader (atomic cache write). Parent
// directory is created with 0o755 permissions if missing.
//
// The format is the JSON shape documented at the top of this file:
// map[provider]map[model]{input, output}. json.MarshalIndent produces
// a human-readable file so the cache is debuggable without a JSON
// pretty-printer.
func (p *Prices) WriteToDisk() error {
	p.mu.RLock()
	data, err := json.MarshalIndent(p.raw, "", "  ")
	p.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("prices: marshal: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(p.path), pricesDirPerm); err != nil {
		return fmt.Errorf("prices: create directory for %q: %w", p.path, err)
	}

	tmpPath := p.path + ".tmp"
	// O_TRUNC ensures a smaller new payload does not leave trailing
	// bytes from a larger previous payload. os.Rename is atomic on
	// POSIX when source and destination are on the same filesystem
	// (the tmp file is in the same directory as the destination).
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, pricesFilePerm)
	if err != nil {
		return fmt.Errorf("prices: open temp %q: %w", tmpPath, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("prices: write temp %q: %w", tmpPath, err)
	}
	// fsync so the rename publishes a fully-committed file. Without
	// fsync a crash between rename and the kernel's async write-back
	// could leave a zero-length file at the destination path after
	// recovery. fsync is the standard "crash-safe atomic rename"
	// ingredient on Linux.
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("prices: sync temp %q: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("prices: close temp %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, p.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("prices: rename %q -> %q: %w", tmpPath, p.path, err)
	}

	// Flip cold=false: the cache now has an on-disk representation.
	// A subsequent lookup miss in the same process should not refetch.
	p.mu.Lock()
	p.cold = false
	p.mu.Unlock()
	return nil
}

// modelsDevModel is the per-model slice of the models.dev response
// shape that ktx reads. Only the cost field is retained; everything
// else (modalities, limits, knowledge cutoff, etc.) is discarded
// because the cache is a price table, not a model registry.
type modelsDevModel struct {
	Cost *Price `json:"cost"`
}

// modelsDevProvider is the per-provider slice of the response. ktx only
// reads the models map; the provider-level fields (env, npm, api, name,
// doc) are metadata that does not enter the price cache.
type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

// Refresh fetches ModelsDevURL and replaces the in-memory cache with
// the parsed result. The fetch has a 5-second timeout; a network
// error, non-2xx response, or malformed JSON returns a non-nil error
// and leaves the in-memory cache unchanged. The caller decides whether
// to persist the result with WriteToDisk.
//
// Used by the write path (PersistToolCall) on cold-cache lookup miss
// and by the manual refresh command. The write path's caller
// soft-fails on error (cost stays NULL); the manual refresh
// command's caller surfaces the error as a hard failure.
func (p *Prices) Refresh() error {
	ctx, cancel := context.WithTimeout(context.Background(), modelsDevTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ModelsDevURL, nil)
	if err != nil {
		return fmt.Errorf("prices: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// httpClient defaults to http.DefaultClient via NewPrices; tests inject
	// httptest-backed clients via WithHTTPClient to exercise the hard-fail
	// contract without a live network call. The seam preserves the existing
	// Refresh semantics (timeout, status check, body cap, parse error) —
	// only the client is swappable.
	client := p.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("prices: fetch %q: %w", ModelsDevURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("prices: fetch %q: status %d", ModelsDevURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelsDevMaxBody))
	if err != nil {
		return fmt.Errorf("prices: read body: %w", err)
	}
	var raw map[string]modelsDevProvider
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("prices: parse %q: %w", ModelsDevURL, err)
	}

	newRaw := make(map[string]map[string]Price, len(raw))
	retrievedAt := time.Now().UTC().Format(time.RFC3339)
	for provider, pdata := range raw {
		if len(pdata.Models) == 0 {
			continue
		}
		byModel := make(map[string]Price, len(pdata.Models))
		for model, mdata := range pdata.Models {
			if mdata.Cost == nil {
				continue
			}
			price := *mdata.Cost
			price.SourceURL = ModelsDevURL
			price.RetrievedAt = retrievedAt
			byModel[model] = price
		}
		if len(byModel) > 0 {
			newRaw[provider] = byModel
		}
	}

	p.mu.Lock()
	p.raw = newRaw
	p.mu.Unlock()
	return nil
}

// RefreshAndWrite fetches from models.dev and atomically writes the
// result to disk. A non-nil error leaves the on-disk cache unchanged.
// Used by `ktx prices update` — the manual refresh hook forces a
// refetch regardless of cache state.
func (p *Prices) RefreshAndWrite() error {
	if err := p.Refresh(); err != nil {
		return err
	}
	return p.WriteToDisk()
}

// processPrices is the singleton per-process cache. sync.Once guarantees
// a single Load per process; LoadPrices is the public entry point used
// by PersistToolCall and `ktx prices update`. The error from Load is
// stored for diagnostic visibility but is not propagated — soft-fail
// contract.
var (
	pricesOnce     sync.Once
	pricesInstance *Prices
	pricesLoadErr  error
)

// LoadPrices returns the process-wide Prices cache, loading it from
// the default path on the first call. Subsequent calls return the same
// cached *Prices without re-reading the file. A nil error and a non-nil
// *Prices is the success case; a non-nil *Prices with a non-nil error
// means "loaded but the file was malformed, cache is empty" — the
// caller can choose to refresh via the manual command.
//
// The function is safe to call concurrently. The returned *Prices is
// safe for concurrent use by multiple goroutines.
func LoadPrices() (*Prices, error) {
	pricesOnce.Do(func() {
		path, err := PricesPath()
		if err != nil {
			pricesLoadErr = err
			// Create an empty in-memory cache so callers can
			// still attempt a refresh; the path error is logged
			// via pricesLoadErr but not propagated.
			pricesInstance = NewPrices("")
			return
		}
		p := NewPrices(path)
		if err := p.Load(); err != nil {
			// Store the error for diagnostics but keep the
			// (empty) *Prices so the write path can still
			// attempt a refresh on cold cache. A malformed
			// cache file is treated as cold-soft by the write
			// path because p.cold reflects whether the file
			// existed at Load time; a malformed existing file
			// leaves cold=false, so the write path will not
			// refetch — the user must run `ktx prices update`.
			pricesLoadErr = err
		}
		pricesInstance = p
	})
	return pricesInstance, pricesLoadErr
}

// LookupPrice is a convenience wrapper that calls LoadPrices and
// performs the (provider, model) lookup. Returns (0, 0, false) when
// the cache is unavailable or the (provider, model) entry is missing.
// This is the soft-fail surface used by PersistToolCall: callers do
// not need to distinguish "cache unavailable" from "price unknown".
func LookupPrice(provider, model string) (input, output float64, ok bool) {
	p, err := LoadPrices()
	if err != nil || p == nil || p.path == "" {
		return 0, 0, false
	}
	return p.Lookup(provider, model)
}

// ResolvePrice returns the complete cached record, fetching only when the cache
// was cold. Warm misses never refetch implicitly.
func ResolvePrice(provider, model string) (Price, error) {
	p, err := LoadPrices()
	if err != nil || p == nil || p.path == "" {
		return Price{}, fmt.Errorf("pricing cache unavailable; run `ktx prices update`")
	}
	if record, ok := p.LookupRecord(provider, model); ok {
		return record, nil
	}
	if p.Cold() {
		if err := p.RefreshAndWrite(); err != nil {
			return Price{}, fmt.Errorf("resolve pricing for %s/%s: %w", provider, model, err)
		}
		if record, ok := p.LookupRecord(provider, model); ok {
			return record, nil
		}
	}
	return Price{}, fmt.Errorf("pricing for %s/%s is absent from the warm cache; run `ktx prices update`", provider, model)
}

// resetPricesForTest clears the singleton so the next LoadPrices call
// re-resolves the path and re-loads from disk. Tests use this to
// exercise cold/warm cache transitions without restarting the process.
// Not exported — internal use only; production callers go through
// LoadPrices which caches for the process lifetime.
func resetPricesForTest() {
	pricesOnce = sync.Once{}
	pricesInstance = nil
	pricesLoadErr = nil
}
