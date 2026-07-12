package db

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jgabor/ktx/internal/identity"
)

type identityReplacement struct {
	source string
	target string
}

// migrateCodexIdentities replaces historical runtime-owned primary keys with
// canonical KTX identities. Its caller owns the transaction.
func migrateCodexIdentities(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT id, session_id, runtime FROM tool_calls")
	if err != nil {
		return fmt.Errorf("inspect historical tool-call identities: %w", err)
	}
	var replacements []identityReplacement
	targets := make(map[string]string)
	for rows.Next() {
		var source, sessionID string
		var runtime sql.NullString
		if err := rows.Scan(&source, &sessionID, &runtime); err != nil {
			rows.Close()
			return fmt.Errorf("inspect historical tool-call identity: %w", err)
		}
		if identity.IsCanonical(source) || !runtime.Valid || normalizeRuntime(runtime.String) != "codex" {
			continue
		}
		target, err := identity.Derive(runtime.String, sessionID, source)
		if err != nil {
			rows.Close()
			return fmt.Errorf("cannot migrate Codex tool-call identity %q: %w", source, err)
		}
		if prior, ok := targets[target]; ok {
			rows.Close()
			return fmt.Errorf("cannot migrate Codex tool-call identities %q and %q: both map to %q", prior, source, target)
		}
		targets[target] = source
		replacements = append(replacements, identityReplacement{source: source, target: target})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect historical tool-call identities: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close historical identity scan: %w", err)
	}
	if len(replacements) == 0 {
		return nil
	}

	for _, replacement := range replacements {
		var exists int
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM tool_calls WHERE id = ?)", replacement.target).Scan(&exists); err != nil {
			return fmt.Errorf("check canonical identity %q: %w", replacement.target, err)
		}
		if exists != 0 {
			return fmt.Errorf("cannot migrate Codex tool-call identity %q: canonical identity %q already exists", replacement.source, replacement.target)
		}
	}
	references, err := toolCallReferences(tx)
	if err != nil {
		return err
	}
	for _, replacement := range replacements {
		_, err := tx.Exec("INSERT INTO tool_calls (id, session_id, tool, input, output, content_hash, timestamp, event_type, tokens_in, tokens_out, cost, latency_ms, model, provider, exit_code, is_error, runtime) SELECT ?, session_id, tool, input, output, content_hash, timestamp, event_type, tokens_in, tokens_out, cost, latency_ms, model, provider, exit_code, is_error, runtime FROM tool_calls WHERE id = ?", replacement.target, replacement.source)
		if err != nil {
			return fmt.Errorf("create canonical identity %q for %q: %w", replacement.target, replacement.source, err)
		}
		for _, ref := range references {
			query := "UPDATE " + quoteIdentifier(ref.table) + " SET " + quoteIdentifier(ref.column) + " = ? WHERE " + quoteIdentifier(ref.column) + " = ?"
			if _, err := tx.Exec(query, replacement.target, replacement.source); err != nil {
				return fmt.Errorf("update %s.%s for Codex identity %q: %w", ref.table, ref.column, replacement.source, err)
			}
		}
		if _, err := tx.Exec("DELETE FROM tool_calls WHERE id = ?", replacement.source); err != nil {
			return fmt.Errorf("remove external Codex identity %q: %w", replacement.source, err)
		}
	}
	var table, parent string
	var rowID sql.NullInt64
	if err := tx.QueryRow("SELECT \"table\", rowid, parent FROM pragma_foreign_key_check LIMIT 1").Scan(&table, &rowID, &parent); err != sql.ErrNoRows {
		if err != nil {
			return fmt.Errorf("validate referential integrity after Codex identity migration: %w", err)
		}
		return fmt.Errorf("referential integrity failed after Codex identity migration: table %q row %v references %q", table, rowID, parent)
	}
	return nil
}

type toolCallReference struct {
	table  string
	column string
}

func toolCallReferences(tx *sql.Tx) ([]toolCallReference, error) {
	rows, err := tx.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("inspect tool-call dependents: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return nil, fmt.Errorf("inspect tool-call dependent table: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("inspect tool-call dependents: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close tool-call dependent scan: %w", err)
	}
	var references []toolCallReference
	for _, table := range tables {
		fkRows, err := tx.Query("PRAGMA foreign_key_list(" + quoteIdentifier(table) + ")")
		if err != nil {
			return nil, fmt.Errorf("inspect foreign keys for %q: %w", table, err)
		}
		for fkRows.Next() {
			var id, seq int
			var parentTable, from, to, onUpdate, onDelete, match string
			if err := fkRows.Scan(&id, &seq, &parentTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				fkRows.Close()
				return nil, fmt.Errorf("inspect foreign key for %q: %w", table, err)
			}
			if parentTable == "tool_calls" && to == "id" {
				references = append(references, toolCallReference{table: table, column: from})
			}
		}
		if err := fkRows.Err(); err != nil {
			fkRows.Close()
			return nil, fmt.Errorf("inspect foreign keys for %q: %w", table, err)
		}
		if err := fkRows.Close(); err != nil {
			return nil, fmt.Errorf("close foreign-key scan for %q: %w", table, err)
		}
	}
	return references, nil
}

func normalizeRuntime(runtime string) string {
	runtime = strings.Trim(runtime, "\x09\x0a\x0b\x0c\x0d\x20")
	b := []byte(runtime)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func quoteIdentifier(identifier string) string {
	return "\"" + strings.ReplaceAll(identifier, "\"", "\"\"") + "\""
}
