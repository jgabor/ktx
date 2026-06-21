// Persistence and query methods for `bench_runs` (M3 plan Task 4).
//
// bench_runs is the M3 headline store — one row per `ktx bench run`
// invocation. The schema lives in db.go (D12 reproducibility contract):
//
//	id TEXT PRIMARY KEY          -- D3 16-char nanoid
//	agent TEXT                   -- the agent binary / model spec
//	model TEXT                   -- the model id
//	task TEXT                    -- the task identifier
//	score REAL                   -- exact-match: 1.0 or 0.0
//	tokens_in INTEGER            -- SUM of underlying tool_calls
//	tokens_out INTEGER           -- SUM of underlying tool_calls
//	cost REAL                    -- SUM of underlying tool_calls (D11
//	                               derivation when the sum is NULL)
//	methodology_version TEXT     -- D12: references
//	                               .agentera/bench/methodology-v1.yaml
//	timestamp INTEGER            -- Unix epoch seconds
//
// Cost derivation (Decision 11) follows the same write-time soft-fail
// pattern as PersistToolCall.deriveCost: when the caller passes a
// BenchRun with a nil Cost pointer AND non-nil TokensIn, TokensOut,
// and Model, the helper looks up the cached (provider-derived, model)
// price and fills Cost from (tokens_in * in_price + tokens_out *
// out_price) / 1_000_000. The cache key in the bench-run path uses a
// provider resolved from the bench's model — the only provider model
// we currently know about in the bench-run path is "anthropic" (the
// pinned model is claude-sonnet-4-20250514; the methodology file
// names it as `provider: anthropic`). When the cache does not have
// the price, Cost stays nil and the bench row is inserted with cost
// NULL — D11 soft-fail contract preserved.
//
// Pointer discipline mirrors ToolCall (Task 3): Cost is *float64
// so the absent-vs-zero distinction survives the SQL round-trip. A
// nil Cost pointer round-trips to SQL NULL; a non-nil pointer to 0
// round-trips to SQL 0. The other BenchRun columns are required by
// the D12 contract (every bench row carries an id, agent, model, task,
// methodology_version, and timestamp), so they use plain types and
// are surfaced as the empty string / zero value when absent — the
// caller (the CLI layer) is responsible for populating them.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// ErrBenchMethodologyNotFound is returned by LookupMethodologyVersion
// when the methodology file is absent or unreadable. The CLI layer
// maps this to ExitNotFound (exit 2) per plan Task 4 AC7: missing
// methodology is a hard failure, not a soft fail — the bench headline
// has no meaning without the methodology reference.
var ErrBenchMethodologyNotFound = errors.New("db: bench methodology file not found")

// BenchRun is one row of the bench_runs table.
//
// JSON tags follow the column names so the CLI layer can marshal
// directly without an intermediate struct. omitempty is intentionally
// NOT applied to the pointer fields: nil Cost and the zero-value
// optional fields must round-trip through JSON as `null` (the absent-
// vs-zero distinction is the same round-trip contract the bench_runs
// schema inherits from tool_calls, D9).
//
// The JSON shape mirrors what `ktx bench report` emits in its `rows`
// array (D12: each row carries id, agent, model, task, score, cost,
// methodology_version, timestamp). Tokens_in and tokens_out are
// surfaced too because they are the inputs to the cost (D11
// reconstructability contract); the bench report's row view will
// include them.
type BenchRun struct {
	ID                string  `json:"id"`
	Agent             string  `json:"agent"`
	Model             string  `json:"model"`
	Task              string  `json:"task"`
	Score             float64 `json:"score"`
	TokensIn          int64   `json:"tokens_in"`
	TokensOut         int64   `json:"tokens_out"`
	Cost              *float64 `json:"cost"`
	MethodologyVersion string `json:"methodology_version"`
	Timestamp         int64   `json:"timestamp"`
}

// deriveBenchCost mirrors PersistToolCall.deriveCost at the bench-run
// level. The caller passes a BenchRun whose tokens_in, tokens_out, and
// model are populated (the bench-run rollup aggregates these from the
// underlying tool_calls; an empty model means the bench has nothing to
// price against). When Cost is nil AND TokensIn, TokensOut, and Model
// are all non-zero-meaningful AND the cache has a price for the
// (provider, model) tuple, the helper fills Cost from:
//
//	cost = (tokens_in * in_price + tokens_out * out_price) / 1_000_000
//
// rounded to 6 decimals (matches the M2 tool_call derivation
// precision; the methodology-v1 headline number is six decimals).
//
// Lookup behaviour is identical to PersistToolCall.deriveCost:
// cold-cache lookup miss triggers a fetch + best-effort disk write;
// warm-cache lookup miss is silent (cost stays nil). Any failure
// leaves Cost nil — the D11 soft-fail contract requires the bench row
// to be persisted with whatever cost the caller supplied on a
// derivation failure.
//
// providerModel returns the (provider, model) pair to use for the
// cache lookup. v1 only knows the (anthropic, claude-sonnet-4-20250514)
// pair (the methodology file pins it). A future methodology version
// that introduces a different provider/model pair would extend
// providerModel's switch accordingly; the bench_runs row's model
// column is the canonical reference.
func (d *DB) deriveBenchCost(b *BenchRun) {
	if b == nil {
		return
	}
	if b.Cost != nil {
		return
	}
	// D11 NULL preservation: tokens and model must all be present for
	// derivation to fire. A zero token count on either side is a real
	// value, not an absent one — cost = 0 is a meaningful signal
	// (zero-token call), not a missing input.
	if b.TokensIn == 0 && b.TokensOut == 0 {
		return
	}
	if b.Model == "" {
		return
	}

	prices, _ := LoadPrices()
	if prices == nil || prices.path == "" {
		return
	}

	provider := providerModel(b.Model)
	in, out, ok := prices.Lookup(provider, b.Model)
	if !ok && prices.Cold() {
		if err := prices.Refresh(); err == nil {
			in, out, ok = prices.Lookup(provider, b.Model)
			if ok {
				_ = prices.WriteToDisk()
			}
		}
	}
	if !ok {
		return
	}

	raw := float64(b.TokensIn)*in + float64(b.TokensOut)*out
	raw = raw / 1_000_000
	rounded := math.Round(raw*math.Pow(10, costRoundDecimals)) / math.Pow(10, costRoundDecimals)
	b.Cost = &rounded
}

// providerModel returns the cache-key provider for a given model id.
// v1 only knows the methodology-pinned (anthropic, claude-sonnet-4-20250514)
// pair; the methodology file's `inputs.model.provider` field is
// authoritative for future versions. The bench-run CLI layer passes
// the model id verbatim; this helper is the local single source of
// truth that bridges the bench-run model id and the price cache key.
//
// When a new methodology version introduces a new (provider, model)
// pair, this switch is the place to extend. YAGNI for v1 — the v1
// methodology commits to one (agent, model, task) triple.
func providerModel(model string) string {
	switch model {
	case "claude-sonnet-4-20250514":
		return "anthropic"
	default:
		// Default to "anthropic" as a best-effort fallback for v1:
		// the methodology pins anthropic. A future version that adds
		// non-Anthropic models will replace this with a table-driven
		// lookup. For v1 this branch only fires on typos or future
		// models added before this switch is extended — both of which
		// are out-of-scope failures (D11 soft-fail: cost stays nil).
		return "anthropic"
	}
}

// InsertBenchRun persists a single bench_runs row (M3 plan Task 4).
// The transaction is the same idempotency pattern as PersistToolCall:
// the BEGIN/Commit pair with defer tx.Rollback() matches the M2 review
// fix-up pattern (tx.Rollback is a no-op after a successful Commit).
//
// deriveBenchCost runs before the transaction begins; the in-memory
// b.Cost pointer is updated in place (or left nil on soft-fail) and
// the SQL INSERT sees the post-derivation value.
//
// The INSERT covers all 10 columns. Cost is bound through the
// pointer: nil maps to SQL NULL via database/sql's typed-nil-pointer
// convention (same as ToolCall.Cost).
func (d *DB) InsertBenchRun(b *BenchRun) error {
	if b == nil {
		return errors.New("db: insert bench_run: nil bench run")
	}
	if b.ID == "" {
		return errors.New("db: insert bench_run: id is empty")
	}

	d.deriveBenchCost(b) // write-time D11 derivation; soft-fail

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("db: begin bench_run tx: %w", err)
	}
	defer tx.Rollback() // Safe, low-overhead no-op after tx.Commit()

	_, err = tx.Exec(
		`INSERT INTO bench_runs (
			id, agent, model, task, score,
			tokens_in, tokens_out, cost,
			methodology_version, timestamp
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.Agent, b.Model, b.Task, b.Score,
		b.TokensIn, b.TokensOut, b.Cost,
		b.MethodologyVersion, b.Timestamp,
	)
	if err != nil {
		return fmt.Errorf("db: insert bench_run %q: %w", b.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit bench_run %q: %w", b.ID, err)
	}
	return nil
}

// QueryBenchRuns returns all bench_runs rows ordered by timestamp
// ascending, with id as the stable tie-breaker. D12's contract is
// "rows array, oldest first" — the CLI's `bench report` output
// renders rows in this order so the table view is chronological.
//
// The slice is non-nil but may be empty when no bench_runs rows
// exist; the CLI layer treats an empty slice as "no runs yet" and
// emits `rows: []` rather than a nil check.
//
// The Scan uses sql.NullFloat64 for Cost so a NULL cost column
// round-trips to a nil *float64 on the BenchRun struct, preserving
// the absent-vs-zero distinction.
func (d *DB) QueryBenchRuns() ([]BenchRun, error) {
	rows, err := d.db.Query(
		`SELECT id, agent, model, task, score,
		        tokens_in, tokens_out, cost,
		        methodology_version, timestamp
		 FROM bench_runs
		 ORDER BY timestamp ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: query bench_runs: %w", err)
	}
	defer rows.Close()

	out := []BenchRun{}
	for rows.Next() {
		var (
			b    BenchRun
			cost sql.NullFloat64
		)
		if err := rows.Scan(
			&b.ID, &b.Agent, &b.Model, &b.Task, &b.Score,
			&b.TokensIn, &b.TokensOut, &cost,
			&b.MethodologyVersion, &b.Timestamp,
		); err != nil {
			return nil, fmt.Errorf("db: scan bench_run: %w", err)
		}
		if cost.Valid {
			v := cost.Float64
			b.Cost = &v
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate bench_runs: %w", err)
	}
	return out, nil
}

// QueryBenchRun fetches a single bench_runs row by id. Returns
// ErrNotFound when no row matches, so the CLI layer can map a
// missing id to ExitNotFound (exit 2, D4 contract). The same
// pointer-discipline rules apply as QueryBenchRuns (nil Cost when
// the column is NULL).
func (d *DB) QueryBenchRun(id string) (BenchRun, error) {
	var (
		b    BenchRun
		cost sql.NullFloat64
	)
	err := d.db.QueryRow(
		`SELECT id, agent, model, task, score,
		        tokens_in, tokens_out, cost,
		        methodology_version, timestamp
		 FROM bench_runs WHERE id = ?`,
		id,
	).Scan(
		&b.ID, &b.Agent, &b.Model, &b.Task, &b.Score,
		&b.TokensIn, &b.TokensOut, &cost,
		&b.MethodologyVersion, &b.Timestamp,
	)
	if err == sql.ErrNoRows {
		return BenchRun{}, ErrNotFound
	}
	if err != nil {
		return BenchRun{}, fmt.Errorf("db: get bench_run %q: %w", id, err)
	}
	if cost.Valid {
		v := cost.Float64
		b.Cost = &v
	}
	return b, nil
}

// SummarizeBenchToolCalls computes the (tokens_in, tokens_out, cost,
// model, provider) rollup for a session's underlying tool_calls rows.
// Returns sql.NullFloat64 / sql.NullString intermediates so the
// NULL-aware SUM / MIN aggregates surface as NULL when every input
// row is NULL — the bench run's tokens/cost/model fields stay at
// their zero / empty value in that case (the NULL preservation
// contract from the bench_run semantics: absent-vs-zero is
// observable downstream).
//
// SQLite's SUM ignores NULL operands (so a row with either field
// NULL contributes NULL to the per-call sum and is excluded from
// the aggregate). MIN returns NULL only when every input is NULL.
// This matches the methodology's "non-NULL rows only" aggregation
// rule from the input block.
//
// Used by the bench run aggregator (cli/bench.go's benchRunFromSession
// helper) to fill the bench_runs row's tokens_in, tokens_out, cost,
// and model from a wrapper invocation's tool_calls.
//
// The sessionID parameter scopes the aggregation to a single wrapper
// invocation's session — bench run opens a fresh session per
// invocation (D1), so all tool_calls belonging to the bench run's
// underlying agent invocation share that session_id.
func (d *DB) SummarizeBenchToolCalls(sessionID string) (tokensIn, tokensOut int64, cost *float64, model, provider string, err error) {
	var (
		nullCost     sql.NullFloat64
		nullModel    sql.NullString
		nullProvider sql.NullString
	)
	err = d.db.QueryRow(
		`SELECT COALESCE(SUM(tokens_in), 0),
		        COALESCE(SUM(tokens_out), 0),
		        SUM(cost),
		        MIN(model),
		        MIN(provider)
		 FROM tool_calls
		 WHERE session_id = ?`,
		sessionID,
	).Scan(&tokensIn, &tokensOut, &nullCost, &nullModel, &nullProvider)
	if err != nil {
		return 0, 0, nil, "", "", fmt.Errorf("db: summarize bench tool_calls %q: %w", sessionID, err)
	}
	if nullCost.Valid {
		v := nullCost.Float64
		cost = &v
	}
	model = nullModel.String
	provider = nullProvider.String
	return tokensIn, tokensOut, cost, model, provider, nil
}
