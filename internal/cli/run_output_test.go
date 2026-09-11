package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/jgabor/ktx/internal/cli/testutil"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestRunOutputNormalization(t *testing.T) {
	for _, input := range []string{"", "\n", "\r", "\r\n", "a", "a\r", "a\r\nb\n\nlast", "a\rb\r\r\n", strings.Repeat("x", 15) + "\r\nlast", strings.Repeat("x", 16) + "\r", strings.Repeat("x", 31) + "\r\r\n"} {
		var want, got bytes.Buffer
		scanner := bufio.NewScanner(strings.NewReader(input))
		for scanner.Scan() {
			want.Write(scanner.Bytes())
			want.WriteByte('\n')
		}
		reader := bufio.NewReaderSize(iotest.OneByteReader(strings.NewReader(input)), 16)
		for {
			line, oversized, err := forwardAgentLine(reader, &got)
			if err == io.EOF {
				break
			}
			if err != nil || oversized {
				t.Fatalf("input=%q oversized=%v err=%v", input, oversized, err)
			}
			if bytes.ContainsAny(line, "\n") {
				t.Fatalf("event contains newline: %q", line)
			}
		}
		if got.String() != want.String() {
			t.Fatalf("input=%q got=%q want=%q", input, &got, &want)
		}
	}
}

type runOutputErrorWriter struct {
	err           error
	calls, failAt int
}

func (w *runOutputErrorWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func TestRunOutputForwardingErrors(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrClosedPipe} {
		for _, failAt := range []int{1, 2} {
			writer := &runOutputErrorWriter{err: err, failAt: failAt}
			count, got := teeAndParseRuntimeCollection(strings.NewReader("plain\n"), writer, nil, "", io.Discard, "", "")
			if count != 0 || !errors.Is(got, err) {
				t.Fatalf("count=%d err=%v want=%v", count, got, err)
			}
		}
	}
	var out bytes.Buffer
	_, _, err := forwardAgentLine(bufio.NewReader(iotest.ErrReader(io.ErrUnexpectedEOF)), &out)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error = %v", err)
	}
}

func TestRunOutputFailurePreservesCommittedEvidence(t *testing.T) {
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sessionID := "abcdefghijklmnop"
	if err := d.InsertSession(&dbpkg.Session{ID: sessionID, Agent: "test"}); err != nil {
		t.Fatal(err)
	}
	writer := &runOutputErrorWriter{err: io.ErrClosedPipe, failAt: 3}
	count, err := teeAndParseRuntimeCollection(strings.NewReader("{\"tool\":\"read\",\"output\":\"committed\"}\n{\"tool\":\"read\",\"output\":\"undelivered\"}\n"), writer, d, sessionID, io.Discard, "", "")
	if count != 1 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("count=%d err=%v", count, err)
	}
	var output string
	if err := d.DB().QueryRow(`SELECT output FROM tool_calls`).Scan(&output); err != nil || output != "committed" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

type repeatedRunOutput struct{}

func (repeatedRunOutput) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func BenchmarkRunOutputOversized(b *testing.B) {
	for _, size := range []int64{65 << 20, 130 << 20} {
		b.Run(fmt.Sprint(size>>20), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(size)
			for b.Loop() {
				_, oversized, err := forwardAgentLine(bufio.NewReaderSize(io.LimitReader(repeatedRunOutput{}, size), 64*1024), io.Discard)
				if err != nil || !oversized {
					b.Fatalf("oversized=%v err=%v", oversized, err)
				}
			}
		})
	}
}

func TestRunOutputChild(t *testing.T) {
	mode := os.Getenv("KTX_TEST_OUTPUT_CHILD")
	if mode == "" {
		return
	}
	if mode == "large" {
		_, _ = io.WriteString(os.Stdout, "before\n")
		chunk := bytes.Repeat([]byte("x"), 64*1024)
		for range 1025 {
			_, _ = os.Stdout.Write(chunk)
		}
		_, _ = io.WriteString(os.Stdout, "\r\nafter\n{\"tool\":\"read\",\"output\":\"control\"}\nfinal")
		os.Exit(7)
	}
	for {
		_, _ = io.WriteString(os.Stdout, "keep writing\n")
	}
}

func TestRunOutputBrokenPipeProcess(t *testing.T) {
	if os.Getenv("KTX_TEST_BROKEN_PIPE_WRAPPER") != "1" {
		return
	}
	os.Exit(executeRun(os.Args[0], []string{"-test.run=^TestRunOutputChild$"}, runOptions{RecordStream: true}, os.Stdout, os.Stderr))
}

func TestRunOutputBrokenPipe(t *testing.T) {
	testutil.WithTempXDG(t)
	t.Setenv("KTX_TEST_BROKEN_PIPE_WRAPPER", "1")
	t.Setenv("KTX_TEST_OUTPUT_CHILD", "continuous")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunOutputBrokenPipeProcess$")
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil || cmd.ProcessState.ExitCode() != ExitInternal || !strings.Contains(stderr.String(), "write agent stdout") {
		t.Fatalf("exit=%v err=%v stderr=%s", cmd.ProcessState, err, &stderr)
	}
	d, err := dbpkg.Open(os.Getenv("KTX_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var closed int
	if err := d.DB().QueryRow(`SELECT count(*) FROM sessions WHERE ended_at IS NOT NULL`).Scan(&closed); err != nil || closed != 1 {
		t.Fatalf("closed=%d err=%v", closed, err)
	}
}

func outputChild(t *testing.T, mode string) **exec.Cmd {
	t.Helper()
	testutil.WithTempXDG(t)
	t.Setenv("KTX_TEST_OUTPUT_CHILD", mode)
	original := runCmd
	var child *exec.Cmd
	// Independent watchdog makes the old deadlock fail rather than hang the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() { cancel(); runCmd = original })
	runCmd = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		child = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunOutputChild$")
		return child
	}
	return &child
}

func TestRunOutputOversizedSubprocess(t *testing.T) {
	child := outputChild(t, "large")
	got, want := sha256.New(), sha256.New()
	_, _ = io.WriteString(want, "before\n")
	chunk := bytes.Repeat([]byte("x"), 64*1024)
	for range 1025 {
		_, _ = want.Write(chunk)
	}
	_, _ = io.WriteString(want, "\nafter\n{\"tool\":\"read\",\"output\":\"control\"}\nfinal\n")
	var stderr bytes.Buffer
	code := executeRun("output-child", nil, runOptions{}, got, &stderr)
	if code != 7 || !bytes.Equal(got.Sum(nil), want.Sum(nil)) {
		t.Fatalf("code=%d complete digest=%v stderr=%s", code, bytes.Equal(got.Sum(nil), want.Sum(nil)), &stderr)
	}
	if (*child).ProcessState == nil || !strings.Contains(stderr.String(), "event parsing skipped") {
		t.Fatalf("unsettled child or missing oversize diagnostic: %s", &stderr)
	}
	d, err := dbpkg.Open(os.Getenv("KTX_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var count int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE output='control'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("control persistence count=%d err=%v", count, err)
	}
}

type failingRunOutput struct{ short bool }

func (w failingRunOutput) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("sink unavailable")
}

func TestRunOutputSinkFailure(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "short"}[short], func(t *testing.T) {
			child := outputChild(t, "continuous")
			var stderr bytes.Buffer
			start := time.Now()
			code := executeRun("output-child", nil, runOptions{RecordStream: true}, failingRunOutput{short}, &stderr)
			if code != ExitInternal || time.Since(start) > 5*time.Second || (*child).ProcessState == nil {
				t.Fatalf("code=%d elapsed=%s settled=%v stderr=%s", code, time.Since(start), (*child).ProcessState != nil, &stderr)
			}
			if !strings.Contains(stderr.String(), "write agent stdout") {
				t.Fatalf("missing sink diagnostic: %s", &stderr)
			}
			d, err := dbpkg.Open(os.Getenv("KTX_DB"))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			var recordings, closed, interrupted int
			for query, dest := range map[string]*int{
				`SELECT count(*) FROM recorded_streams`:                             &recordings,
				`SELECT count(*) FROM sessions WHERE ended_at IS NOT NULL`:          &closed,
				`SELECT count(*) FROM ingress_continuity WHERE state='interrupted'`: &interrupted,
			} {
				if err := d.DB().QueryRow(query).Scan(dest); err != nil {
					t.Fatal(err)
				}
			}
			if recordings != 0 || closed != 1 || interrupted != 1 {
				t.Fatalf("recordings=%d closed=%d interrupted=%d", recordings, closed, interrupted)
			}
		})
	}
}
