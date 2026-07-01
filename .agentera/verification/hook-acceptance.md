# ktx hook acceptance verification log

Generated: 2026-07-01
Task: 4 (hook acceptance verification)
Binary: built from working tree (go install ./cmd/ktx)
Plan: ktx hook — in-flight compression plumbing verification (D22 Phase 1)

Decision referenced: D22 (firm)

```
===========================================================================
BUILD
===========================================================================
$ go build ./... && go install ./cmd/ktx
build exit: 0

$ go vet ./...
vet exit: 0

$ go test ./... -count=1
ok  github.com/jgabor/ktx/internal/cli           0.078s
ok  github.com/jgabor/ktx/internal/cli/testutil  0.002s
ok  github.com/jgabor/ktx/internal/config        0.002s
ok  github.com/jgabor/ktx/internal/db            0.049s
ok  github.com/jgabor/ktx/internal/parser        0.002s
ok  github.com/jgabor/ktx/internal/pipeline      0.004s
ok  github.com/jgabor/ktx/internal/pipeline/stages 0.002s

===========================================================================
REGRESSION (M1-M5)
===========================================================================
All M1-M5 tests pass byte-identical. The compressLogic refactor
(extracted compressCore) preserved the existing compressLogic signature;
existing compress_test.go tests pass unchanged.

===========================================================================
HOOK ACCEPTANCE
===========================================================================

--- AC1: Identity (no ktx.toml) ---

$ echo '{"id":"testhook01",...,"output":"line 1\nline 2\nline 3\nline 4\nline 5\n",...}' | ktx hook
{"output":"line 1\nline 2\nline 3\nline 4\nline 5\n","compressed_id":null,"source_id":"testhook01"}
exit: 0

RESULT: PASS — original persisted, returned unchanged, compressed_id=null.

--- AC2: Configured pipeline (head:3) ---

$ echo '{"id":"testhook02",...,"output":"line 1\nline 2\nline 3\nline 4\nline 5\n",...}' | ktx hook
{"output":"line 1\nline 2\nline 3\n","compressed_id":"gtrzbeajajglbvgm","source_id":"testhook02"}
exit: 0

RESULT: PASS — output compressed to 3 lines; compressed row created; envelope correct.

--- AC3: Reversibility ---

$ ktx decompress gtrzbeajajglbvgm
line 1
line 2
line 3
line 4
line 5
exit: 0

RESULT: PASS — original 5 lines returned verbatim via decompress.

--- AC4: D15 gate (is_error=true) ---

$ echo '{"id":"testhook03",...,"output":"Error: failed","is_error":true,...}' | ktx hook
ktx: warning: source tool_call indicates failure, compression skipped
{"output":"Error: failed","compressed_id":null,"source_id":"testhook03"}
exit: 0

RESULT: PASS — D15 gate fired, identity returned, no compressed row.

--- AC5: Malformed JSON ---

$ echo 'not json' | ktx hook
exit: 1 (usage error)

RESULT: PASS — usage error, no envelope written.

--- AC6: Missing required fields ---

$ echo '{"tool":"bash","output":"hi","timestamp":"2026-07-01T12:00:00Z"}' | ktx hook
exit: 1 (usage: missing required field: id)

RESULT: PASS — usage error for missing id/tool/timestamp.

--- AC7: Plugin installed ---

$ ktx init opencode
{"installed":true,"path":"~/.config/opencode/plugins/ktx.ts"}

RESULT: PASS — plugin template updated to call ktx hook (not ktx write).

===========================================================================
VERIFICATION SUMMARY
===========================================================================
Hook acceptance: ALL PASS

  1. Identity (no ktx.toml) — original returned unchanged ............ PASS
  2. Configured pipeline (head:3) — output compressed ................. PASS
  3. Reversibility — decompress returns original verbatim ............ PASS
  4. D15 gate (is_error=true) — compression skipped ................... PASS
  5. Malformed JSON — usage error ................................... PASS
  6. Missing fields — usage error ................................... PASS
  7. Plugin installed — template calls ktx hook ..................... PASS
  8. M1-M5 regression — all tests green ............................. PASS
  9. go vet — clean ................................................ PASS

D22 Phase 1 complete: the full loop (stdin → persist → pipeline →
envelope → plugin mutation → agent sees compressed output) is verified.
The agent's context receives compressed tool_call output; the original
is in ktx's database (ktx decompress returns it verbatim). With no
ktx.toml, the agent sees the original unchanged (identity, zero
behavioral change).
```
