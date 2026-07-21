// Residual-waste discovery: raw row query for the stats command.
//
// The query uses a LEFT JOIN from tool_calls to compressed so
// uncompressed calls surface with delivered bytes equal to raw
// bytes. When multiple compressed rows exist for the same source,
// the most recent by timestamp is used. The binary name is
// extracted in the CLI layer (reusing router.BinaryFromInput) so
// the extraction logic stays in one place.
package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
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
// compressed rows exist for the same source, the most recent by
// timestamp is used.
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
			 ORDER BY c.timestamp DESC LIMIT 1),
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
	RawChars              int
	DeliveredChars        int
	HasCompressedArtifact bool
}

type statsSummarySelectedRow struct {
	row               StatsSummaryRow
	rawSet            bool
	artifactID        string
	artifactTimestamp string
}

const statsSummaryBatchSize = 128

// StatsSummaryRows returns source calls with non-NULL output and an RFC3339
// timestamp in the inclusive [from, to] interval. Each source contributes at
// most one artifact: greatest compressed timestamp, then greatest ID. A missing
// artifact (or an anomalous artifact with NULL output) delivers the raw output.
func (d *DB) StatsSummaryRows(from, to time.Time) ([]StatsSummaryRow, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("db: stats summary: database is nil")
	}
	return statsSummaryRowsFor(d.db, from, to, nil)
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
		SELECT tc.id, LENGTH(tc.output),
		       c.id, LENGTH(c.output), c.timestamp
		FROM tool_calls tc
		LEFT JOIN compressed c ON c.source_id = tc.id
		WHERE tc.id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return fmt.Errorf("db: stats summary length query: %w", err)
	}
	for rows.Next() {
		var (
			sourceID                      string
			rawChars                      int
			artifactID, artifactTimestamp sql.NullString
			artifactChars                 sql.NullInt64
		)
		if err := rows.Scan(&sourceID, &rawChars, &artifactID, &artifactChars, &artifactTimestamp); err != nil {
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
			current.row.RawChars = rawChars
			current.row.DeliveredChars = rawChars
		}
		if !artifactID.Valid {
			continue
		}
		current.row.HasCompressedArtifact = true
		candidateTimestamp := artifactTimestamp.String
		if current.artifactID == "" || compressedArtifactLater(candidateTimestamp, artifactID.String, current.artifactTimestamp, current.artifactID) {
			current.artifactID = artifactID.String
			current.artifactTimestamp = candidateTimestamp
			current.row.DeliveredChars = rawChars
			if artifactChars.Valid {
				current.row.DeliveredChars = int(artifactChars.Int64)
			}
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

func compressedArtifactLater(candidateTimestamp, candidateID, currentTimestamp, currentID string) bool {
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidateTimestamp)
	currentTime, currentErr := time.Parse(time.RFC3339Nano, currentTimestamp)
	switch {
	case candidateErr == nil && currentErr == nil:
		if !candidateTime.Equal(currentTime) {
			return candidateTime.After(currentTime)
		}
	case candidateErr == nil:
		return true
	case currentErr == nil:
		return false
	default:
		if candidateTimestamp != currentTimestamp {
			return candidateTimestamp > currentTimestamp
		}
	}
	return candidateID > currentID
}
