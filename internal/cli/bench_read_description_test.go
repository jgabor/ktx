package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
)

func TestReadDescriptionGeneratedHook(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun required for generated adapter test")
	}
	dir := t.TempDir()
	for _, name := range []string{"original", "candidate"} {
		data, err := os.ReadFile("../../docs/normalization-read-" + name + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ast, err := os.ReadFile("../experiment/testdata/read-parameters-ast.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ast.json"), ast, 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := strings.ReplaceAll(ktxOpencodePluginTemplate, benchcontract.ReadDescriptionDir, filepath.ToSlash(dir)) + "\nexport { benchmarkReadDescription }\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.ts"), []byte(plugin), 0o600); err != nil {
		t.Fatal(err)
	}
	const script = `import {benchmarkReadDescription} from './plugin.ts';
import {readFileSync,writeFileSync} from 'node:fs';
const original=readFileSync(import.meta.dir+'/original.txt','utf8');
const candidate=readFileSync(import.meta.dir+'/candidate.txt','utf8');
const ast=JSON.parse(readFileSync(import.meta.dir+'/ast.json','utf8'));
delete process.env.KTX_BENCH_READ_DESCRIPTION;
if(benchmarkReadDescription(false,'epoch')!==undefined) throw Error('ordinary activation');
process.env.KTX_BENCH_READ_DESCRIPTION='candidate';
try { benchmarkReadDescription(false,'epoch'); throw Error('unguarded') } catch(e) { if(e.message==='unguarded') throw e }
for(const side of ['baseline','candidate']) {
 process.env.KTX_BENCH_READ_DESCRIPTION=side;
 const hook=benchmarkReadDescription(true,'epoch-'+side);
 const parameters={ast}; const output={description:original,parameters};
 await hook({toolID:'read'},output);
 if(output.description!==(side==='candidate'?candidate:original)||output.parameters!==parameters) throw Error('bad transform');
 const other={description:'public synthetic',parameters:{ast:{public:true}}};
 await hook({toolID:'bash'},other);
 if(other.description!=='public synthetic') throw Error('other tool changed');
 for(const broken of [ {description:original+'changed',parameters}, {description:original,parameters:{ast:{changed:true}}} ]) {
  let rejected=false;try { await hook({toolID:'read'},broken) } catch { rejected=true }
  if(!rejected) throw Error('stale source/schema accepted');
 }
}

writeFileSync(import.meta.dir+'/candidate.txt',candidate+'changed');
let rejected=false;try { benchmarkReadDescription(true,'epoch') } catch {rejected=true}
if(!rejected)throw Error('altered fixture accepted');
console.log('deterministic generated hook: pass');
`
	scriptPath := filepath.Join(dir, "test.ts")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bun, scriptPath)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestReadDescriptionAgentRejectsUnownedFollowup(t *testing.T) {
	t.Setenv(benchcontract.ReadDescriptionEnv, "")
	var out, diagnostics strings.Builder
	if code := runBenchAgent([]string{"-m", "openai/gpt-6-luna", "--variant", "high", "--followup", "second", "first"}, Options{}, &out, &diagnostics); code != ExitUsage || !strings.Contains(diagnostics.String(), "owned JSON benchmark") {
		t.Fatalf("code=%d %s", code, diagnostics.String())
	}
}

func TestReadDescriptionSameSessionLaunch(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "two-turns", true: "failed-first"}[failFirst], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			pluginPath, err := opencodeGlobalPluginPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(pluginPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pluginPath, []byte(ktxOpencodePluginTemplate), 0o600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
if [ "$1" = '--version' ]; then printf '1.18.5\n'; exit; fi
printf '%s\n' "$@" >> "$HOME/argv"
if [ "$FAIL_FIRST" = 'yes' ]; then exit 7; fi
printf '%s\n' '{"type":"text","sessionID":"ses_synthetic","part":{"text":"synthetic"}}' '{"type":"step_finish","sessionID":"ses_synthetic","part":{"reason":"stop"}}'
`
			if err := os.WriteFile(filepath.Join(root, "opencode"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if failFirst {
				t.Setenv("FAIL_FIRST", "yes")
			} else {
				t.Setenv("FAIL_FIRST", "")
			}
			var out, diagnostics bytes.Buffer
			err = runReadDescriptionTurns([]string{"run", "--format", "json", "-m", "openai/gpt-6-luna", "--variant", "high", "first"}, "second", root, &out, &diagnostics)
			if (err != nil) != failFirst {
				t.Fatalf("err=%v", err)
			}
			argv, readErr := os.ReadFile(filepath.Join(root, "argv"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(argv), "--session\nses_synthetic\nsecond\n") == failFirst {
				t.Fatalf("continuation argv=%s", argv)
			}
			_, secondErr := os.Stat(filepath.Join(root, "turn-2.jsonl"))
			if (secondErr == nil) == failFirst {
				t.Fatalf("second turn after failure: %v", secondErr)
			}
		})
	}
}
