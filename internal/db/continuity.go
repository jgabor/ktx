package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const IngressProcessAuthority = "ingress_process"

var ErrContinuityInvalid = errors.New("db: invalid continuity evidence")

type CollectionInterval struct {
	ID, DatabaseTarget, OpenedAt string
	SupportedIngresses           []string
}

type IngressObservation struct {
	IntervalID, Ingress, InstanceID, State, ObservedAt, Authority string
}

type ContinuityGap struct {
	Ingress string `json:"ingress"`
	From    string `json:"from"`
	To      string `json:"to"`
	Reason  string `json:"reason"`
}

type ContinuityResult struct {
	Continuous bool
	Gaps       []ContinuityGap
}

func (d *DB) OpenCollectionInterval(ingresses []string, openedAt string) (*CollectionInterval, error) {
	var target string
	if err := d.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&target); err != nil || strings.TrimSpace(target) == "" {
		return nil, ErrContinuityInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, openedAt); err != nil {
		return nil, ErrContinuityInvalid
	}
	for _, ingress := range ingresses {
		if ingress = strings.TrimSpace(ingress); ingress != "hook" && ingress != "write" && ingress != "run" {
			return nil, ErrContinuityInvalid
		}
	}
	ingresses = normalizedIngresses(ingresses)
	if len(ingresses) == 0 {
		return nil, ErrContinuityInvalid
	}
	encoded, _ := json.Marshal(ingresses)
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := writeContext()
	defer cancel()
	err = d.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO collection_intervals (id,database_target,supported_ingresses,opened_at) VALUES (?,?,?,?)`, id, target, string(encoded), openedAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &CollectionInterval{ID: id, DatabaseTarget: target, SupportedIngresses: ingresses, OpenedAt: openedAt}, nil
}

// EnsureCollectionInterval creates a caller-named collection or verifies that
// the existing collection names the same database and ingress set.
func (d *DB) EnsureCollectionInterval(id string, ingresses []string, openedAt string) (*CollectionInterval, error) {
	if !IDPattern.MatchString(id) {
		return nil, ErrContinuityInvalid
	}
	var target string
	if err := d.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&target); err != nil || strings.TrimSpace(target) == "" {
		return nil, ErrContinuityInvalid
	}
	ingresses = normalizedIngresses(ingresses)
	if len(ingresses) == 0 {
		return nil, ErrContinuityInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, openedAt); err != nil {
		return nil, ErrContinuityInvalid
	}
	encoded, _ := json.Marshal(ingresses)
	ctx, cancel := writeContext()
	defer cancel()
	err := d.withWriteTx(ctx, func(tx *sql.Tx) error {
		var storedTarget, storedIngresses, storedOpened string
		err := tx.QueryRow(`SELECT database_target,supported_ingresses,opened_at FROM collection_intervals WHERE id=?`, id).Scan(&storedTarget, &storedIngresses, &storedOpened)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.Exec(`INSERT INTO collection_intervals (id,database_target,supported_ingresses,opened_at) VALUES (?,?,?,?)`, id, target, string(encoded), openedAt)
			return err
		}
		if err != nil || storedTarget != target || storedIngresses != string(encoded) {
			return ErrContinuityInvalid
		}
		openedAt = storedOpened
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &CollectionInterval{ID: id, DatabaseTarget: target, SupportedIngresses: ingresses, OpenedAt: openedAt}, nil
}

func (d *DB) ValidateCollectionInterval(id string, ingresses []string) error {
	if !IDPattern.MatchString(id) {
		return errors.Join(ErrContinuityInvalid, errors.New("invalid collection id"))
	}
	var target, storedTarget, storedIngresses string
	if err := d.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&target); err != nil || strings.TrimSpace(target) == "" {
		return errors.Join(ErrContinuityInvalid, errors.New("database target unavailable"))
	}
	encoded, _ := json.Marshal(normalizedIngresses(ingresses))
	if err := d.db.QueryRow(`SELECT database_target,supported_ingresses FROM collection_intervals WHERE id=?`, id).Scan(&storedTarget, &storedIngresses); err != nil {
		return errors.Join(ErrContinuityInvalid, errors.New("collection not found"))
	}
	if storedTarget != target || storedIngresses != string(encoded) {
		return errors.Join(ErrContinuityInvalid, errors.New("database target or ingress differs"))
	}
	return nil
}

func normalizedIngresses(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if (value == "hook" || value == "write" || value == "run") && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (d *DB) RetainIngressObservation(o IngressObservation) error {
	if o.Authority != IngressProcessAuthority || (o.State != "started" && o.State != "stopped" && o.State != "interrupted") {
		return ErrContinuityInvalid
	}
	for _, value := range []string{o.IntervalID, o.Ingress, o.InstanceID, o.ObservedAt} {
		if strings.TrimSpace(value) == "" {
			return ErrContinuityInvalid
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, o.ObservedAt); err != nil {
		return ErrContinuityInvalid
	}
	ctx, cancel := writeContext()
	defer cancel()
	return d.withWriteTx(ctx, func(tx *sql.Tx) error {
		var encoded string
		if err := tx.QueryRow(`SELECT supported_ingresses FROM collection_intervals WHERE id = ?`, o.IntervalID).Scan(&encoded); err != nil {
			return err
		}
		var supported []string
		if json.Unmarshal([]byte(encoded), &supported) != nil || !contains(supported, o.Ingress) {
			return ErrContinuityInvalid
		}
		_, err := tx.Exec(`INSERT INTO ingress_continuity (interval_id,ingress,instance_id,state,observed_at,authority) VALUES (?,?,?,?,?,?)`, o.IntervalID, o.Ingress, o.InstanceID, o.State, o.ObservedAt, o.Authority)
		return err
	})
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// EvaluateContinuity treats absence and ambiguity as gaps. A selected ingress
// covers a span only through one or more non-overlapping started/stopped pairs.
func (d *DB) EvaluateContinuity(intervalID string, from, to time.Time) (ContinuityResult, error) {
	result := ContinuityResult{}
	var opened, encoded string
	if err := d.db.QueryRow(`SELECT opened_at,supported_ingresses FROM collection_intervals WHERE id=?`, intervalID).Scan(&opened, &encoded); err != nil {
		return result, err
	}
	var ingresses []string
	if err := json.Unmarshal([]byte(encoded), &ingresses); err != nil {
		return result, err
	}
	deployed, err := time.Parse(time.RFC3339Nano, opened)
	if err != nil || !from.Before(to) {
		return result, ErrContinuityInvalid
	}
	if from.Before(deployed) {
		result.Gaps = append(result.Gaps, ContinuityGap{From: from.Format(time.RFC3339Nano), To: minTime(to, deployed).Format(time.RFC3339Nano), Reason: "pre_deployment_unknown"})
	}
	for _, ingress := range ingresses {
		gaps, err := d.ingressGaps(intervalID, ingress, maxTime(from, deployed), to)
		if err != nil {
			return result, err
		}
		result.Gaps = append(result.Gaps, gaps...)
	}
	result.Continuous = len(result.Gaps) == 0 && !time.Now().UTC().Before(to)
	if now := time.Now().UTC(); now.Before(to) {
		result.Gaps = append(result.Gaps, ContinuityGap{From: maxTime(now, from).Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano), Reason: "interval_not_elapsed"})
	}
	return result, nil
}

func (d *DB) ingressGaps(intervalID, ingress string, from, to time.Time) ([]ContinuityGap, error) {
	if !from.Before(to) {
		return nil, nil
	}
	rows, err := d.db.Query(`SELECT instance_id,state,observed_at FROM ingress_continuity WHERE interval_id=? AND ingress=? ORDER BY rowid`, intervalID, ingress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type event struct {
		instance, state string
		at              time.Time
	}
	var events []event
	for rows.Next() {
		var e event
		var at string
		if err := rows.Scan(&e.instance, &e.state, &at); err != nil {
			return nil, err
		}
		e.at, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Compare instants, retaining append order when timestamp spellings differ
	// but denote the same instant. Stored timestamp evidence stays unchanged.
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	active := ""
	var activeAt time.Time
	covered := from
	var gaps []ContinuityGap
	for _, e := range events {
		if e.state == "started" {
			if !e.at.Before(to) {
				break
			}
			if active != "" {
				gaps = append(gaps, ContinuityGap{Ingress: ingress, From: maxTime(from, e.at).Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano), Reason: "conflicting_start"})
				return gaps, nil
			}
			active = e.instance
			activeAt = e.at
			gapFrom, gapTo := maxTime(covered, from), minTime(e.at, to)
			if gapFrom.Before(gapTo) {
				gaps = append(gaps, ContinuityGap{Ingress: ingress, From: gapFrom.Format(time.RFC3339Nano), To: gapTo.Format(time.RFC3339Nano), Reason: "missing"})
			}
		}
		if e.state == "interrupted" && active == e.instance {
			if !e.at.Before(to) {
				return gaps, nil
			}
			gaps = append(gaps, ContinuityGap{Ingress: ingress, From: maxTime(e.at, from).Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano), Reason: "interrupted"})
			return gaps, nil
		}
		if e.state == "stopped" {
			if active != e.instance {
				if !e.at.Before(to) {
					break
				}
				gaps = append(gaps, ContinuityGap{Ingress: ingress, From: maxTime(e.at, from).Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano), Reason: "conflicting_stop"})
				return gaps, nil
			}
			if !e.at.Before(to) {
				return gaps, nil
			}
			covered = e.at
			active = ""
		}
	}
	if active != "" {
		gaps = append(gaps, ContinuityGap{Ingress: ingress, From: maxTime(activeAt, from).Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano), Reason: "missing_closure"})
	} else if covered.Before(to) {
		gaps = append(gaps, ContinuityGap{Ingress: ingress, From: maxTime(covered, from).Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano), Reason: "missing"})
	}
	return gaps, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
