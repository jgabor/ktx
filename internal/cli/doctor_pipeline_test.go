package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func doctorConfigRoot(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("KTX_DB", "")
	for _, pair := range [][2]string{{"HOME", "home"}, {"XDG_DATA_HOME", "data"}, {"XDG_CONFIG_HOME", "config"}, {"XDG_CACHE_HOME", "cache"}, {"XDG_STATE_HOME", "state"}} {
		t.Setenv(pair[0], filepath.Join(root, pair[1]))
	}
	return root, filepath.Join(root, "config", "ktx", "ktx.toml")
}

func writeDoctorConfig(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Include directory entries and regular-file bytes, not just the database.
func doctorSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		result[path] = entry.Type().String()
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] += string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDoctorPipelineConfigStates(t *testing.T) {
	for _, tc := range []struct {
		name, text, detail            string
		absent, inaccessible, invalid bool
	}{
		{name: "absent", absent: true, detail: "absent; using passthrough defaults"},
		{name: "empty", detail: "readable and valid"},
		{name: "empty pipeline", text: "[pipeline.default]\nstages=[]", detail: "readable and valid"},
		{name: "independent logs", text: "[logs]\nlevel=42", detail: "readable and valid"},
		{name: "syntax", text: "[", invalid: true, detail: "invalid"},
		{name: "unknown key", text: "typo=true", invalid: true, detail: "unknown setting"},
		{name: "timeout", text: "[pipeline.default]\ntimeout='0s'", invalid: true, detail: "timeout"},
		{name: "plugin command required", text: "[[plugin]]\nname='test'", invalid: true, detail: "command"},
		{name: "plugin name required", text: "[[plugin]]\ncommand='unused'", invalid: true, detail: "name"},
		{name: "missing profile", text: "[[route]]\ntool='bash'", invalid: true, detail: "profile"},
		{name: "unknown profile", text: "[[route]]\nprofile='missing'", invalid: true, detail: "profile"},
		{name: "exit status", text: "[pipeline.default]\n[[route]]\nprofile='default'\nexit_status='SUCCESS'", invalid: true, detail: "exit_status"},
		{name: "directory", inaccessible: true, detail: "inaccessible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := doctorConfigRoot(t)
			if tc.inaccessible {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if !tc.absent {
				writeDoctorConfig(t, path, tc.text)
			}
			before := doctorSnapshot(t, root)
			var out, stderr bytes.Buffer
			if code := runDoctorWith(nil, Options{Format: FormatJSON}, &out, &stderr); code != ExitInternal || stderr.Len() != 0 {
				t.Fatalf("missing DB: code=%d stderr=%s", code, &stderr)
			}
			report := decodeDoctorReport(t, out.Bytes())
			status := doctorStatusOK
			if tc.invalid || tc.inaccessible {
				status = doctorStatusError
			}
			assertDoctorCheck(t, report, "pipeline_config", status, tc.detail)
			assertDoctorCheck(t, report, "pipeline_config", status, path)
			assertDoctorCheck(t, report, "database_exists", doctorStatusError, "does not exist")
			if !tc.invalid && !tc.inaccessible {
				assertDoctorCheck(t, report, "pipeline_fallback", doctorStatusOK, "[] (passthrough)")
			}
			if !reflect.DeepEqual(before, doctorSnapshot(t, root)) {
				t.Fatal("doctor changed filesystem")
			}
		})
	}
}

func TestDoctorPipelineResolutionReadOnly(t *testing.T) {
	root, path := doctorConfigRoot(t)
	dbPath := filepath.Join(root, "seeded.db")
	t.Setenv("KTX_DB", dbPath)
	seedDoctorDatabase(t, dbPath, nil)
	sentinel := filepath.Join(root, "sentinel")
	command := filepath.Join(root, "must-not-run")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ntouch '"+sentinel+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeDoctorConfig(t, path, fmt.Sprintf(`
[pipeline.zeta]
stages = ["head:abc", "unknown", "grep:[", "strip-ansi:ignored", "keep-ends:2:3", "replace:a::b", "external:a:b"]
[pipeline.default]
stages = ["truncate:12", "head", "token-budget:bad"]
[pipeline.alpha]
stages = []
[[route]]
tool = "bash"
command_prefix = "git "
subcommand = "status"
exit_status = "success"
profile = "zeta"
[[route]]
tool = "bash"
exit_status = "failure"
profile = "alpha"
[[route]]
profile = "default"
[[plugin]]
name = "head"
command = %q
[[plugin]]
name = "head"
command = %q
[[plugin]]
name = "external"
command = %q
`, command, command, command))
	before := doctorSnapshot(t, root)
	var original []byte
	for i := 0; i < 3; i++ {
		var out, stderr bytes.Buffer
		code := runDoctorWith(nil, Options{Format: FormatJSON}, &out, &stderr)
		if code != ExitOK || stderr.Len() != 0 {
			t.Fatalf("code=%d stderr=%s", code, &stderr)
		}
		if i == 0 {
			original = bytes.Clone(out.Bytes())
		} else if !bytes.Equal(original, out.Bytes()) {
			t.Fatal("nondeterministic JSON")
		}
	}
	report := decodeDoctorReport(t, original)
	if report.Status != doctorStatusDegraded {
		t.Fatalf("status=%s", report.Status)
	}
	var routes, profiles []string
	for _, check := range report.Checks {
		switch check.Name {
		case "pipeline_route":
			routes = append(routes, check.Detail)
		case "pipeline_profile":
			profiles = append(profiles, check.Detail)
		}
		if strings.Contains(check.Detail, command) {
			t.Fatal("plugin command exposed")
		}
	}
	wantRoutes := []string{
		`route 1 (first match wins): tool="bash" command_prefix="git " subcommand="status" exit_status="success" -> profile="zeta" stages=["head:abc" "unknown" "grep:[" "strip-ansi:ignored" "keep-ends:2:3" "replace:a::b" "external:a:b"]`,
		`route 2 (first match wins): tool="bash" command_prefix="" subcommand="" exit_status="failure" -> profile="alpha" stages=[] (passthrough)`,
		`route 3 (first match wins): tool="" command_prefix="" subcommand="" exit_status="any" -> profile="default" stages=["truncate:12" "head" "token-budget:bad"]`,
	}
	if !reflect.DeepEqual(routes, wantRoutes) {
		t.Fatalf("routes=%q", routes)
	}
	if len(profiles) != 3 || !strings.Contains(profiles[0], `profile="alpha"`) || !strings.Contains(profiles[1], `profile="default"`) || !strings.Contains(profiles[2], `profile="zeta"`) {
		t.Fatalf("profiles=%q", profiles)
	}
	assertDoctorCheck(t, report, "pipeline_fallback", doctorStatusOK, `stages=["truncate:12" "head" "token-budget:bad"]`)
	want := map[string]string{
		`token="head:abc"`:           "resolves to plugin 2",
		`token="unknown"`:            "unknown stage; skipped at runtime",
		`token="grep:["`:             "invalid arguments:",
		`token="strip-ansi:ignored"`: "resolves to built-in",
		`token="keep-ends:2:3"`:      "resolves to built-in",
		`token="replace:a::b"`:       "resolves to built-in",
		`token="external:a:b"`:       "resolves to plugin 3",
		`token="token-budget:bad"`:   "invalid arguments:",
		`plugin 1 name="head"`:       "effective winner: plugin 2",
		`plugin 2 name="head"`:       "duplicate name of plugin 1",
	}
	for key, value := range want {
		found := false
		for _, check := range report.Checks {
			if strings.Contains(check.Detail, key) && strings.Contains(check.Detail, value) {
				found = true
				warning := strings.Contains(value, "invalid arguments") || strings.Contains(value, "unknown stage") || strings.HasPrefix(key, "plugin")
				if (check.Status == doctorStatusWarning) != warning {
					t.Errorf("unexpected status: %+v", check)
				}
			}
		}
		if !found {
			t.Errorf("missing %s: %s", key, value)
		}
	}
	var text, expected, stderr bytes.Buffer
	printDoctorText(&expected, report)
	if code := runDoctorWith(nil, Options{Format: FormatText}, &text, &stderr); code != ExitOK || stderr.Len() != 0 || text.String() != expected.String() {
		t.Fatalf("text mismatch: code=%d stderr=%s text=%s", code, &stderr, &text)
	}
	if !reflect.DeepEqual(before, doctorSnapshot(t, root)) {
		t.Fatal("doctor executed a command or changed filesystem/database bytes")
	}
}

func TestDoctorInvalidConfigStatusAndReadinessPrecedence(t *testing.T) {
	root, path := doctorConfigRoot(t)
	dbPath := filepath.Join(root, "seeded.db")
	t.Setenv("KTX_DB", dbPath)
	seedDoctorDatabase(t, dbPath, nil)
	writeDoctorConfig(t, path, "unknown=true")
	var out, stderr bytes.Buffer
	if code := runDoctorWith(nil, Options{Format: FormatJSON}, &out, &stderr); code != ExitInternal || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	report := decodeDoctorReport(t, out.Bytes())
	if report.Status != doctorStatusError {
		t.Fatalf("status=%s", report.Status)
	}
	assertDoctorCheck(t, report, "pipeline_config", doctorStatusError, "invalid")
	assertDoctorCheck(t, report, "database_openable", doctorStatusOK, "read-only")
	d, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB().Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	before := doctorSnapshot(t, root)
	out.Reset()
	stderr.Reset()
	if code := runDoctorWith(nil, Options{Format: FormatJSON}, &out, &stderr); code != ExitSchemaIncompatible || out.Len() != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &out, &stderr)
	}
	var envelope map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("not a single error envelope: %v: %s", err, &stderr)
	}
	if !strings.Contains(stderr.String(), `"code":"schema-incompatible"`) || strings.Contains(stderr.String(), "pipeline_config") {
		t.Fatalf("wrong failure: %s", &stderr)
	}
	if !reflect.DeepEqual(before, doctorSnapshot(t, root)) {
		t.Fatal("failure changed filesystem")
	}
}

func TestDoctorBuiltinArgumentResolution(t *testing.T) {
	_, path := doctorConfigRoot(t)
	for _, tc := range []struct {
		token string
		valid bool
	}{
		{"head", true},
		{"head:3", true},
		{"head:abc", false},
		{"truncate", true},
		{"truncate:bad", false},
		{"keep-ends:2:3", true},
		{"keep-ends:bad", false},
		{"token-budget:12", true},
		{"token-budget:bad", false},
		{"grep:a:b", true},
		{"grep:[", false},
		{"grep-v:abc", true},
		{"grep-v:[", false},
		{"replace:a::b", true},
		{"replace:missing-delimiter", false},
		{"strip-ansi:ignored", true},
	} {
		t.Run(tc.token, func(t *testing.T) {
			writeDoctorConfig(t, path, fmt.Sprintf("[pipeline.default]\nstages=[%q]", tc.token))
			checks := doctorPipelineChecks()
			last := checks[len(checks)-1]
			want := doctorStatusWarning
			if tc.valid {
				want = doctorStatusOK
			}
			if last.Name != "pipeline_stage" || last.Status != want {
				t.Fatalf("finding=%+v", last)
			}
		})
	}
}

func TestDoctorConfigAccessFailures(t *testing.T) {
	for _, kind := range []string{"discovery", "read"} {
		t.Run(kind, func(t *testing.T) {
			root, path := doctorConfigRoot(t)
			if kind == "discovery" {
				// A non-directory path component must not masquerade as absent config.
				if err := os.WriteFile(filepath.Join(root, "config"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeDoctorConfig(t, path, "")
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
				if _, err := os.ReadFile(path); err == nil {
					t.Skip("process can read mode-000 files")
				}
			}
			checks := doctorPipelineChecks()
			if len(checks) != 1 || checks[0].Status != doctorStatusError || !strings.Contains(checks[0].Detail, "inaccessible") {
				t.Fatalf("checks=%+v", checks)
			}
		})
	}
}

func TestDoctorInvalidConfigDeterministic(t *testing.T) {
	_, path := doctorConfigRoot(t)
	writeDoctorConfig(t, path, "[pipeline.zeta]\ntimeout='bad'\n[pipeline.alpha]\ntimeout='0s'")
	first := doctorPipelineChecks()
	for i := 0; i < 20; i++ {
		if got := doctorPipelineChecks(); !reflect.DeepEqual(first, got) {
			t.Fatalf("nondeterministic findings: %v != %v", first, got)
		}
	}
	if len(first) != 1 || !strings.Contains(first[0].Detail, "pipeline.alpha") {
		t.Fatalf("checks=%+v", first)
	}
}
