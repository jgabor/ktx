package benchcontract

import (
	"strings"
	"testing"
)

func TestReadEnvironmentRequiresBenchSession(t *testing.T) {
	for _, name := range []string{SessionEnv, IntervalEnv, ContractEnv, CollectorModeEnv, "KTX_ATTEMPT_SESSION_ID", "KTX_SESSION_ID"} {
		t.Setenv(name, "")
	}
	t.Setenv("KTX_ATTEMPT_SESSION_ID", "aaaaaaaaaaaaaaaa")

	if env, present, err := ReadEnvironment(); err != nil || present || env != (Environment{}) {
		t.Fatalf("old variable alone: env=%#v present=%t err=%v", env, present, err)
	}

	t.Setenv(IntervalEnv, "bbbbbbbbbbbbbbbb")
	t.Setenv(ContractEnv, ContractVersion)
	t.Setenv(CollectorModeEnv, CollectorRequired)
	if _, present, err := ReadEnvironment(); !present || err == nil || !strings.Contains(err.Error(), SessionEnv) {
		t.Fatalf("old variable with required collector settings: present=%t err=%v", present, err)
	}

	t.Setenv(SessionEnv, "aaaaaaaaaaaaaaaa")
	if env, present, err := ReadEnvironment(); err != nil || !present || env.SessionID != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("new variable: env=%#v present=%t err=%v", env, present, err)
	}
}

func TestReadEnvironmentRejectsWrapperSessionConflict(t *testing.T) {
	t.Setenv(SessionEnv, "aaaaaaaaaaaaaaaa")
	t.Setenv(IntervalEnv, "bbbbbbbbbbbbbbbb")
	t.Setenv(ContractEnv, ContractVersion)
	t.Setenv(CollectorModeEnv, CollectorRequired)
	t.Setenv("KTX_SESSION_ID", "wrapper")

	if _, present, err := ReadEnvironment(); !present || err == nil || !strings.Contains(err.Error(), SessionEnv+" conflicts with KTX_SESSION_ID") {
		t.Fatalf("present=%t err=%v", present, err)
	}
}
