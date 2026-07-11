package dbreport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 2

var reportTables = []string{"sessions", "tool_calls", "compressed", "recorded_streams"}

func Generate(path string, includeIdentifiers bool, now time.Time) (map[string]any, error) {
	database, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	schema, err := extractSchema(tx)
	if err != nil {
		return nil, err
	}
	overview, err := extractOverview(tx)
	if err != nil {
		return nil, err
	}
	report := map[string]any{
		"report_schema_version": SchemaVersion,
		"generated_at":          now.UTC().Format(time.RFC3339Nano),
		"source":                map[string]any{"database_file": filepath.Base(path)},
		"units":                 map[string]any{"storage": "UTF-8 bytes", "latency": "milliseconds", "cost": "USD", "percentages": "0-100"},
		"database_schema":       schema, "overview": overview,
	}
	if includeIdentifiers {
		absolute, _ := filepath.Abs(path)
		report["source"].(map[string]any)["database_path"] = absolute
	}
	extractors := []struct {
		name string
		fn   func(*sql.Tx) (any, error)
	}{
		{"measurement_coverage", extractMeasurementCoverage},
		{"failure_metrics", extractFailureMetrics},
		{"tool_metrics", func(tx *sql.Tx) (any, error) { return queryRows(tx, toolMetricsSQL) }},
		{"agent_metrics", func(tx *sql.Tx) (any, error) { return queryRows(tx, agentMetricsSQL) }},
		{"session_lifecycle", func(tx *sql.Tx) (any, error) { return extractSessionLifecycle(tx, includeIdentifiers) }},
		{"recorded_streams", extractRecordedStreams},
		{"daily_activity", extractDailyActivity},
		{"integrity", extractIntegrity},
	}
	for _, extractor := range extractors {
		value, err := extractor.fn(tx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", extractor.name, err)
		}
		report[extractor.name] = value
	}
	totalCalls := asInt64(overview["total_tool_calls"])
	compression, err := extractCompression(tx, totalCalls, includeIdentifiers)
	if err != nil {
		return nil, err
	}
	report["compression"] = compression
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return report, nil
}

func openReadOnly(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: absolute}).String() + "?mode=ro"
	database, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		return nil, err
	}
	if _, err := database.Exec("PRAGMA query_only = ON"); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func queryRows(database queryer, query string, args ...any) ([]map[string]any, error) {
	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for index, column := range columns {
			if bytes, ok := values[index].([]byte); ok {
				row[column] = string(bytes)
			} else {
				row[column] = values[index]
			}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func queryOne(database queryer, query string, args ...any) (map[string]any, error) {
	rows, err := queryRows(database, query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, fmt.Errorf("query returned %d rows", len(rows))
	}
	return rows[0], nil
}

func extractSchema(tx *sql.Tx) (map[string]any, error) {
	schema := map[string]any{}
	for _, table := range reportTables {
		columns, err := queryRows(tx, "PRAGMA table_info("+table+")")
		if err != nil {
			return nil, err
		}
		if len(columns) == 0 {
			return nil, fmt.Errorf("required table is missing: %s", table)
		}
		items := make([]any, 0, len(columns))
		for _, column := range columns {
			items = append(items, map[string]any{
				"name": column["name"], "type": column["type"], "nullable": asInt64(column["notnull"]) == 0,
				"default": column["dflt_value"], "primary_key": asInt64(column["pk"]) != 0,
			})
		}
		schema[table] = items
	}
	return schema, nil
}

func extractOverview(tx *sql.Tx) (map[string]any, error) {
	sessions, err := queryOne(tx, `SELECT COUNT(*) AS total_sessions,
COALESCE(SUM(CASE WHEN ended_at IS NULL OR ended_at = '' THEN 1 ELSE 0 END), 0) AS open_sessions,
COALESCE(SUM(CASE WHEN ended_at IS NOT NULL AND ended_at <> '' THEN 1 ELSE 0 END), 0) AS closed_sessions FROM sessions`)
	if err != nil {
		return nil, err
	}
	calls, err := queryOne(tx, `SELECT COUNT(*) AS total_tool_calls,
COALESCE(SUM(length(CAST(input AS BLOB))), 0) AS total_input_bytes,
COALESCE(SUM(length(CAST(output AS BLOB))), 0) AS total_output_bytes,
COUNT(DISTINCT session_id) AS sessions_with_tool_calls FROM tool_calls`)
	if err != nil {
		return nil, err
	}
	streams, err := queryOne(tx, `SELECT COUNT(*) AS total_recorded_streams,
COALESCE(SUM(length(CAST(stream_text AS BLOB))), 0) AS total_stream_bytes,
COUNT(DISTINCT session_id) AS sessions_with_recorded_streams FROM recorded_streams`)
	if err != nil {
		return nil, err
	}
	for key, value := range calls {
		sessions[key] = value
	}
	for key, value := range streams {
		sessions[key] = value
	}
	return sessions, nil
}

func extractMeasurementCoverage(tx *sql.Tx) (any, error) {
	metrics, err := queryOne(tx, `SELECT COUNT(*) AS total_tool_calls,
COUNT(tokens_in) AS calls_with_tokens_in, COUNT(tokens_out) AS calls_with_tokens_out,
COUNT(cost) AS calls_with_cost, COUNT(latency_ms) AS calls_with_latency,
COUNT(model) AS calls_with_model, COUNT(provider) AS calls_with_provider,
COUNT(exit_code) AS calls_with_exit_code, COUNT(is_error) AS calls_with_error_status,
SUM(tokens_in) AS total_tokens_in, SUM(tokens_out) AS total_tokens_out, SUM(cost) AS total_cost FROM tool_calls`)
	if err != nil {
		return nil, err
	}
	total := asInt64(metrics["total_tool_calls"])
	delete(metrics, "total_tool_calls")
	coverage := map[string]any{}
	for _, name := range []string{"tokens_in", "tokens_out", "cost", "latency", "model", "provider", "exit_code", "error_status"} {
		key := "calls_with_" + name
		count := asInt64(metrics[key])
		delete(metrics, key)
		coverage[name] = map[string]any{"measured_calls": count, "coverage_pct": percentage(float64(count), float64(total))}
	}
	latencyRows, err := queryRows(tx, "SELECT latency_ms FROM tool_calls WHERE latency_ms IS NOT NULL")
	if err != nil {
		return nil, err
	}
	latencies := make([]float64, 0, len(latencyRows))
	for _, row := range latencyRows {
		latencies = append(latencies, asFloat(row["latency_ms"]))
	}
	models, err := queryRows(tx, `SELECT model, COUNT(*) AS call_count, SUM(tokens_in) AS tokens_in,
SUM(tokens_out) AS tokens_out, SUM(cost) AS cost FROM tool_calls WHERE model IS NOT NULL
GROUP BY model ORDER BY call_count DESC, model`)
	if err != nil {
		return nil, err
	}
	providers, err := queryRows(tx, `SELECT provider, COUNT(*) AS call_count, SUM(tokens_in) AS tokens_in,
SUM(tokens_out) AS tokens_out, SUM(cost) AS cost FROM tool_calls WHERE provider IS NOT NULL
GROUP BY provider ORDER BY call_count DESC, provider`)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"total_tool_calls": total, "coverage": coverage, "totals": metrics,
		"latency_ms_distribution": distribution(latencies), "models": models, "providers": providers,
	}, nil
}

func extractFailureMetrics(tx *sql.Tx) (any, error) {
	statuses, err := queryOne(tx, `SELECT
COALESCE(SUM(CASE WHEN is_error = 1 THEN 1 ELSE 0 END), 0) AS error_calls,
COALESCE(SUM(CASE WHEN is_error = 0 THEN 1 ELSE 0 END), 0) AS successful_calls,
COALESCE(SUM(CASE WHEN is_error IS NULL THEN 1 ELSE 0 END), 0) AS unknown_status_calls FROM tool_calls`)
	if err != nil {
		return nil, err
	}
	known := asInt64(statuses["error_calls"]) + asInt64(statuses["successful_calls"])
	statuses["error_rate_pct_of_known"] = percentage(float64(asInt64(statuses["error_calls"])), float64(known))
	exitCodes, err := queryRows(tx, `SELECT exit_code, COUNT(*) AS call_count FROM tool_calls
WHERE exit_code IS NOT NULL GROUP BY exit_code ORDER BY call_count DESC, exit_code`)
	if err != nil {
		return nil, err
	}
	statuses["exit_codes"] = exitCodes
	return statuses, nil
}

const toolMetricsSQL = `SELECT tool, COUNT(*) AS call_count, COUNT(DISTINCT session_id) AS session_count,
COALESCE(SUM(length(CAST(input AS BLOB))), 0) AS input_bytes,
COALESCE(SUM(length(CAST(output AS BLOB))), 0) AS output_bytes,
SUM(tokens_in) AS tokens_in, SUM(tokens_out) AS tokens_out, SUM(cost) AS cost,
ROUND(AVG(latency_ms), 2) AS average_latency_ms,
SUM(CASE WHEN is_error = 1 THEN 1 ELSE 0 END) AS error_calls,
SUM(CASE WHEN is_error IS NULL THEN 1 ELSE 0 END) AS unknown_status_calls
FROM tool_calls GROUP BY tool ORDER BY call_count DESC, tool`

const agentMetricsSQL = `WITH session_counts AS (
SELECT agent, COUNT(*) AS session_count FROM sessions GROUP BY agent), call_counts AS (
SELECT s.agent, COUNT(tc.id) AS tool_call_count,
COALESCE(SUM(length(CAST(tc.output AS BLOB))), 0) AS output_bytes,
SUM(tc.tokens_in) AS tokens_in, SUM(tc.tokens_out) AS tokens_out, SUM(tc.cost) AS cost,
ROUND(AVG(tc.latency_ms), 2) AS average_latency_ms,
SUM(CASE WHEN tc.is_error = 1 THEN 1 ELSE 0 END) AS error_calls
FROM sessions s LEFT JOIN tool_calls tc ON tc.session_id = s.id GROUP BY s.agent)
SELECT sc.agent, sc.session_count, cc.tool_call_count, cc.output_bytes,
cc.tokens_in, cc.tokens_out, cc.cost, cc.average_latency_ms, cc.error_calls
FROM session_counts sc JOIN call_counts cc ON sc.agent IS cc.agent
ORDER BY cc.tool_call_count DESC, sc.agent`

type compressionRow struct {
	ID, SourceID, Pipeline, Timestamp, Tool, SessionID, Agent, StartedAt string
	OriginalBytes, CompressedBytes                                       int64
}

func extractCompression(tx *sql.Tx, totalCalls int64, includeIdentifiers bool) (any, error) {
	rows, err := queryRows(tx, `SELECT c.id, c.source_id, c.pipeline, c.timestamp,
tc.tool, tc.session_id, s.agent, s.started_at,
COALESCE(length(CAST(tc.output AS BLOB)), 0) AS original_bytes,
COALESCE(length(CAST(c.output AS BLOB)), 0) AS compressed_bytes
FROM compressed c JOIN tool_calls tc ON tc.id = c.source_id JOIN sessions s ON s.id = tc.session_id
ORDER BY c.timestamp, c.id`)
	if err != nil {
		return nil, err
	}
	artifacts := make([]compressionRow, 0, len(rows))
	for _, row := range rows {
		artifacts = append(artifacts, compressionRow{
			ID: asString(row["id"]), SourceID: asString(row["source_id"]), Pipeline: asString(row["pipeline"]),
			Timestamp: asString(row["timestamp"]), Tool: asString(row["tool"]), SessionID: asString(row["session_id"]),
			Agent: asString(row["agent"]), StartedAt: asString(row["started_at"]),
			OriginalBytes: asInt64(row["original_bytes"]), CompressedBytes: asInt64(row["compressed_bytes"]),
		})
	}
	sources, uniqueBytes := map[string]int{}, map[string]int64{}
	sessions := map[string]bool{}
	var original, compressed int64
	var savings, reductions []float64
	for _, row := range artifacts {
		sources[row.SourceID]++
		uniqueBytes[row.SourceID] = row.OriginalBytes
		sessions[row.SessionID] = true
		original += row.OriginalBytes
		compressed += row.CompressedBytes
		savings = append(savings, float64(row.OriginalBytes-row.CompressedBytes))
		if value := reduction(float64(row.OriginalBytes), float64(row.CompressedBytes)); value != nil {
			reductions = append(reductions, *value)
		}
	}
	var uniqueSourceBytes int64
	for _, value := range uniqueBytes {
		uniqueSourceBytes += value
	}
	recompressed, maximum, improved, passthrough, worsened := 0, 0, 0, 0, 0
	for _, count := range sources {
		if count > 1 {
			recompressed++
		}
		if count > maximum {
			maximum = count
		}
	}
	for _, value := range savings {
		if value > 0 {
			improved++
		} else if value == 0 {
			passthrough++
		} else {
			worsened++
		}
	}
	overview := map[string]any{
		"artifact_count": len(artifacts), "unique_source_count": len(sources), "recompressed_source_count": recompressed,
		"maximum_artifacts_per_source": maximum, "sessions_with_compression": len(sessions),
		"source_coverage_pct": percentage(float64(len(sources)), float64(totalCalls)),
		"unique_source_bytes": uniqueSourceBytes, "source_bytes_across_artifacts": original,
		"compressed_bytes": compressed, "bytes_saved": original - compressed,
		"reduction_pct":      reduction(float64(original), float64(compressed)),
		"improved_artifacts": improved, "passthrough_artifacts": passthrough, "worsened_artifacts": worsened,
		"bytes_saved_distribution": distribution(savings), "reduction_pct_distribution": distribution(reductions),
	}
	byTool := compressionGroups(artifacts, func(row compressionRow) string { return row.Tool }, "tool")
	byPipeline := compressionGroups(artifacts, func(row compressionRow) string { return row.Pipeline }, "pipeline")
	topSessions, err := compressionSessions(tx, artifacts, includeIdentifiers)
	if err != nil {
		return nil, err
	}
	return map[string]any{"overview": overview, "by_tool": byTool, "by_pipeline": byPipeline, "top_sessions": topSessions}, nil
}

func compressionGroups(rows []compressionRow, key func(compressionRow) string, keyName string) []any {
	groups := map[string][]compressionRow{}
	for _, row := range rows {
		groups[key(row)] = append(groups[key(row)], row)
	}
	result := make([]map[string]any, 0, len(groups))
	for groupKey, artifacts := range groups {
		item := compressionAggregate(artifacts)
		item[keyName] = groupKey
		if keyName == "pipeline" {
			item["label"] = pipelineLabel(groupKey)
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := asInt64(result[i]["artifact_count"]), asInt64(result[j]["artifact_count"])
		if left != right {
			return left > right
		}
		return fmt.Sprint(result[i][keyName]) < fmt.Sprint(result[j][keyName])
	})
	output := make([]any, len(result))
	for i := range result {
		output[i] = result[i]
	}
	return output
}

func compressionAggregate(rows []compressionRow) map[string]any {
	var original, compressed int64
	sources := map[string]bool{}
	var savings, reductions []float64
	improved, passthrough, worsened := 0, 0, 0
	for _, row := range rows {
		original += row.OriginalBytes
		compressed += row.CompressedBytes
		sources[row.SourceID] = true
		saved := row.OriginalBytes - row.CompressedBytes
		savings = append(savings, float64(saved))
		if saved > 0 {
			improved++
		} else if saved == 0 {
			passthrough++
		} else {
			worsened++
		}
		if value := reduction(float64(row.OriginalBytes), float64(row.CompressedBytes)); value != nil {
			reductions = append(reductions, *value)
		}
	}
	return map[string]any{
		"artifact_count": len(rows), "unique_source_count": len(sources),
		"source_bytes_across_artifacts": original, "compressed_bytes": compressed, "bytes_saved": original - compressed,
		"reduction_pct": reduction(float64(original), float64(compressed)), "improved_artifacts": improved,
		"passthrough_artifacts": passthrough, "worsened_artifacts": worsened,
		"bytes_saved_distribution": distribution(savings), "reduction_pct_distribution": distribution(reductions),
	}
}

func compressionSessions(tx *sql.Tx, rows []compressionRow, includeIdentifiers bool) ([]any, error) {
	groups := map[string][]compressionRow{}
	metadata := map[string]compressionRow{}
	for _, row := range rows {
		groups[row.SessionID] = append(groups[row.SessionID], row)
		metadata[row.SessionID] = row
	}
	stats, err := queryRows(tx, `SELECT session_id, COUNT(*) AS tool_call_count,
COALESCE(SUM(length(CAST(output AS BLOB))), 0) AS total_output_bytes FROM tool_calls GROUP BY session_id`)
	if err != nil {
		return nil, err
	}
	statsByID := map[string]map[string]any{}
	for _, row := range stats {
		id := asString(row["session_id"])
		delete(row, "session_id")
		statsByID[id] = row
	}
	type item struct {
		id  string
		row map[string]any
	}
	items := []item{}
	for id, artifacts := range groups {
		row := compressionAggregate(artifacts)
		row["agent"] = metadata[id].Agent
		row["started_at"] = metadata[id].StartedAt
		for key, value := range statsByID[id] {
			row[key] = value
		}
		items = append(items, item{id, row})
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := asInt64(items[i].row["artifact_count"]), asInt64(items[j].row["artifact_count"])
		if left != right {
			return left > right
		}
		return items[i].id < items[j].id
	})
	if len(items) > 20 {
		items = items[:20]
	}
	result := make([]any, 0, len(items))
	for index, value := range items {
		value.row["rank"] = index + 1
		if includeIdentifiers {
			value.row["session_id"] = value.id
		}
		result = append(result, value.row)
	}
	return result, nil
}

func extractSessionLifecycle(tx *sql.Tx, includeIdentifiers bool) (any, error) {
	rows, err := queryRows(tx, `SELECT s.id, s.tool_call_count AS recorded_tool_call_count,
COUNT(tc.id) AS actual_tool_call_count, s.started_at, s.ended_at, s.last_seen_at,
CASE WHEN julianday(s.ended_at) >= julianday(s.started_at)
THEN CAST((julianday(s.ended_at)-julianday(s.started_at))*86400000 AS INTEGER) END AS duration_ms
FROM sessions s LEFT JOIN tool_calls tc ON tc.session_id=s.id GROUP BY s.id ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	open, closed, zero, missingStart, missingSeen := 0, 0, 0, 0, 0
	var durations []float64
	mismatches := []any{}
	for _, row := range rows {
		if row["ended_at"] == nil || asString(row["ended_at"]) == "" {
			open++
		} else {
			closed++
		}
		if asInt64(row["actual_tool_call_count"]) == 0 {
			zero++
		}
		if row["started_at"] == nil || asString(row["started_at"]) == "" {
			missingStart++
		}
		if row["last_seen_at"] == nil || asString(row["last_seen_at"]) == "" {
			missingSeen++
		}
		if row["duration_ms"] != nil {
			durations = append(durations, asFloat(row["duration_ms"]))
		}
		if asInt64(row["recorded_tool_call_count"]) != asInt64(row["actual_tool_call_count"]) {
			mismatch := map[string]any{"recorded_tool_call_count": row["recorded_tool_call_count"], "actual_tool_call_count": row["actual_tool_call_count"]}
			if includeIdentifiers {
				mismatch["session_id"] = row["id"]
			}
			mismatches = append(mismatches, mismatch)
		}
	}
	return map[string]any{
		"open_sessions": open, "closed_sessions": closed, "zero_call_sessions": zero,
		"missing_started_at": missingStart, "missing_last_seen_at": missingSeen,
		"duration_ms_distribution": distribution(durations), "rollup_mismatch_count": len(mismatches), "rollup_mismatches": mismatches,
	}, nil
}

func extractRecordedStreams(tx *sql.Tx) (any, error) {
	overview, err := queryOne(tx, `SELECT COUNT(*) AS stream_count, COUNT(DISTINCT session_id) AS session_count,
COALESCE(SUM(length(CAST(stream_text AS BLOB))),0) AS stream_bytes,
MIN(captured_at) AS first_captured_at, MAX(captured_at) AS last_captured_at FROM recorded_streams`)
	if err != nil {
		return nil, err
	}
	formats, err := queryRows(tx, `SELECT stream_format, COUNT(*) AS stream_count,
COALESCE(SUM(length(CAST(stream_text AS BLOB))),0) AS stream_bytes FROM recorded_streams
GROUP BY stream_format ORDER BY stream_count DESC, stream_format`)
	if err != nil {
		return nil, err
	}
	overview["formats"] = formats
	return overview, nil
}

func extractDailyActivity(tx *sql.Tx) (any, error) {
	rows, err := queryRows(tx, dailySQL)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		row["compression_reduction_pct"] = reduction(asFloat(row["compression_source_bytes"]), asFloat(row["compressed_bytes"]))
	}
	return rows, nil
}

const dailySQL = `WITH dates AS (
SELECT DATE(started_at) AS date FROM sessions UNION SELECT DATE(timestamp) FROM tool_calls
UNION SELECT DATE(timestamp) FROM compressed UNION SELECT DATE(captured_at) FROM recorded_streams),
session_daily AS (SELECT DATE(started_at) AS date, COUNT(*) AS sessions FROM sessions WHERE DATE(started_at) IS NOT NULL GROUP BY DATE(started_at)),
call_daily AS (SELECT DATE(timestamp) AS date, COUNT(*) AS tool_calls,
COALESCE(SUM(length(CAST(output AS BLOB))),0) AS output_bytes, SUM(tokens_in) AS tokens_in,
SUM(tokens_out) AS tokens_out, SUM(cost) AS cost, SUM(CASE WHEN is_error=1 THEN 1 ELSE 0 END) AS error_calls
FROM tool_calls WHERE DATE(timestamp) IS NOT NULL GROUP BY DATE(timestamp)),
compression_daily AS (SELECT DATE(c.timestamp) AS date, COUNT(*) AS compression_artifacts,
COUNT(DISTINCT c.source_id) AS compressed_sources, COALESCE(SUM(length(CAST(tc.output AS BLOB))),0) AS source_bytes,
COALESCE(SUM(length(CAST(c.output AS BLOB))),0) AS compressed_bytes FROM compressed c JOIN tool_calls tc ON tc.id=c.source_id
WHERE DATE(c.timestamp) IS NOT NULL GROUP BY DATE(c.timestamp)),
stream_daily AS (SELECT DATE(captured_at) AS date, COUNT(*) AS recorded_streams,
COALESCE(SUM(length(CAST(stream_text AS BLOB))),0) AS stream_bytes FROM recorded_streams
WHERE DATE(captured_at) IS NOT NULL GROUP BY DATE(captured_at))
SELECT d.date, COALESCE(s.sessions,0) AS sessions, COALESCE(tc.tool_calls,0) AS tool_calls,
COALESCE(tc.output_bytes,0) AS output_bytes, tc.tokens_in, tc.tokens_out, tc.cost,
COALESCE(tc.error_calls,0) AS error_calls, COALESCE(c.compression_artifacts,0) AS compression_artifacts,
COALESCE(c.compressed_sources,0) AS compressed_sources, COALESCE(c.source_bytes,0) AS compression_source_bytes,
COALESCE(c.compressed_bytes,0) AS compressed_bytes, COALESCE(c.source_bytes,0)-COALESCE(c.compressed_bytes,0) AS bytes_saved,
COALESCE(rs.recorded_streams,0) AS recorded_streams, COALESCE(rs.stream_bytes,0) AS stream_bytes
FROM dates d LEFT JOIN session_daily s ON s.date=d.date LEFT JOIN call_daily tc ON tc.date=d.date
LEFT JOIN compression_daily c ON c.date=d.date LEFT JOIN stream_daily rs ON rs.date=d.date
WHERE d.date IS NOT NULL ORDER BY d.date`

func extractIntegrity(tx *sql.Tx) (any, error) {
	checks := map[string]any{}
	for table, column := range map[string]string{"tool_calls": "output", "compressed": "output", "recorded_streams": "stream_text"} {
		rows, err := tx.Query("SELECT content_hash, CAST(" + column + " AS BLOB) FROM " + table)
		if err != nil {
			return nil, err
		}
		counts := map[string]int{"row_count": 0, "checked_count": 0, "valid_count": 0, "mismatch_count": 0, "missing_hash_count": 0}
		for rows.Next() {
			var stored sql.NullString
			var content []byte
			if err := rows.Scan(&stored, &content); err != nil {
				rows.Close()
				return nil, err
			}
			counts["row_count"]++
			if !stored.Valid || stored.String == "" {
				counts["missing_hash_count"]++
				continue
			}
			counts["checked_count"]++
			digest := sha256.Sum256(content)
			if hex.EncodeToString(digest[:]) == stored.String {
				counts["valid_count"]++
			} else {
				counts["mismatch_count"]++
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		checks[table] = counts
	}
	return checks, nil
}

func distribution(values []float64) map[string]any {
	sort.Float64s(values)
	result := map[string]any{"count": len(values), "min": nil, "p50": nil, "p95": nil, "max": nil, "average": nil}
	if len(values) == 0 {
		return result
	}
	result["min"], result["max"] = values[0], values[len(values)-1]
	result["p50"], result["p95"] = percentile(values, .5), percentile(values, .95)
	total := 0.0
	for _, value := range values {
		total += value
	}
	result["average"] = round2(total / float64(len(values)))
	return result
}

func percentile(values []float64, fraction float64) float64 {
	position := float64(len(values)-1) * fraction
	lower, upper := int(math.Floor(position)), int(math.Ceil(position))
	if lower == upper {
		return values[lower]
	}
	weight := position - float64(lower)
	return round2(values[lower]*(1-weight) + values[upper]*weight)
}

func percentage(numerator, denominator float64) any {
	if denominator == 0 {
		return nil
	}
	return round2(100 * numerator / denominator)
}

func reduction(original, result float64) *float64 {
	if original == 0 {
		return nil
	}
	value := round2(100 * (original - result) / original)
	return &value
}
func round2(value float64) float64 { return math.Round(value*100) / 100 }

func pipelineLabel(value string) string {
	var stages []string
	if json.Unmarshal([]byte(value), &stages) == nil {
		if len(stages) == 0 {
			return "passthrough"
		}
		return strings.Join(stages, " → ")
	}
	if len(value) > 40 {
		return value[:40] + "..."
	}
	return value
}

func asInt64(value any) int64 {
	switch value := value.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func asFloat(value any) float64 {
	switch value := value.(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	case int:
		return float64(value)
	default:
		return 0
	}
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
