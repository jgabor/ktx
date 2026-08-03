package main

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDirectLockExposesExactIdentityAndClaimBoundaries(t *testing.T) {
	lock, err := loadLock(trackedLock)
	if err != nil {
		t.Fatalf("load tracked lock: %v", err)
	}
	checks := map[string]any{
		"evaluator.version":              "0.6.5",
		"task.id":                        "3d-scan-calc",
		"task.official_identity":         nil,
		"runtime.version":                "1.18.5",
		"runtime.release_sha256":         "d61cefe7d59b26cab43d4758aa411b8e47a4228904eea362ca7d6c6f6d0f3ea8",
		"adapter.registry":               "codex-acp",
		"ktx.source_commit":              "54d5607d49282161ab470d3592b66d86d1afdd8a",
		"execution.model":                "openai/gpt-5.6-luna",
		"digests.current_metrics.sha256": "3b8d64b1e95bf6dcc85f16f91a40276dce97b960d9383d4e66d5e17c3fe35ca1",
		"claims.official_suite_identity": "not established",
		"claims.authorizes_execution":    false,
	}
	for path, expected := range checks {
		if actual := valueAt(t, lock, path); !reflectEqual(actual, expected) {
			t.Errorf("%s = %#v, want %#v", path, actual, expected)
		}
	}
	runtimeIdentity := valueAt(t, lock, "runtime").(map[string]any)
	if _, ok := runtimeIdentity["binary_sha256"]; ok {
		t.Error("runtime identity must not label the release digest as a binary digest")
	}
	if bytes.Contains(trackedLock, []byte(`"bindings"`)) || bytes.Contains(trackedLock, []byte(`"receipt_schema"`)) {
		t.Error("direct lock must not contain binding or receipt-schema machinery")
	}
}

func TestTrackedIdentityFixtures(t *testing.T) {
	tests := []struct {
		name       string
		fixture    string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "match",
			fixture:    "matching.json",
			wantCode:   0,
			wantStdout: "external identity lock: match\n",
		},
		{
			name:     "value mismatch",
			fixture:  "mismatching.json",
			wantCode: 2,
			wantStderr: "external identity lock: mismatch at runtime.version; expected \"1.18.5\". " +
				"Compare the runtime name, version, release asset, and release digest with the tracked lock.\n",
		},
		{
			name:     "malformed shape",
			fixture:  "malformed.json",
			wantCode: 65,
			wantStderr: "external identity lock: malformed observed identity: missing task.id. " +
				"Supply the exact fields listed in tools/bench/external-identity/lock.json.\n",
		},
		{
			name:       "duplicate member",
			fixture:    "duplicate.json",
			wantCode:   65,
			wantStderr: "external identity lock: observed identity unavailable or malformed: duplicate JSON member\n",
		},
		{
			name:       "non-finite number",
			fixture:    "non-finite.json",
			wantCode:   65,
			wantStderr: "external identity lock: observed identity unavailable or malformed: file is not valid JSON\n",
		},
		{
			name:       "trailing value",
			fixture:    "trailing.input",
			wantCode:   65,
			wantStderr: "external identity lock: observed identity unavailable or malformed: file contains trailing JSON data\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runCase(filepath.Join("testdata", test.fixture), trackedLock)
			if code != test.wantCode || stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("run = (%d, %q, %q), want (%d, %q, %q)", code, stdout, stderr, test.wantCode, test.wantStdout, test.wantStderr)
			}
		})
	}
}

func TestObservedDataErrorsStayBounded(t *testing.T) {
	temp := t.TempDir()
	matching, err := os.ReadFile(filepath.Join("testdata", "matching.json"))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		path       string
		prepare    func(string) error
		wantDetail string
	}{
		{
			name:       "unavailable",
			path:       filepath.Join(temp, "missing.json"),
			prepare:    func(string) error { return nil },
			wantDetail: "file is unavailable",
		},
		{
			name:       "directory",
			path:       filepath.Join(temp, "directory"),
			prepare:    func(path string) error { return os.Mkdir(path, 0o700) },
			wantDetail: "file is not a regular non-symlink file",
		},
		{
			name: "symlink",
			path: filepath.Join(temp, "link.json"),
			prepare: func(path string) error {
				return os.Symlink(filepath.Join("..", "matching.json"), path)
			},
			wantDetail: "file is not a regular non-symlink file",
		},
		{
			name: "dangling symlink",
			path: filepath.Join(temp, "dangling.json"),
			prepare: func(path string) error {
				return os.Symlink("missing-target.json", path)
			},
			wantDetail: "file is not a regular non-symlink file",
		},
		{
			name: "oversize",
			path: filepath.Join(temp, "oversize.json"),
			prepare: func(path string) error {
				return os.WriteFile(path, bytes.Repeat([]byte(" "), observedByteLimit+1), 0o600)
			},
			wantDetail: "file exceeds 32768 bytes",
		},
		{
			name: "invalid UTF-8",
			path: filepath.Join(temp, "invalid-utf8.json"),
			prepare: func(path string) error {
				return os.WriteFile(path, []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, 0o600)
			},
			wantDetail: "file contains invalid UTF-8",
		},
		{
			name: "non-object",
			path: filepath.Join(temp, "array.json"),
			prepare: func(path string) error {
				return os.WriteFile(path, []byte("[]\n"), 0o600)
			},
			wantDetail: "file is not one JSON object",
		},
		{
			name: "excessive nesting",
			path: filepath.Join(temp, "deep.json"),
			prepare: func(path string) error {
				data := []byte(`{"x":` + strings.Repeat("[", maxJSONNesting) + "null" + strings.Repeat("]", maxJSONNesting) + "}")
				return os.WriteFile(path, data, 0o600)
			},
			wantDetail: "JSON nesting exceeds 64 containers",
		},
		{
			name: "injection-like value",
			path: filepath.Join(temp, "injection.json"),
			prepare: func(path string) error {
				data := bytes.Replace(matching, []byte(`"1.18.5"`), []byte(`"https://user:secret@example.invalid/$(touch owned)"`), 1)
				return os.WriteFile(path, data, 0o600)
			},
			wantDetail: "mismatch at runtime.version",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.prepare(test.path); err != nil {
				t.Skipf("cannot prepare filesystem case: %v", err)
			}
			code, stdout, stderr := runCase(test.path, trackedLock)
			if code != 65 && test.name != "injection-like value" {
				t.Fatalf("exit = %d, want 65; stdout=%q stderr=%q", code, stdout, stderr)
			}
			if test.name == "injection-like value" && code != 2 {
				t.Fatalf("exit = %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, test.wantDetail) {
				t.Fatalf("stdout=%q stderr=%q, want stderr containing %q", stdout, stderr, test.wantDetail)
			}
			if strings.Contains(stderr, "secret") || strings.Contains(stderr, "touch owned") {
				t.Fatalf("diagnostic leaked observed value: %q", stderr)
			}
		})
	}
}

func TestOpenAndReadErrorsAreMapped(t *testing.T) {
	path := filepath.Join("testdata", "matching.json")
	_, err := readRegularFileWith(path, observedByteLimit, func(string) (*os.File, error) {
		return nil, errors.New("private open error")
	})
	if err == nil || err.Error() != "file could not be opened for reading" {
		t.Fatalf("open error = %v", err)
	}
	_, err = readBounded(failingReader{}, observedByteLimit)
	if err == nil || err.Error() != "file could not be read" {
		t.Fatalf("read error = %v", err)
	}
}

func TestBoundariesAndParserFailures(t *testing.T) {
	data := bytes.Repeat([]byte("x"), observedByteLimit)
	read, err := readBounded(bytes.NewReader(data), observedByteLimit)
	if err != nil || !bytes.Equal(read, data) {
		t.Fatalf("exact-limit read = %d bytes, %v", len(read), err)
	}
	if _, err := readBounded(bytes.NewReader(append(data, 'x')), observedByteLimit); err == nil || err.Error() != "file exceeds 32768 bytes" {
		t.Fatalf("over-limit error = %v", err)
	}

	for _, input := range []string{
		`{"x":Infinity}`,
		`{"x":-Infinity}`,
		`{"x":1,"x":2}`,
		`{"x":{"y":1,"y":2}}`,
		`{} garbage`,
		`{} {}`,
	} {
		if _, err := parseJSONObject([]byte(input), maxJSONNesting); err == nil {
			t.Errorf("parseJSONObject(%q) unexpectedly succeeded", input)
		}
	}

	boundary := []byte(strings.Repeat("[", maxJSONNesting-1) + "null" + strings.Repeat("]", maxJSONNesting-1))
	objectAtBoundary := append([]byte(`{"x":`), boundary...)
	objectAtBoundary = append(objectAtBoundary, '}')
	if _, err := parseJSONObject(objectAtBoundary, maxJSONNesting); err != nil {
		t.Fatalf("maximum nesting should parse: %v", err)
	}
	overBoundary := []byte(`{"x":` + strings.Repeat("[", maxJSONNesting) + "null" + strings.Repeat("]", maxJSONNesting) + "}")
	if _, err := parseJSONObject(overBoundary, maxJSONNesting); err == nil || err.Error() != "JSON nesting exceeds 64 containers" {
		t.Fatalf("over-depth error = %v", err)
	}
}

func TestUsageAndInvalidLock(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr, trackedLock); code != 0 || stdout.String() != "usage: external-identity --observed FILE\n" || stderr.Len() != 0 {
		t.Fatalf("help = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(nil, &stdout, &stderr, trackedLock); code != 64 || stdout.Len() != 0 || stderr.String() != "external identity lock: usage: external-identity --observed FILE\n" {
		t.Fatalf("usage = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--observed", filepath.Join("testdata", "matching.json")}, &stdout, &stderr, []byte(`{"lock_version":1}`)); code != 70 || stdout.Len() != 0 || stderr.String() != "external identity lock: invalid tracked lock: unexpected direct lock shape or version\n" {
		t.Fatalf("invalid lock = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
}

func TestCheckerImportsOnlyReadOnlyStandardLibraryPackages(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"bytes": true, "embed": true, "encoding/json": true, "fmt": true, "io": true,
		"os": true, "reflect": true, "sort": true, "unicode/utf8": true,
	}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if !allowed[path] {
			t.Errorf("unexpected import %q", path)
		}
	}
}

func runCase(path string, lock []byte) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--observed", path}, &stdout, &stderr, lock)
	return code, stdout.String(), stderr.String()
}

func valueAt(t *testing.T, root map[string]any, path string) any {
	t.Helper()
	var value any = root
	for _, key := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s parent is %T", path, value)
		}
		var present bool
		value, present = object[key]
		if !present {
			t.Fatalf("%s is missing", path)
		}
	}
	return value
}

func reflectEqual(actual, expected any) bool {
	return actual == expected
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
