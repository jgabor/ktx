//go:build mage

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHermeticVerificationEnvironment(t *testing.T) {
	if os.Getenv(hermeticMarkerEnv) != "1" {
		t.Fatal("standard test target did not enter the hermetic boundary")
	}
	if _, present := os.LookupEnv("KTX_DB"); present {
		t.Fatal("KTX_DB is present inside the hermetic boundary")
	}
	root := os.Getenv(hermeticRootEnv)
	if root == "" {
		t.Fatal("disposable root is not declared")
	}
	for _, key := range applicationRootVariables {
		value := os.Getenv(key)
		if value == "" || filepath.Dir(value) != root {
			t.Errorf("%s=%q is not a direct child of disposable root %q", key, value, root)
		}
	}
	for _, key := range toolCacheVariables {
		value := os.Getenv(key)
		if value == "" {
			t.Errorf("%s is empty", key)
			continue
		}
		if pathWithin(root, value) {
			t.Errorf("%s=%q is inside disposable root %q", key, value, root)
		}
	}
	if audit := os.Getenv(databaseAuditEnv); audit == "" || filepath.Dir(audit) != root {
		t.Errorf("database audit path %q is not owned by disposable root %q", audit, root)
	}
	if capability := os.Getenv(hermeticCapabilityEnv); capability == "" {
		t.Error("reentry capability path is not declared")
	} else if _, err := os.Stat(capability); !os.IsNotExist(err) {
		t.Errorf("single-use reentry capability was not consumed: %v", err)
	}
}

func TestHermeticTargetsUseSingleBoundary(t *testing.T) {
	if got := os.Getenv(hermeticBoundaryIDEnv); got == "" {
		t.Fatal("hermetic boundary has no invocation identifier")
	}
}

func TestHermeticReentryRejectsForgedMarkerBeforeBody(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "body-ran")
	t.Setenv(hermeticMarkerEnv, "1")
	t.Setenv("KTX_DB", filepath.Join(t.TempDir(), "inert.db"))
	err := hermeticTarget("test", func() error { return os.WriteFile(sentinel, []byte("ran"), 0o600) })
	if err == nil || !strings.Contains(err.Error(), "KTX_DB") {
		t.Fatalf("forged reentry error = %v", err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("target body ran before reentry rejection: %v", statErr)
	}
}

func TestHermeticReentryValidationFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, map[string]string)
	}{
		{name: "missing boundary id", mutate: func(t *testing.T, env map[string]string) { env[hermeticBoundaryIDEnv] = "" }},
		{name: "malformed boundary id", mutate: func(t *testing.T, env map[string]string) { env[hermeticBoundaryIDEnv] = "not-hex" }},
		{name: "missing root", mutate: func(t *testing.T, env map[string]string) {
			env[hermeticRootEnv] = filepath.Join(t.TempDir(), "missing")
		}},
		{name: "duplicate app directories", mutate: func(t *testing.T, env map[string]string) { env["XDG_STATE_HOME"] = env["XDG_CACHE_HOME"] }},
		{name: "app directory outside root", mutate: func(t *testing.T, env map[string]string) { env["XDG_DATA_HOME"] = t.TempDir() }},
		{name: "audit outside root", mutate: func(t *testing.T, env map[string]string) {
			env[databaseAuditEnv] = filepath.Join(t.TempDir(), "audit.jsonl")
		}},
		{name: "malformed forbidden paths", mutate: func(t *testing.T, env map[string]string) { env[forbiddenDBPathsEnv] = "{" }},
		{name: "oversized forbidden paths", mutate: func(t *testing.T, env map[string]string) {
			env[forbiddenDBPathsEnv] = `[` + strings.Repeat(`"x",`, maxForbiddenDatabasePaths) + `"x"]`
		}},
		{name: "empty cache", mutate: func(t *testing.T, env map[string]string) { env["GOCACHE"] = "" }},
		{name: "cache inside root", mutate: func(t *testing.T, env map[string]string) {
			env["GOCACHE"] = filepath.Join(env[hermeticRootEnv], "tool-cache")
		}},
		{name: "missing capability", mutate: func(t *testing.T, env map[string]string) {
			env[hermeticCapabilityEnv] = filepath.Join(env[hermeticRootEnv], "missing-capability")
		}},
		{name: "wrong nonce", mutate: func(t *testing.T, env map[string]string) {
			env[hermeticNonceEnv] = strings.Repeat("0", boundaryNonceHexLength)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validReentryEnvironment(t)
			test.mutate(t, env)
			setTestEnvironment(t, env)
			ran := false
			if err := hermeticTarget("test", func() error { ran = true; return nil }); err == nil {
				t.Fatal("invalid reentry succeeded")
			}
			if ran {
				t.Fatal("target body ran for invalid reentry")
			}
		})
	}
}

func TestHermeticValidParentReentrySucceedsOnce(t *testing.T) {
	env := validReentryEnvironment(t)
	setTestEnvironment(t, env)
	runs := 0
	if err := hermeticTarget("test", func() error { runs++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := hermeticTarget("test", func() error { runs++; return nil }); err == nil {
		t.Fatal("consumed parent capability was accepted twice")
	}
	if runs != 1 {
		t.Fatalf("target body runs = %d, want 1", runs)
	}
}

func TestOuterVerificationEnvironmentStripsIncomingDatabase(t *testing.T) {
	environment := verificationEnvironment([]string{"KTX_DB=" + filepath.Join(t.TempDir(), "inert.db"), "KEEP=value"}, map[string]string{"HOME": t.TempDir()})
	for _, entry := range environment {
		if strings.HasPrefix(entry, "KTX_DB=") {
			t.Fatalf("outer environment retained %q", entry)
		}
	}
}

func validReentryEnvironment(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	env := map[string]string{
		hermeticMarkerEnv: "1", hermeticRootEnv: root, hermeticBoundaryIDEnv: "0123456789abcdef",
		databaseAuditEnv: filepath.Join(root, "database-access.jsonl"), forbiddenDBPathsEnv: "[]",
	}
	for index, name := range applicationRootVariables {
		path := filepath.Join(root, strings.ToLower(strings.TrimPrefix(name, "XDG_"))+string(rune('a'+index)))
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		env[name] = path
	}
	for _, name := range toolCacheVariables {
		env[name] = filepath.Join(t.TempDir(), strings.ToLower(name))
	}
	capabilityPath, nonce, err := createReentryCapability(root, env[hermeticBoundaryIDEnv])
	if err != nil {
		t.Fatal(err)
	}
	env[hermeticCapabilityEnv], env[hermeticNonceEnv] = capabilityPath, nonce
	return env
}

func setTestEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	t.Setenv("KTX_DB", "")
	if err := os.Unsetenv("KTX_DB"); err != nil {
		t.Fatal(err)
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	for _, name := range []string{hermeticMarkerEnv, hermeticRootEnv, hermeticBoundaryIDEnv, hermeticCapabilityEnv, hermeticNonceEnv, databaseAuditEnv, forbiddenDBPathsEnv} {
		if _, exists := values[name]; !exists {
			t.Setenv(name, "")
		}
	}
}
