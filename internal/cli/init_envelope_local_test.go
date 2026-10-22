package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jgabor/ktx/internal/testjs"
)

func TestOpencodeEnvelopeLocalCLI(t *testing.T) {
	binary := os.Getenv("KTX_ADAPTER_CLI")
	if binary == "" {
		t.Skip("set KTX_ADAPTER_CLI to the locally built binary")
	}
	node := testjs.Runtime(t, "node", true)
	for _, input := range []string{"", "unchanged", "{\n  \"hello\": \"world\"\n}"} {
		t.Run(strconv.Quote(input), func(t *testing.T) {
			root := t.TempDir()
			testjs.WriteModulePackage(t, root)
			for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"} {
				path := filepath.Join(root, key)
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv(key, path)
			}
			t.Setenv("KTX_SESSION_ID", "")
			t.Setenv("KTX_COLLECTION_ID", "")
			t.Setenv("KTX_BENCH_COLLECTOR_MODE", "")
			t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
			config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "ktx")
			if err := os.Mkdir(config, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(config, "ktx.toml"), []byte("[pipeline.default]\nstages = [\"json-compact\"]\nrecover_footer = false\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "init", "opencode")
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("install: %v: %s", err, out)
			}
			plugin := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "plugins", "ktx.ts")
			want := input
			if input != "" && input != "unchanged" {
				want = `{"hello":"world"}`
			}
			driver := `import plugin from ` + strconv.Quote(plugin) + `
const hooks: any = await plugin({} as any)
const output = {title:"original title", metadata:{kept:true}, output:` + strconv.Quote(input) + `}
await hooks["tool.execute.after"]({tool:"read",sessionID:"ses_local",callID:"call_local",args:{}},output)
if (output.output !== ` + strconv.Quote(want) + ` || output.title !== "original title" || output.metadata.kept !== true) throw new Error(JSON.stringify(output))
console.log("delivery verified")
`
			path := filepath.Join(root, "driver.ts")
			if err := os.WriteFile(path, []byte(driver), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd = exec.Command(node, testjs.DisableExperimentalWarning, path)
			cmd.Dir = root
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil || stderr.Len() != 0 || stdout.String() != "delivery verified\n" {
				t.Fatalf("delivery: %v stdout=%s stderr=%s", err, &stdout, &stderr)
			}
		})
	}
}
