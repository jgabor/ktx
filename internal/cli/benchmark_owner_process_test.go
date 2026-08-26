package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestBenchmarkHookProcessHelper(t *testing.T) {
	if os.Getenv("KTX_TEST_BENCHMARK_HOOK_HELPER") != "1" {
		return
	}
	database, err := dbpkg.Open(os.Getenv("KTX_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	callID := os.Getenv("KTX_TEST_CALL_ID")
	payload := fmt.Sprintf(`{"runtime":"opencode","input":{"tool":"bash","sessionID":"runtime-session","callID":%q,"args":{"command":"true"}},"output":{"output":"first evidence %s"}}`, callID, callID)
	if _, err := hookLogic(bytes.NewBufferString(payload), database, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentBenchmarkHooksUseOneExplicitOwnerWithoutProcessFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ktx.db")
	database, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	openedAt := time.Now().UTC().Format(time.RFC3339Nano)
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, openedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureCollectionInterval(interval.ID, []string{"run"}, openedAt); err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.SessionEnv, interval.SessionID)
	t.Setenv(benchcontract.IntervalEnv, interval.ID)
	t.Setenv(benchcontract.ContractEnv, benchcontract.ContractVersion)
	t.Setenv(benchcontract.CollectorModeEnv, benchcontract.CollectorRequired)
	if _, err := telemetryLogic([]string{"collector-open"}, FormatJSON, bytes.NewBufferString(`{"epoch_id":"epoch"}`), database, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	configDir := t.TempDir()
	const processes = 6
	var wg sync.WaitGroup
	errorsCh := make(chan error, processes)
	for index := 0; index < processes; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			command := exec.Command(os.Args[0], "-test.run=^TestBenchmarkHookProcessHelper$")
			command.Env = append(os.Environ(), "KTX_TEST_BENCHMARK_HOOK_HELPER=1", fmt.Sprintf("KTX_TEST_CALL_ID=call-%d", index), "KTX_DB="+path,
				benchcontract.SessionEnv+"="+interval.SessionID, benchcontract.IntervalEnv+"="+interval.ID,
				benchcontract.ContractEnv+"="+benchcontract.ContractVersion, benchcontract.CollectorModeEnv+"="+benchcontract.CollectorRequired,
				"XDG_RUNTIME_DIR="+runtimeDir, "XDG_CONFIG_HOME="+configDir)
			if output, err := command.CombinedOutput(); err != nil {
				errorsCh <- fmt.Errorf("helper %d: %w: %s", index, err, output)
			}
		}(index)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	database, err = dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var sessions, calls, rollup int
	if err := database.DB().QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := database.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE session_id = ?`, interval.SessionID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if err := database.DB().QueryRow(`SELECT tool_call_count FROM sessions WHERE id = ?`, interval.SessionID).Scan(&rollup); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || calls != processes || rollup != processes {
		t.Fatalf("sessions=%d calls=%d rollup=%d", sessions, calls, rollup)
	}
	files, err := filepath.Glob(filepath.Join(runtimeDir, "ktx", "sessions", "*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("process session files=%v err=%v", files, err)
	}
}
