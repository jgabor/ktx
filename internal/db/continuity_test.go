package db

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestContinuityChronologicalOrdering(t *testing.T) {
	type observation struct{ instance, state, at string }
	const day = "2026-01-01T"
	tests := []struct {
		name, opened, from, to, reason string
		events                         []observation
	}{
		{name: "offset", events: []observation{{"a", "started", "10:00:00+02:00"}, {"a", "stopped", "09:00:00Z"}}},
		{name: "reverse append", events: []observation{{"a", "stopped", "09:00:00Z"}, {"a", "started", "10:00:00+02:00"}}},
		{name: "whole UTC", events: []observation{{"a", "started", "08:00:00Z"}, {"a", "stopped", "09:00:00Z"}}},
		{name: "fraction", from: "08:00:00Z", to: "08:00:00.5Z", events: []observation{{"a", "started", "08:00:00Z"}, {"a", "stopped", "08:00:00.5Z"}}},
		{name: "fraction precision", from: "08:00:00.1Z", to: "08:00:00.11Z", events: []observation{{"a", "started", "08:00:00.1Z"}, {"a", "stopped", "08:00:00.11Z"}}},
		{name: "offset tie restart", events: []observation{{"a", "started", "08:00:00Z"}, {"a", "stopped", "10:30:00+02:00"}, {"b", "started", "08:30:00Z"}, {"b", "stopped", "09:00:00Z"}}},
		{name: "fraction tie restart", events: []observation{{"a", "started", "08:00:00Z"}, {"a", "stopped", "08:30:00Z"}, {"b", "started", "08:30:00.000Z"}, {"b", "stopped", "09:00:00Z"}}},
		{name: "identical tie restart", events: []observation{{"a", "started", "08:00:00Z"}, {"a", "stopped", "08:30:00Z"}, {"b", "started", "08:30:00Z"}, {"b", "stopped", "09:00:00Z"}}},
		{name: "tie conflict preserves append order", reason: "conflicting_start", events: []observation{{"a", "started", "08:00:00Z"}, {"b", "started", "10:30:00+02:00"}, {"a", "stopped", "08:30:00Z"}, {"b", "stopped", "09:00:00Z"}}},
		{name: "true gap", reason: "missing", events: []observation{{"a", "started", "10:00:00+02:00"}, {"a", "stopped", "08:20:00Z"}, {"b", "started", "10:30:00+02:00"}, {"b", "stopped", "09:00:00Z"}}},
		{name: "true overlap", reason: "conflicting_start", events: []observation{{"a", "started", "10:00:00+02:00"}, {"b", "started", "08:20:00Z"}, {"a", "stopped", "10:30:00+02:00"}, {"b", "stopped", "09:00:00Z"}}},
		{name: "missing", reason: "missing"},
		{name: "missing closure", reason: "missing_closure", events: []observation{{"a", "started", "10:00:00+02:00"}}},
		{name: "conflicting stop", reason: "conflicting_stop", events: []observation{{"a", "started", "10:00:00+02:00"}, {"b", "stopped", "08:30:00Z"}}},
		{name: "interruption inside", reason: "interrupted", events: []observation{{"a", "started", "10:00:00+02:00"}, {"a", "interrupted", "08:30:00Z"}}},
		{name: "interruption at end", events: []observation{{"a", "started", "10:00:00+02:00"}, {"a", "interrupted", "09:00:00Z"}}},
		{name: "pre deployment", opened: "10:30:00+02:00", reason: "pre_deployment_unknown", events: []observation{{"a", "started", "08:30:00Z"}, {"a", "stopped", "09:00:00Z"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := Open(filepath.Join(t.TempDir(), "ktx.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if tt.opened == "" {
				tt.opened = "08:00:00Z"
			}
			if tt.from == "" {
				tt.from = "08:00:00Z"
			}
			if tt.to == "" {
				tt.to = "09:00:00Z"
			}
			interval, err := database.OpenCollectionInterval([]string{"run"}, day+tt.opened)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range tt.events {
				if err := database.RetainIngressObservation(IngressObservation{IntervalID: interval.ID, Ingress: "run", InstanceID: e.instance, State: e.state, ObservedAt: day + e.at, Authority: IngressProcessAuthority}); err != nil {
					t.Fatal(err)
				}
			}
			from, err := time.Parse(time.RFC3339Nano, day+tt.from)
			if err != nil {
				t.Fatal(err)
			}
			to, err := time.Parse(time.RFC3339Nano, day+tt.to)
			if err != nil {
				t.Fatal(err)
			}
			var first ContinuityResult
			for i := 0; i < 3; i++ {
				result, err := database.EvaluateContinuity(interval.ID, from, to)
				if err != nil || result.Continuous != (tt.reason == "") {
					t.Fatalf("continuity = %+v, %v; want reason %q", result, err, tt.reason)
				}
				if tt.reason != "" && (len(result.Gaps) != 1 || result.Gaps[0].Reason != tt.reason) {
					t.Fatalf("gaps = %+v; want %s", result.Gaps, tt.reason)
				}
				if i > 0 && !reflect.DeepEqual(first, result) {
					t.Fatalf("nondeterministic: %+v then %+v", first, result)
				}
				first = result
			}
			var opened string
			if err := database.db.QueryRow(`SELECT opened_at FROM collection_intervals WHERE id=?`, interval.ID).Scan(&opened); err != nil || opened != day+tt.opened {
				t.Fatalf("stored opened_at = %q, %v", opened, err)
			}
			rows, err := database.db.Query(`SELECT instance_id,state,observed_at FROM ingress_continuity WHERE interval_id=? ORDER BY rowid`, interval.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			i := 0
			for rows.Next() {
				var got observation
				if err := rows.Scan(&got.instance, &got.state, &got.at); err != nil {
					t.Fatal(err)
				}
				if i >= len(tt.events) || got != (observation{tt.events[i].instance, tt.events[i].state, day + tt.events[i].at}) {
					t.Fatalf("stored observation %d changed: %+v", i, got)
				}
				i++
			}
			if err := rows.Err(); err != nil || i != len(tt.events) {
				t.Fatalf("stored count = %d, %v", i, err)
			}
		})
	}
}

func TestContinuityMalformedTimestampDoesNotCover(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ktx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	from := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	interval, err := database.OpenCollectionInterval([]string{"run"}, from.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	o := IngressObservation{IntervalID: interval.ID, Ingress: "run", InstanceID: "a", State: "started", ObservedAt: "not-a-time", Authority: IngressProcessAuthority}
	if err := database.RetainIngressObservation(o); !errors.Is(err, ErrContinuityInvalid) {
		t.Fatalf("malformed write = %v", err)
	}
	if _, err := database.db.Exec(`INSERT INTO ingress_continuity (interval_id,ingress,instance_id,state,observed_at,authority) VALUES (?,?,?,?,?,?)`, o.IntervalID, o.Ingress, o.InstanceID, o.State, o.ObservedAt, o.Authority); err != nil {
		t.Fatal(err)
	}
	result, err := database.EvaluateContinuity(interval.ID, from, from.Add(time.Hour))
	if err == nil || result.Continuous {
		t.Fatalf("malformed evidence manufactured coverage: %+v, %v", result, err)
	}
}

func TestContinuityEvidence(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ktx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	interval, err := database.OpenCollectionInterval([]string{"hook", "write", "hook"}, base.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if len(interval.SupportedIngresses) != 2 || interval.SupportedIngresses[0] != "hook" || !filepath.IsAbs(interval.DatabaseTarget) {
		t.Fatalf("interval = %+v", interval)
	}

	observe := func(ingress, instance, state string, at time.Time) {
		t.Helper()
		if err := database.RetainIngressObservation(IngressObservation{IntervalID: interval.ID, Ingress: ingress, InstanceID: instance, State: state, ObservedAt: at.Format(time.RFC3339Nano), Authority: IngressProcessAuthority}); err != nil {
			t.Fatal(err)
		}
	}
	observe("hook", "h1", "started", base)
	observe("write", "w1", "started", base)
	observe("hook", "h1", "stopped", base.Add(3*time.Minute))
	observe("write", "w1", "stopped", base.Add(3*time.Minute))
	result, err := database.EvaluateContinuity(interval.ID, base.Add(time.Minute), base.Add(2*time.Minute))
	if err != nil || !result.Continuous || len(result.Gaps) != 0 {
		t.Fatalf("uninterrupted = %+v, %v", result, err)
	}

	observe("hook", "h2", "started", base.Add(3*time.Minute))
	observe("hook", "h2", "stopped", base.Add(5*time.Minute))
	observe("write", "w2", "started", base.Add(3*time.Minute))
	observe("write", "w2", "interrupted", base.Add(5*time.Minute))
	result, err = database.EvaluateContinuity(interval.ID, base.Add(time.Minute), base.Add(4*time.Minute))
	if err != nil || !result.Continuous {
		t.Fatalf("restart = %+v, %v", result, err)
	}
}

func TestContinuityExcludesPreWindowRestartGap(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ktx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	interval, err := database.OpenCollectionInterval([]string{"run"}, base.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []struct {
		instance string
		state    string
		minute   int
	}{
		{instance: "first", state: "started", minute: 0},
		{instance: "first", state: "stopped", minute: 1},
		{instance: "second", state: "started", minute: 2},
	} {
		if err := database.RetainIngressObservation(IngressObservation{
			IntervalID: interval.ID,
			Ingress:    "run",
			InstanceID: observation.instance,
			State:      observation.state,
			ObservedAt: base.Add(time.Duration(observation.minute) * time.Minute).Format(time.RFC3339Nano),
			Authority:  IngressProcessAuthority,
		}); err != nil {
			t.Fatal(err)
		}
	}

	result, err := database.EvaluateContinuity(interval.ID, base.Add(3*time.Minute), base.Add(4*time.Minute))
	if err != nil || result.Continuous || len(result.Gaps) != 1 || result.Gaps[0].Reason != "missing_closure" || result.Gaps[0].From != base.Add(3*time.Minute).Format(time.RFC3339Nano) || result.Gaps[0].To != base.Add(4*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("post-restart window = %+v, %v", result, err)
	}
}

func TestContinuityGapsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		events []struct {
			instance, state string
			minute          int
		}
		want string
	}{
		{name: "missing", want: "missing"},
		{name: "missing closure", events: []struct {
			instance, state string
			minute          int
		}{{"a", "started", 0}}, want: "missing_closure"},
		{name: "interrupted", events: []struct {
			instance, state string
			minute          int
		}{{"a", "started", 0}, {"a", "interrupted", 2}}, want: "interrupted"},
		{name: "conflicting", events: []struct {
			instance, state string
			minute          int
		}{{"a", "started", 0}, {"b", "started", 2}}, want: "conflicting_start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := Open(filepath.Join(t.TempDir(), "ktx.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			interval, err := database.OpenCollectionInterval([]string{"hook"}, base.Format(time.RFC3339Nano))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range tt.events {
				if err := database.RetainIngressObservation(IngressObservation{IntervalID: interval.ID, Ingress: "hook", InstanceID: e.instance, State: e.state, ObservedAt: base.Add(time.Duration(e.minute) * time.Minute).Format(time.RFC3339Nano), Authority: IngressProcessAuthority}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := database.EvaluateContinuity(interval.ID, base.Add(time.Minute), base.Add(4*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if result.Continuous || len(result.Gaps) == 0 || result.Gaps[0].Reason != tt.want {
				t.Fatalf("result = %+v, want %s gap", result, tt.want)
			}
			for _, gap := range result.Gaps {
				if gap.From < base.Add(time.Minute).Format(time.RFC3339Nano) || gap.To > base.Add(4*time.Minute).Format(time.RFC3339Nano) {
					t.Fatalf("gap outside requested span: %+v", gap)
				}
			}
		})
	}
}

func TestContinuityRejectsUnsupportedAuthorityAndPredeployment(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ktx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	interval, err := database.OpenCollectionInterval([]string{"hook"}, base.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	err = database.RetainIngressObservation(IngressObservation{IntervalID: interval.ID, Ingress: "hook", InstanceID: "x", State: "started", ObservedAt: base.Format(time.RFC3339Nano), Authority: "caller"})
	if err == nil {
		t.Fatal("caller authority accepted")
	}
	result, err := database.EvaluateContinuity(interval.ID, base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.Continuous || len(result.Gaps) == 0 || result.Gaps[0].Reason != "pre_deployment_unknown" {
		t.Fatalf("result = %+v", result)
	}
}

func TestContinuityEvaluationIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ktx.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	interval, err := database.OpenCollectionInterval([]string{"hook"}, base.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RetainIngressObservation(IngressObservation{IntervalID: interval.ID, Ingress: "hook", InstanceID: "x", State: "started", ObservedAt: base.Format(time.RFC3339Nano), Authority: IngressProcessAuthority}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := readOnly.EvaluateContinuity(interval.ID, base.Add(time.Minute), base.Add(2*time.Minute))
	if err != nil || result.Continuous || len(result.Gaps) != 1 || result.Gaps[0].Reason != "missing_closure" {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("read-only evaluation changed database")
	}
}
