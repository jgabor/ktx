// Residual-waste discovery: raw row query for the stats command.
//
// The query uses a LEFT JOIN from tool_calls to compressed so
// uncompressed calls surface with delivered bytes equal to raw
// bytes. When multiple compressed rows exist for the same source,
// the first by timestamp and ID is authoritative. The binary name is
// extracted in the CLI layer (reusing router.BinaryFromInput) so
// the extraction logic stays in one place.
package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jgabor/ktx/internal/telemetry"
)

// StatsRow is one tool_call row from the stats query. The
// CLI layer groups these by (Tool, extracted binary name) to
// produce the final ranked output.
type StatsRow struct {
	Tool           string
	Input          string
	RawBytes       int
	DeliveredBytes int
}

// StatsRows returns one row per tool_call with the raw output
// length and the delivered (compressed) output length. When no
// compressed row exists, delivered equals raw. When multiple
// compressed rows exist for the same source, the first by timestamp
// and ID is authoritative.
func (d *DB) StatsRows() ([]StatsRow, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("db: stats: database is nil")
	}

	query := `
		SELECT tc.tool,
		       tc.input,
		       LENGTH(tc.output),
		       COALESCE(
			(SELECT LENGTH(c.output) FROM compressed c
			 WHERE c.source_id = tc.id
			 ORDER BY c.timestamp ASC, c.id ASC LIMIT 1),
			LENGTH(tc.output)
		       )
		FROM tool_calls tc
		WHERE tc.output IS NOT NULL
	`
	rows, err := d.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("db: stats query: %w", err)
	}
	defer rows.Close()

	var result []StatsRow
	for rows.Next() {
		var r StatsRow
		if err := rows.Scan(&r.Tool, &r.Input, &r.RawBytes, &r.DeliveredBytes); err != nil {
			return nil, fmt.Errorf("db: stats scan: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: stats iterate: %w", err)
	}
	return result, nil
}

// StatsSummaryRow is one source call in a bounded stats summary. SQLite LENGTH
// counts characters rather than UTF-8 bytes, matching the existing stats token
// estimate. HasCompressedArtifact reports retained-artifact coverage even when
// the selected artifact output is NULL.
type StatsSummaryRow struct {
	Tool                  string
	Input                 string
	Profile               string
	Route                 string
	Disposition           string
	IsError               *bool
	RawChars              int
	DeliveredChars        int
	HasCompressedArtifact bool
}

// StatsSummaryCacheRow is one accepted runtime model-step observation in a
// bounded stats summary. Session and capture fields describe the contributing
// observation, while numeric fields preserve their measured, unknown, or
// malformed state so callers cannot mistake a partial sum for a complete one.
type StatsSummaryCacheRow struct {
	SessionID     string
	CapturedAt    time.Time
	Provider      *string
	Model         *string
	UncachedInput IntegerMeasurement
	CacheRead     IntegerMeasurement
	CacheWrite    IntegerMeasurement
}

type statsSummarySelectedRow struct {
	row    StatsSummaryRow
	rawSet bool
}

const statsSummaryBatchSize = 128

// StatsSummaryRows returns source calls with non-NULL output and an RFC3339
// timestamp in the inclusive [from, to] interval. Each source contributes at
// most one artifact: earliest compressed timestamp, then smallest ID. A missing
// artifact (or an anomalous artifact with NULL output) delivers the raw output.
func (d *DB) StatsSummaryRows(from, to time.Time) ([]StatsSummaryRow, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("db: stats summary: database is nil")
	}
	return statsSummaryRowsFor(d.db, from, to, nil)
}

// StatsSummaryCacheRows returns accepted runtime model-step observations from
// current trusted collection sources whose capture timestamp is in the inclusive
// [from, to] interval and whose runtime identity matches the requested accounting
// contract.
func (d *DB) StatsSummaryCacheRows(from, to time.Time, runtime, runtimeVersion string) ([]StatsSummaryCacheRow, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("db: stats summary cache: database is nil")
	}
	from = from.UTC()
	to = to.UTC()
	rows, err := d.db.Query(`SELECT session_id, captured_at, provider, model,
			tokens_input, cache_read, cache_write,
			tokens_input_state, cache_read_state, cache_write_state
		FROM session_telemetry
		WHERE runtime = ? AND runtime_version = ?
			AND source IN (?, ?)`, runtime, runtimeVersion, telemetry.SourceEventHook, telemetry.SourceWrapperStream)
	if err != nil {
		return nil, fmt.Errorf("db: stats summary cache query: %w", err)
	}
	defer rows.Close()

	result := make([]StatsSummaryCacheRow, 0)
	for rows.Next() {
		var sessionID, capturedAt, inputState, cacheReadState, cacheWriteState string
		var input, cacheRead, cacheWrite sql.NullInt64
		var provider, model sql.NullString
		if err := rows.Scan(&sessionID, &capturedAt, &provider, &model, &input, &cacheRead, &cacheWrite, &inputState, &cacheReadState, &cacheWriteState); err != nil {
			return nil, fmt.Errorf("db: stats summary cache scan: %w", err)
		}
		captured, err := time.Parse(time.RFC3339Nano, capturedAt)
		if err != nil || captured.Before(from) || captured.After(to) {
			continue
		}
		captured = captured.UTC()
		row := StatsSummaryCacheRow{
			SessionID:     sessionID,
			CapturedAt:    captured,
			UncachedInput: databaseIntegerMeasurement(inputState, input),
			CacheRead:     databaseIntegerMeasurement(cacheReadState, cacheRead),
			CacheWrite:    databaseIntegerMeasurement(cacheWriteState, cacheWrite),
		}
		if provider.Valid {
			row.Provider = &provider.String
		}
		if model.Valid {
			row.Model = &model.String
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: stats summary cache iterate: %w", err)
	}
	return result, nil
}

// statsSummaryRowsFor reads source IDs before reading output lengths so that
// the first SQLite read is released before the potentially expensive second
// phase. The queries deliberately do not share a transaction: a compressed
// artifact committed between phases may be included, while source calls are
// append-only.
// afterSourceIDs exists only to synchronize the rollback-journal contention
// regression at the phase boundary.
func statsSummaryRowsFor(database *sql.DB, from, to time.Time, afterSourceIDs func()) ([]StatsSummaryRow, error) {
	from = from.UTC()
	to = to.UTC()

	ids, err := statsSummarySourceIDs(database, from, to)
	if err != nil {
		return nil, err
	}
	if afterSourceIDs != nil {
		afterSourceIDs()
	}
	if len(ids) == 0 {
		return []StatsSummaryRow{}, nil
	}

	selected := make(map[string]*statsSummarySelectedRow, len(ids))
	for _, sourceID := range ids {
		selected[sourceID] = &statsSummarySelectedRow{}
	}

	if err := forEachStatsSummaryBatch(ids, func(batch []string) error {
		if err := statsSummaryLengths(database, batch, selected); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	result := make([]StatsSummaryRow, 0, len(ids))
	for _, sourceID := range ids {
		result = append(result, selected[sourceID].row)
	}
	return result, nil
}

func forEachStatsSummaryBatch(ids []string, fn func([]string) error) error {
	for start := 0; start < len(ids); start += statsSummaryBatchSize {
		end := min(start+statsSummaryBatchSize, len(ids))
		if err := fn(ids[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// statsSummarySourceIDs finishes and closes its rows before returning. This
// keeps the phase-one rollback-journal read from blocking writers while output
// and artifact lengths are read in phase two.
func statsSummarySourceIDs(database *sql.DB, from, to time.Time) ([]string, error) {
	rows, err := database.Query(`
		SELECT id, timestamp
		FROM tool_calls
		WHERE output IS NOT NULL AND timestamp IS NOT NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("db: stats summary source query: %w", err)
	}

	selected := make(map[string]struct{})
	for rows.Next() {
		var sourceID, timestamp string
		if err := rows.Scan(&sourceID, &timestamp); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("db: stats summary source scan: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil || parsed.Before(from) || parsed.After(to) {
			continue
		}
		selected[sourceID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("db: stats summary source iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("db: stats summary source close: %w", err)
	}

	ids := make([]string, 0, len(selected))
	for sourceID := range selected {
		ids = append(ids, sourceID)
	}
	sort.Strings(ids)
	return ids, nil
}

func statsSummaryLengths(database *sql.DB, ids []string, selected map[string]*statsSummarySelectedRow) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, sourceID := range ids {
		args[i] = sourceID
	}

	rows, err := database.Query(`
		SELECT tc.id, COALESCE(tc.tool, ''), COALESCE(tc.input, ''),
		       CASE WHEN typeof(tc.is_error) = 'integer' AND tc.is_error IN (0, 1) THEN tc.is_error END,
		       LENGTH(tc.output),
		       cd.profile, cd.route, cd.disposition,
		       c.id, LENGTH(c.output)
		FROM tool_calls tc
		LEFT JOIN compression_dispositions cd ON cd.source_id = tc.id
		LEFT JOIN compressed c ON c.id = (
			SELECT first.id FROM compressed first
			WHERE first.source_id = tc.id
			ORDER BY first.timestamp ASC, first.id ASC
			LIMIT 1
		)
		WHERE tc.id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return fmt.Errorf("db: stats summary length query: %w", err)
	}
	for rows.Next() {
		var (
			sourceID                    string
			tool, input                 string
			isError                     sql.NullInt64
			rawChars                    int
			profile, route, disposition sql.NullString
			artifactID                  sql.NullString
			artifactChars               sql.NullInt64
		)
		if err := rows.Scan(
			&sourceID, &tool, &input, &isError, &rawChars,
			&profile, &route, &disposition,
			&artifactID, &artifactChars,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("db: stats summary length scan: %w", err)
		}
		current := selected[sourceID]
		if current == nil {
			_ = rows.Close()
			return fmt.Errorf("db: stats summary length returned unselected source %q", sourceID)
		}
		if !current.rawSet {
			current.rawSet = true
			current.row.Tool = tool
			current.row.Input = input
			if isError.Valid && (isError.Int64 == 0 || isError.Int64 == 1) {
				value := isError.Int64 == 1
				current.row.IsError = &value
			}
			current.row.Profile = profile.String
			current.row.Route = route.String
			current.row.Disposition = CompressionDispositionUnobserved
			if disposition.Valid {
				current.row.Disposition = disposition.String
			}
			current.row.RawChars = rawChars
			current.row.DeliveredChars = rawChars
		}
		if !artifactID.Valid {
			continue
		}
		current.row.HasCompressedArtifact = true
		if artifactChars.Valid {
			current.row.DeliveredChars = int(artifactChars.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("db: stats summary length iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("db: stats summary length close: %w", err)
	}
	return nil
}
