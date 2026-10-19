package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/config"
)

func TestOptimizeEarlyApplicationBoundary(t *testing.T) {
	for _, extra := range []string{"[logs]\nlevel='debug'\n", "[pipeline.other]\nstages=['normalize']\n", "[[route]]\ntool='bash'\nprofile='default'\n", "timeout='1s'\n", "recover_footer=true\n"} {
		t.Run(extra, func(t *testing.T) {
			_, args, runner, fake := optimizeLiveFixture(t)
			optimizeWrite(t, "baseline.toml", "[pipeline.default]\nstages=[]\n")
			optimizeWrite(t, "proposal.toml", "[pipeline.default]\nstages=['json-compact','strip-ansi','dedup-blank','normalize']\n"+extra)
			d := optimizeRun(t, "--config", "baseline.toml", "--candidate", "proposal.toml")
			if d.Outcome != "blocked" || d.ApplyEligible || d.SelectedCandidate != nil || d.Candidates[2].Screen != nil || d.Candidates[2].Reasons[0] != "unsupported_changes_outside_pipeline_default_stages" {
				t.Fatalf("early boundary: %+v", d)
			}
			r, err := qualifyOptimize(context.Background(), d, args, runner, io.Discard)
			if err != nil || r.Outcome != "blocked" || r.ApplyEligible || fake.launches != 0 || fake.capabilities != 0 {
				t.Fatalf("known unappliable proposal spent calls: %v %+v", err, r)
			}
			code, text, _ := optimizeInvoke(t, "show", r.ID, "--format", "text")
			if code != 0 || !strings.Contains(text, "unsupported_changes_outside_pipeline_default_stages") || !strings.Contains(text, "Apply eligible: false") {
				t.Fatal("text/JSON boundary mismatch")
			}
		})
	}
}

func TestOptimizeDerivedConfigPreservesUnrelatedBytes(t *testing.T) {
	optimizeTestRoot(t)
	for _, body := range []string{
		"# prefix\r\n[pipeline.default] # header\r\nstages = [\r\n  'strip-ansi', # existing stage\r\n]\r\ntimeout='500ms' # keep me\r\n[logs]\r\nlevel='off' # suffix\r\n",
		"# no table\n[logs]\nlevel='off'\n",
		"[pipeline.default]\n# preserve comment\ntimeout='500ms'\n",
	} {
		optimizeWrite(t, "baseline.toml", body)
		cfg, err := config.LoadFile("baseline.toml")
		if err != nil {
			t.Fatal(err)
		}
		out, err := optimizeDerivedConfig([]byte(body), cfg, []string{"json-compact"})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{"# prefix\r\n", "timeout='500ms' # keep me\r\n", "[logs]\r\nlevel='off' # suffix\r\n", "# preserve comment\n", "# no table\n[logs]\nlevel='off'\n"} {
			if strings.Contains(body, line) && !bytes.Contains(out, []byte(line)) {
				t.Fatalf("unrelated bytes lost: %q", line)
			}
		}
		if _, _, err := optimizeStageOnlyDiff([]byte(body), out); err != nil {
			t.Fatal(err)
		}
	}
	optimizeWrite(t, "baseline.toml", "[pipeline]\ndefault={stages=[]}\n")
	r := optimizeRun(t, "--config", "baseline.toml")
	if r.Outcome != "blocked" || r.Reasons[0] != "unsupported_default_stages_edit_syntax" {
		t.Fatal("unusual syntax silently re-encoded")
	}
}

func TestOptimizeHelpAndOutcomeActions(t *testing.T) {
	optimizeTestRoot(t)
	code, help, stderr := optimizeInvoke(t, "--help")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, required := range []string{"pipeline.default.stages", "before live preflight", "24 agent launches", "not provider requests or a USD cap", "OpenCode auth", "embedded", "Exit 0", "3 apply/rollback conflict", "never prompt"} {
		if !strings.Contains(help, required) {
			t.Fatalf("undiscoverable contract: %s", required)
		}
	}
	for _, argv := range [][]string{{"--live"}, {"--candidate", ""}, {"--format", "xml"}} {
		code, _, _ := optimizeInvoke(t, argv...)
		if code != ExitUsage {
			t.Fatal("invalid inputs did not fail non-interactively")
		}
	}
	for _, outcome := range []string{"screened", "ready", "no_change", "blocked", "incomplete"} {
		r := optimizeResult{ID: strings.Repeat("a", 32), Outcome: outcome, Mode: "offline"}
		r.NextCommands = optimizeNextCommands(r)
		var text bytes.Buffer
		if err := renderOptimizeText(&text, r); err != nil {
			t.Fatal(err)
		}
		for _, command := range r.NextCommands {
			if !strings.Contains(text.String(), command) {
				t.Fatal("JSON/text next commands differ")
			}
		}
		if outcome != "ready" {
			for _, command := range r.NextCommands {
				if strings.Contains(command, "optimize apply") {
					t.Fatal("ineligible outcome recommends apply")
				}
			}
		}
	}
}

func TestOptimizeSuppliedHandoffCommand(t *testing.T) {
	optimizeTestRoot(t)
	optimizeWrite(t, "baseline.toml", "[pipeline.default]\nstages=[]\n")
	optimizeWrite(t, "proposal.toml", "[pipeline.default]\nstages=['json-compact','strip-ansi','dedup-blank','normalize']\n")
	r := optimizeRun(t, "--config", "baseline.toml", "--candidate", "proposal.toml")
	if r.Outcome != "screened" || r.SelectedCandidate == nil || r.SelectedCandidate.Artifact != "candidate-3.toml" {
		t.Fatal("supported supplied proposal not screened")
	}
	commands, _ := json.Marshal(r.NextCommands)
	if !bytes.Contains(commands, []byte("--candidate")) || !bytes.Contains(commands, []byte(filepath.Join(".ktx", "optimizations", r.ID, "candidate-3.toml"))) {
		t.Fatal("next live cycle silently drops exact supplied config")
	}
	if _, err := os.Stat("proposal.toml"); err != nil {
		t.Fatal(err)
	}
}
