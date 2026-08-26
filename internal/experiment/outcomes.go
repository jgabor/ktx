package experiment

import (
	"crypto/sha256"
	"database/sql"
	"sort"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/canon"
)

type attemptOutcomes struct {
	Recovery map[string]any
	Repeats  map[string]any
}

func materializeAttemptOutcomes(database *sql.DB, sessionID string) *attemptOutcomes {
	recovery, ok := recoveryOutcome(database, sessionID)
	if !ok {
		return nil
	}
	repeats, ok := repeatOutcome(database, sessionID)
	if !ok {
		return nil
	}
	return &attemptOutcomes{Recovery: recovery, Repeats: repeats}
}

func recoveryOutcome(database *sql.DB, sessionID string) (map[string]any, bool) {
	var intervalID string
	requiredAuthority := database.QueryRow(`SELECT id FROM runtime_event_intervals WHERE session_id=? AND contract_version=?`, sessionID, benchcontract.ContractVersion).Scan(&intervalID) == nil
	rows, err := database.Query(`SELECT d.collection_id,d.source_id,d.session_id,d.delivered_at,d.authority,
		COALESCE(a.attempt_source_id,''),COALESCE(a.attempted_at,''),COALESCE(a.result_state,''),a.byte_exact,COALESCE(a.authority,''),
		COALESCE(tc.session_id,''),COALESCE(c.source_id,'')
		FROM recovery_cue_deliveries d
		LEFT JOIN recovery_attempts a ON a.collection_id=d.collection_id AND a.cue_source_id=d.source_id
		LEFT JOIN tool_calls tc ON tc.id=a.attempt_source_id
		LEFT JOIN compressed c ON c.id=d.artifact_id
		ORDER BY d.source_id,a.attempt_source_id`)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	cues := map[string]int{}
	attempts, succeeded, failed, exact, inexact := 0, 0, 0, 0, 0
	for rows.Next() {
		var collection, cue, owner, delivered, deliveryAuthority, attemptID, attempted, state, attemptAuthority, attemptOwner, artifactSource string
		var byteExact sql.NullBool
		if rows.Scan(&collection, &cue, &owner, &delivered, &deliveryAuthority, &attemptID, &attempted, &state, &byteExact, &attemptAuthority, &attemptOwner, &artifactSource) != nil ||
			owner != sessionID || deliveryAuthority != "opencode_adapter" || artifactSource != cue || requiredAuthority && collection != intervalID {
			return nil, false
		}
		deliveredAt, err := time.Parse(time.RFC3339Nano, delivered)
		if err != nil {
			return nil, false
		}
		if _, exists := cues[cue]; !exists {
			cues[cue] = 0
		}
		if attemptID == "" {
			continue
		}
		attemptedAt, err := time.Parse(time.RFC3339Nano, attempted)
		if err != nil || !attemptedAt.After(deliveredAt) || attemptOwner != sessionID || attemptAuthority != "opencode_hook" {
			return nil, false
		}
		attempts++
		cues[cue]++
		switch state {
		case "succeeded":
			if !byteExact.Valid {
				return nil, false
			}
			succeeded++
			if byteExact.Bool {
				exact++
			} else {
				inexact++
			}
		case "failed":
			if byteExact.Valid {
				return nil, false
			}
			failed++
		default:
			return nil, false
		}
	}
	if rows.Err() != nil {
		return nil, false
	}
	if len(cues) == 0 && !requiredAuthority {
		return map[string]any{
			"state": "unavailable", "reason": "cue_delivery_authority_unavailable",
			"cue_count": nil, "followed_cue_count": nil, "attempt_count": nil, "repeated_attempt_count": nil,
			"outcomes": nil, "successful_results": nil,
		}, true
	}
	followed, repeated := 0, 0
	for _, count := range cues {
		if count > 0 {
			followed++
			repeated += count - 1
		}
	}
	return map[string]any{
		"state": "measured", "reason": nil, "cue_count": len(cues), "followed_cue_count": followed,
		"attempt_count": attempts, "repeated_attempt_count": repeated,
		"outcomes":           map[string]any{"succeeded": succeeded, "failed": failed},
		"successful_results": map[string]any{"byte_exact": exact, "not_byte_exact": inexact},
	}, true
}

func repeatOutcome(database *sql.DB, sessionID string) (map[string]any, bool) {
	rows, err := database.Query(`SELECT tool,COALESCE(input,'') FROM tool_calls WHERE session_id=? ORDER BY COALESCE(timestamp_unix_nano,ktx_unix_nano(timestamp)),id`, sessionID)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	classes := map[[32]byte]int{}
	count := 0
	for rows.Next() {
		var tool, input string
		if rows.Scan(&tool, &input) != nil {
			return nil, false
		}
		normalized, ok := canon.Canonicalize([]byte(input))
		if !ok {
			normalized = []byte(input)
		}
		classes[sha256.Sum256(append(append([]byte(tool), 0), normalized...))]++
		count++
	}
	if rows.Err() != nil {
		return nil, false
	}
	repeated, equalityCounts := 0, []int{}
	for _, n := range classes {
		if n > 1 {
			repeated += n - 1
			equalityCounts = append(equalityCounts, n)
		}
	}
	sort.Ints(equalityCounts)
	return map[string]any{"state": "measured", "call_count": count, "unique_call_count": len(classes), "repeated_call_count": repeated, "equality_class_counts": equalityCounts}, true
}
