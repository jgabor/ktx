package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
