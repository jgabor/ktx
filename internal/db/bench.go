// Persistence and query methods for `bench_runs`.
//
// bench_runs is the headline bench store — one row per `ktx bench run`
// invocation. The schema lives in db.go (reproducibility contract):
//
//	id TEXT PRIMARY KEY          -- 16-char nanoid
//	agent TEXT                   -- the agent binary / model spec
//	model TEXT                   -- the model id
//	task TEXT                    -- the task identifier
//	score REAL                   -- exact-match: 1.0 or 0.0
//	tokens_in INTEGER            -- SUM of underlying tool_calls
//	tokens_out INTEGER           -- SUM of underlying tool_calls
//	cost REAL                    -- runtime-reported cost
//	methodology_version TEXT     -- references
//	                               tools/bench/methodology-v1.yaml
//	timestamp INTEGER            -- Unix epoch seconds
//
// EstimatedAPICost is derived separately from the cache-aware model price;
// Cost is never filled or replaced by that estimate.
//
// Pointer discipline mirrors ToolCall: Cost is *float64
// so the absent-vs-zero distinction survives the SQL round-trip. A
// nil Cost pointer round-trips to SQL NULL; a non-nil pointer to 0
// round-trips to SQL 0. The other BenchRun columns are required by
// the bench contract (every bench row carries an id, agent, model, task,
// methodology_version, and timestamp), so they use plain types and
// are surfaced as the empty string / zero value when absent — the
// caller (the CLI layer) is responsible for populating them.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrBenchMethodologyNotFound is returned by LookupMethodologyVersion
// when the methodology file is absent or unreadable. The CLI layer
// maps this to ExitNotFound (exit 2): missing
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
// schema inherits from tool_calls).
//
// CacheRead and CacheWrite carry the SUM of per-request prompt-cache
// token counts from the runtime's step_finish events. They are NULL
// when the runtime did not emit cache telemetry so the absent-vs-zero distinction survives the SQL round-trip — a runtime
// that emits an explicit zero cache_read stays distinct from one that
// emits nothing. Cache evidence is sourced from the captured stream,
// never from tool_calls (the per-tool rows are NULL-by-design for
// step-aggregate usage under OpenCode 1.17.8).
//
// The JSON shape mirrors what `ktx bench report` emits in its `rows`
// array (each row carries id, agent, model, task, score, cost,
// methodology_version, timestamp). Tokens_in and tokens_out are
// surfaced too because they are the inputs to the cost (the
// reconstructability contract); the bench report's row view will
// include them.
type BenchRun struct {
	ID                   string   `json:"id"`
	Agent                string   `json:"agent"`
	Model                string   `json:"model"`
	Task                 string   `json:"task"`
	Score                float64  `json:"score"`
	TokensIn             int64    `json:"tokens_in"`
	TokensOut            int64    `json:"tokens_out"`
	Cost                 *float64 `json:"cost"`
	MethodologyVersion   string   `json:"methodology_version"`
	Timestamp            int64    `json:"timestamp"`
	CacheRead            *int64   `json:"cache_read"`
	CacheWrite           *int64   `json:"cache_write"`
	Reasoning            *int64   `json:"reasoning"`
	EstimatedAPICost     *float64 `json:"estimated_api_cost"`
	SkipEstimatedAPICost bool     `json:"-"`
}

func (d *DB) deriveBenchEstimatedCost(b *BenchRun) {
	if b == nil || b.EstimatedAPICost != nil || b.SkipEstimatedAPICost || b.Model == "" {
		return
	}
	prices, _ := LoadPrices()
	if prices == nil {
		return
	}
	provider := providerModel(b.Model)
	if prefix, _, ok := strings.Cut(b.Agent, "/"); ok && prefix != "" {
		provider = prefix
	}
	price, ok := prices.LookupRecord(provider, b.Model)
	if !ok && prices.Cold() {
		if prices.RefreshAndWrite() == nil {
			price, ok = prices.LookupRecord(provider, b.Model)
		}
	}
	if !ok {
		return
	}
	b.EstimatedAPICost = EstimateAPICost(EstimatedCostInputs{UncachedInput: b.TokensIn, Output: b.TokensOut, Reasoning: b.Reasoning, CacheRead: b.CacheRead, CacheWrite: b.CacheWrite}, price)
}

// providerModel returns the cache-key provider for a benchmark model.
func providerModel(model string) string {
	switch model {
	case "claude-sonnet-4-20250514":
		return "anthropic"
	default:
		// Benchmark methodologies currently use Anthropic models.
		return "anthropic"
	}
}

// InsertBenchRun persists a single bench_runs row.
// The write uses the shared bounded transaction retry path.
//
// deriveBenchCost runs before the transaction begins; the in-memory
// b.Cost pointer is updated in place (or left nil on soft-fail) and
// the SQL INSERT sees the post-derivation value.
//
// The INSERT covers all current columns. Nullable evidence
// columns are bound through their pointers: nil maps to SQL NULL via
// database/sql's typed-nil-pointer convention (same as ToolCall.Cost).
func (d *DB) InsertBenchRun(b *BenchRun) error {
	if b == nil {
		return errors.New("db: insert bench_run: nil bench run")
	}
	if b.ID == "" {
		return errors.New("db: insert bench_run: id is empty")
	}

	// Cost is runtime-reported evidence and is never derived or overwritten.
	d.deriveBenchEstimatedCost(b)

	ctx, cancel := writeContext()
	defer cancel()
	err := d.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(
			ctx,
			`INSERT INTO bench_runs (
			id, agent, model, task, score,
			tokens_in, tokens_out, cost,
			methodology_version, timestamp,
			cache_read, cache_write, reasoning, estimated_api_cost
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.Agent, b.Model, b.Task, b.Score,
			b.TokensIn, b.TokensOut, b.Cost,
			b.MethodologyVersion, b.Timestamp,
			b.CacheRead, b.CacheWrite, b.Reasoning, b.EstimatedAPICost,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: insert bench_run %q: %w", b.ID, err)
	}
	return nil
}

// QueryBenchRuns returns all bench_runs rows ordered by timestamp
// ascending, with id as the stable tie-breaker. The bench contract is
// "rows array, oldest first" — the CLI's `bench report` output
// renders rows in this order so the table view is chronological.
//
// The slice is non-nil but may be empty when no bench_runs rows
// exist; the CLI layer treats an empty slice as "no runs yet" and
// emits `rows: []` rather than a nil check.
//
// The Scan uses sql.NullFloat64 for Cost and sql.NullInt64 for the
// cache telemetry columns so a NULL column round-trips to a nil
// pointer on the BenchRun struct, preserving the absent-vs-zero
// distinction.
func (d *DB) QueryBenchRuns() ([]BenchRun, error) {
	rows, err := d.db.Query(
		`SELECT id, agent, model, task, score,
		        tokens_in, tokens_out, cost,
		        methodology_version, timestamp,
		        cache_read, cache_write, reasoning, estimated_api_cost
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
			b          BenchRun
			cost       sql.NullFloat64
			cacheRead  sql.NullInt64
			cacheWrite sql.NullInt64
			reasoning  sql.NullInt64
			estimated  sql.NullFloat64
		)
		if err := rows.Scan(
			&b.ID, &b.Agent, &b.Model, &b.Task, &b.Score,
			&b.TokensIn, &b.TokensOut, &cost,
			&b.MethodologyVersion, &b.Timestamp,
			&cacheRead, &cacheWrite, &reasoning, &estimated,
		); err != nil {
			return nil, fmt.Errorf("db: scan bench_run: %w", err)
		}
		if cost.Valid {
			v := cost.Float64
			b.Cost = &v
		}
		if cacheRead.Valid {
			v := cacheRead.Int64
			b.CacheRead = &v
		}
		if cacheWrite.Valid {
			v := cacheWrite.Int64
			b.CacheWrite = &v
		}
		if reasoning.Valid {
			v := reasoning.Int64
			b.Reasoning = &v
		}
		if estimated.Valid {
			v := estimated.Float64
			b.EstimatedAPICost = &v
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
// missing id to ExitNotFound (exit 2). The same
// pointer-discipline rules apply as QueryBenchRuns (nil Cost and
// nil cache telemetry when the columns are NULL).
func (d *DB) QueryBenchRun(id string) (BenchRun, error) {
	var (
		b          BenchRun
		cost       sql.NullFloat64
		cacheRead  sql.NullInt64
		cacheWrite sql.NullInt64
		reasoning  sql.NullInt64
		estimated  sql.NullFloat64
	)
	err := d.db.QueryRow(
		`SELECT id, agent, model, task, score,
		        tokens_in, tokens_out, cost,
		        methodology_version, timestamp,
		        cache_read, cache_write, reasoning, estimated_api_cost
		 FROM bench_runs WHERE id = ?`,
		id,
	).Scan(
		&b.ID, &b.Agent, &b.Model, &b.Task, &b.Score,
		&b.TokensIn, &b.TokensOut, &cost,
		&b.MethodologyVersion, &b.Timestamp,
		&cacheRead, &cacheWrite, &reasoning, &estimated,
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
	if cacheRead.Valid {
		v := cacheRead.Int64
		b.CacheRead = &v
	}
	if cacheWrite.Valid {
		v := cacheWrite.Int64
		b.CacheWrite = &v
	}
	if reasoning.Valid {
		v := reasoning.Int64
		b.Reasoning = &v
	}
	if estimated.Valid {
		v := estimated.Float64
		b.EstimatedAPICost = &v
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
// invocation, so all tool_calls belonging to the bench run's
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
