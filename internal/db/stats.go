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

// StatsSummaryRows returns source calls with non-NULL output and an RFC3339
// timestamp in the inclusive [from, to] interval. Each source contributes at
// most one artifact: greatest compressed timestamp, then greatest ID. A missing
// artifact (or an anomalous artifact with NULL output) delivers the raw output.
func (d *DB) StatsSummaryRows(from, to time.Time) ([]StatsSummaryRow, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("db: stats summary: database is nil")
	}

	rows, err := d.db.Query(`
		SELECT tc.id, LENGTH(tc.output), tc.timestamp,
		       c.id, LENGTH(c.output), c.timestamp
		FROM tool_calls tc
		LEFT JOIN compressed c ON c.source_id = tc.id
		WHERE tc.output IS NOT NULL AND tc.timestamp IS NOT NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("db: stats summary query: %w", err)
	}
	defer rows.Close()

	from = from.UTC()
	to = to.UTC()
	type selectedRow struct {
		row               StatsSummaryRow
		artifactID        string
		artifactTimestamp string
	}
	selected := make(map[string]*selectedRow)
	for rows.Next() {
		var (
			sourceID, timestamp           string
			rawChars                      int
			artifactID, artifactTimestamp sql.NullString
			artifactChars                 sql.NullInt64
		)
		if err := rows.Scan(&sourceID, &rawChars, &timestamp, &artifactID, &artifactChars, &artifactTimestamp); err != nil {
			return nil, fmt.Errorf("db: stats summary scan: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil || parsed.Before(from) || parsed.After(to) {
			continue
		}
		current, ok := selected[sourceID]
		if !ok {
			current = &selectedRow{row: StatsSummaryRow{RawChars: rawChars, DeliveredChars: rawChars}}
			selected[sourceID] = current
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
		return nil, fmt.Errorf("db: stats summary iterate: %w", err)
	}
	result := make([]StatsSummaryRow, 0, len(selected))
	for _, row := range selected {
		result = append(result, row.row)
	}
	return result, nil
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
