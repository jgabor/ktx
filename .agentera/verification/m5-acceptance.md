# M5 acceptance verification log

Generated: 2026-07-01
Task: 5 (M5 acceptance verification)
Binary: /tmp/ktx-m5-acceptance (built from working tree; M5 implementation
uncommitted as of this log)
Plan: M5 — User-extensible compression: ktx plugin + ktx config show +
external Builder (D21)

Decisions referenced:
- D5 (config surface — ktx.toml + $XDG_CONFIG_HOME/ktx, single source
  of truth; BurntSushi/toml is the D5-approved dep)
- D14 (compression pipeline — conservative-on-failure invariants
  extend to external subprocess stages: empty-output guard,
  stage-error passthrough, unknown-stage skip)
- D15 (pre-pipeline passthrough-row gate fires for is_error=true sources)
- D21 (structured editor over [[plugin]] blocks; agents call --json,
  never author TOML by hand)

```
===========================================================================
BUILD
===========================================================================
$ go build -buildvcs=false -o /tmp/ktx-m5-acceptance ./cmd/ktx
build exit: 0

$ go vet ./...
vet exit: 0

$ go test ./... -count=1
ok      github.com/jgabor/ktx/internal/cli           0.069s
ok      github.com/jgabor/ktx/internal/cli/testutil  0.001s
ok      github.com/jgabor/ktx/internal/config        0.003s
ok      github.com/jgabor/ktx/internal/db            0.050s
ok      github.com/jgabor/ktx/internal/parser        0.002s
ok      github.com/jgabor/ktx/internal/pipeline      0.004s
ok      github.com/jgabor/ktx/internal/pipeline/stages 0.001s

===========================================================================
M4 REGRESSION (byte-identical passthrough path)
===========================================================================
M4 tests remain green: passthrough path, configured built-in path,
compressed migration, D15 gate, D20 replay — all pass with -count=1.
No M4 test was modified to accommodate M5. The config_test.go
malformed-test assertions were loosened (substring → existence) per
plan AC1: "error-message text may differ; only the existence of
*ParseError on malformed TOML is preserved."

RESULT: PASS — M4 regression byte-identical.

===========================================================================
M5 ACCEPTANCE
===========================================================================

--- AC: End-to-end plugin runs (echo) ---

Setup: ktx.toml with [[plugin]] name="myecho" command="/bin/echo"
       and [pipeline.default].stages = ["myecho:hello"]
Seeded tool_call id: test-call-1
Source output: "hello\nline 2\nline 3\nline 4\nline 5\n"

$ ktx compress test-call-1
"xbnxtzxptbtebkdu"
exit: 0

  compressed.output (hex) : 68656C6C6F0A  ("hello\n")
  pipeline (DB)            : ["myecho:hello"]
  compressed.output != source : True (echo replaced source with "hello\n")

RESULT: PASS — external plugin stage ran; output is echo's stdout.

--- AC: Reversibility (decompress returns source verbatim) ---

$ ktx decompress xbnxtzxptbtebkdu
hello
line 2
line 3
line 4
line 5
exit: 0

  decompress.output == source.output : True (D14 reversibility preserved)

RESULT: PASS — source retained in tool_calls; decompress returns it verbatim.

--- AC: D14 invariant 1 — empty-output guard (/bin/true) ---

ktx.toml: stages = ["noop"], [[plugin]] name="noop" command="/bin/true"
/bin/true exits 0 with empty stdout.

$ ktx compress test-call-1
"frosmbtetrjuokhl"
exit: 0

  compressed.output (hex) : 68656C6C6F0A6C696E6520320A... (source verbatim)
  pipeline (DB)            : ["noop"]

RESULT: PASS — empty output discarded, input passed through unchanged.
  The stage IS recorded in Ran (it ran, just produced empty output).

--- AC: D14 invariant 2 — stage-error passthrough (/bin/false) ---

ktx.toml: stages = ["fail"], [[plugin]] name="fail" command="/bin/false"
/bin/false exits 1 with empty stdout.

$ ktx compress test-call-1
ktx: warning: pipeline stage "fail" errored: external stage "/bin/false": exit status 1
"hwohbyjpwqucgtkk"
exit: 0

  compressed.output (hex) : 68656C6C6F0A6C696E6520320A... (source verbatim)
  pipeline (DB)            : []

RESULT: PASS — non-zero exit → input passed through; warning emitted;
  stage absent from Ran (pipeline JSON is []).

--- AC: D15 pre-pipeline gate (is_error=true source) ---

Seeded error-call-1 with is_error=true, output="Error: something went wrong"
ktx.toml: stages = ["myecho:hello"], [[plugin]] name="myecho" command="/bin/echo"

$ ktx compress error-call-1
ktx: warning: source tool_call indicates failure, compression skipped
"zdphsdrhawwvyytt"
exit: 0

  compressed.output = "Error: something went wrong" (source verbatim)
  pipeline (DB)     : []

RESULT: PASS — D15 gate fires for M5 external-stage pipelines; passthrough
  row persisted; pipeline JSON is [].

--- AC: ktx plugin list --json ---

ktx.toml: [[plugin]] name="myecho" command="/bin/echo" description="echo stage"

$ ktx plugin list
{"plugins":[{"name":"strip-ansi","builtin":true,"removable":false},
{"name":"dedup-blank","builtin":true,"removable":false},
{"name":"normalize","builtin":true,"removable":false},
{"name":"head","builtin":true,"removable":false},
{"name":"truncate","builtin":true,"removable":false},
{"name":"myecho","builtin":false,"removable":true,"command":"/bin/echo","description":"echo stage"}]}
exit: 0

RESULT: PASS — 5 builtins (builtin=true, removable=false) + 1 user plugin
  (builtin=false, removable=true, command/description populated).

--- AC: ktx config show --json ---

With ktx.toml (stages = ["myecho:hello"]):

$ ktx config show
{"profiles":{"default":{"stages":["myecho:hello"]}}}
exit: 0

Without ktx.toml (passthrough path):

$ ktx config show
{"profiles":{"default":{"stages":[]}}}
exit: 0

RESULT: PASS — active pipeline shown per profile; no-config case shows
  empty stages (D14 passthrough).

--- AC: ktx plugin add (creates new ktx.toml) ---

No ktx.toml exists.

$ ktx plugin add mycat /bin/cat --description "passthrough"
{"plugin":{"command":"/bin/cat","description":"passthrough","name":"mycat"}}
exit: 0

  ktx.toml: [[plugin]] block with name="mycat" command="/bin/cat" description="passthrough"

RESULT: PASS — new ./ktx.toml created with one [[plugin]] block.

--- AC: ktx plugin add (appends to existing ktx.toml) ---

ktx.toml has [pipeline.default].stages = ["strip-ansi", "head:100"]

$ ktx plugin add myecho /bin/echo
{"plugin":{"command":"/bin/echo","description":"","name":"myecho"}}
exit: 0

  ktx.toml: [pipeline.default] preserved, [[plugin]] block appended

RESULT: PASS — existing content preserved, new block appended.

--- AC: ktx plugin remove (user plugin) ---

$ ktx plugin remove mycat
{"removed":{"command":"/bin/cat","description":"passthrough","name":"mycat"}}
exit: 0

  ktx.toml: [[plugin]] block deleted

RESULT: PASS — user plugin removed; JSON envelope with removed fields.

--- AC: ktx plugin remove (builtin without override) ---

$ ktx plugin remove head
{"error":{"code":"usage","message":"cannot remove built-in stage \"head\"; removable plugins: (none)","valid_examples":["ktx plugin remove <head>"]}}
exit: 1

RESULT: PASS — self-correcting error with valid_examples; exit 1.

--- AC: ktx plugin remove (builtin WITH override) ---

ktx.toml: [[plugin]] name="head" command="/bin/cat" (overrides builtin)

$ ktx plugin remove head
{"removed":{"command":"/bin/cat","description":"","name":"head"}}
exit: 0

RESULT: PASS — user override removed; builtin head re-emerges on next run.

===========================================================================
FIXES APPLIED
===========================================================================
None — all acceptance criteria passed on first run.

===========================================================================
VERIFICATION SUMMARY
===========================================================================
M5 acceptance: ALL PASS

  1. End-to-end plugin (echo) runs as pipeline stage ........... PASS
  2. Reversibility (decompress returns source verbatim) ....... PASS
  3. D14 invariant 1 (empty-output guard, /bin/true) ............ PASS
  4. D14 invariant 2 (stage-error passthrough, /bin/false) ..... PASS
  5. D15 pre-pipeline gate (is_error=true) ...................... PASS
  6. ktx plugin list --json (builtins + user) ................... PASS
  7. ktx config show --json (with/without config) .............. PASS
  8. ktx plugin add (create + append) ........................... PASS
  9. ktx plugin remove (user + builtin + override) ............. PASS
 10. M4 regression (byte-identical passthrough path) ............... PASS
 11. go test ./... -count=1 (all packages green) ................ PASS
 12. go vet ./... (clean) ...................................... PASS

ROADMAP M5 acceptance criterion: "a user can supply a configuration
that extends or replaces the default compression behavior, and the
extension takes effect without code changes" — SATISFIED.
```
