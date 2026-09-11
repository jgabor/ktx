package cli

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestRunLongLivedWriterRejectsUpgradedSchema(t *testing.T) {
	d, err := dbpkg.Open(filepath.Join(t.TempDir(), "wrapper.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const session = "abcdefghijklmnop"
	if err := d.InsertSession(&dbpkg.Session{ID: session}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	input := "{\"tool\":\"read\",\"output\":\"before upgrade\"}\n"
	n, err := teeAndParseRuntimeCollection(strings.NewReader(input), &stdout, d, session, &stderr, "", "")
	if err != nil || n != 1 || stdout.String() != input || stderr.Len() != 0 {
		t.Fatalf("initial wrapper count=%d err=%v out=%q stderr=%q", n, err, &stdout, &stderr)
	}
	// A long-lived wrapper keeps this handle after another binary upgrades.
	if _, err := d.DB().Exec(fmt.Sprintf("PRAGMA user_version=%d", dbpkg.MaxSupportedSchema()+1)); err != nil {
		t.Fatal(err)
	}
	input = "{\"tool\":\"read\",\"output\":\"after upgrade\"}\n"
	stdout.Reset()
	n, err = teeAndParseRuntimeCollection(strings.NewReader(input), &stdout, d, session, &stderr, "", "")
	if err != nil || n != 0 || stdout.String() != input || !strings.Contains(stderr.String(), "newer") {
		t.Fatalf("stale wrapper count=%d err=%v out=%q stderr=%q", n, err, &stdout, &stderr)
	}
	var count, rollup int
	if err := d.DB().QueryRow("SELECT count(*) FROM tool_calls").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := d.DB().QueryRow("SELECT tool_call_count FROM sessions WHERE id=?", session).Scan(&rollup); err != nil {
		t.Fatal(err)
	}
	if count != 1 || rollup != 1 {
		t.Fatalf("stale wrapper changed evidence: count=%d rollup=%d", count, rollup)
	}
}
