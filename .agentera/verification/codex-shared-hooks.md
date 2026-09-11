# Codex shared-hook mutation evidence

Scope: plan `hljjrlykxb`, task `mebgweiffm`; ledger A3 and S5 only.
Implementation and verification recorded 2026-09-07. The task remains
`in_progress`; this is not task evaluation, lifecycle completion, an audit,
or decision satisfaction.

## Changed boundaries

- `internal/cli/init_codex_mutation*.go` holds a Linux directory `flock` over
  read/merge/publish or removal. Acquisition is bounded to five seconds. The
  directory lock survives replacing or removing the document; there is no
  replaceable lock-file inode or persistent sidecar. Missing-parent uninstall
  does not create directories. Non-Linux mutation is explicitly unsupported.
- `internal/durablefile/write.go` retains the existing staged-file write, sync,
  close and rename sequence. Its small `WriteChecked` variant checks immediately
  before rename; existing `Write` callers keep their contract. Codex checks the
  observed document's identity, content, permission bits and modification time,
  plus parent-directory identity, before publication or deletion.
- `internal/cli/init_codex.go` rejects malformed JSON and malformed managed
  container shapes without rewriting them. Unknown fields and exact JSON number
  values survive merges. Uninstall no longer deletes a sole unrelated top-level
  field. Outer matcher metadata survives removal of its last owned inner hook.
- Ownership still accepts the exact current canonical pinned command. Relocated
  hooks require the unmodified generated inner shape: exactly command, type and
  ktx status-message fields; the command must round-trip through the generator's
  single-quoted clean absolute-path grammar followed by ` hook`. No executable
  probing, hashing, extra Codex field, old command alias or substring deletion
  was added. Relocation refresh also removes duplicates when a current command
  is already present. Other commands and siblings remain.

## Regression observations

Before implementation, the initial regressions observed:

- Interrupted install truncated a 90,128-byte shared document to 4,096 bytes.
- Install completed despite another process holding the directory lease.
- Show missed a relocated generated hook, reinstall retained its stale command,
  and uninstall left that stale hook behind.
- Null documents and non-object/non-array managed containers were rewritten.
- The initial uninstall fixture also exposed deletion of a sole unrelated
  top-level field; its corrected interrupted-write fixture retains two fields.

A separate negative-control run temporarily bypassed the new lease and restored
direct write/remove behavior, then restored the implementation immediately:

- Interrupted install and uninstall each left 4,096 bytes instead of the
  original 90,142 and 90,345 bytes respectively.
- The 24-process install/remove/update case lost seven of eight independent
  document updates. With coordination restored, all eight survive.
- A noncooperating subprocess edit was overwritten; an external edit was deleted
  on removal. Both operations reject the edits with checks restored.
- Install/removal bypassed all twelve injected pre-publication failure checks.

The final tests also cover injected pre-rename interruption and rename failure,
same-byte inode replacement, permission changes, document symlink substitution,
parent-directory replacement, direct symlink rejection, mode preservation,
large unknown numeric fields, custom command rejection, and shell-quoting
round trips. These are controlled fault injections, not a power-loss test.

`TestCodexSharedLocalCLI` performs nine local CLI invocations using two actual
copies of `build/ktx`: one in a quoted/metacharacter-containing path and one with
a different filename, plus a symlink to the second executable. It verifies show
without inode/mtime/content changes; uninstall from the relocated binary before
reinstall; duplicate collapse and canonical pinning through the executable
symlink; merged sibling/unknown field and mode preservation; and malformed-file
rejection. Every CLI invocation has a fresh disposable HOME, XDG and temporary
environment, with the shared project config confined to the test's owned tree.
Neither executable is used to run a hook or start Codex.

## Checks and results

Each verification entrypoint allocated fresh HOME, XDG data/config/cache/state,
and temporary roots, unset `KTX_DB`, and used `GOPROXY=off`, `GOSUMDB=off` and
`GOTOOLCHAIN=local`. Existing Go build/module caches were external tool caches,
not application data. No provider, live Codex runtime, ambient home config or
live database was used.

- `mage build`: passed before local CLI use.
- `go test ./internal/cli ./internal/durablefile -run
  'TestCodexShared|TestRunInitWith_Codex|TestWrite' -count=1`: passed.
- After a fresh local build, `KTX_CODEX_CLI=<checkout>/build/ktx go test -race
  ./internal/cli ./internal/durablefile -run
  'TestCodexShared|TestRunInitWith_Codex|TestWrite' -count=1`: passed, including
  the local CLI test rather than its normal opt-in skip.
- First `mage check`: tests, 14 Python readiness-verifier cases, required
  generated-adapter overlap and vet passed; lint found one boolean-expression
  style issue in the new test. It was corrected.
- Final `mage check`: passed all gates, lint `0 issues`; verification boundary
  `380d1cde66cd1a97`, `forbidden=0`, `ktx_db=unset`.

## Limits and handoff

Participating editors must lock the same resolved parent directory for their
entire read/update. Observed noncooperating edits abort, but another writer can
still race the final check-to-rename/remove window. No filesystem CAS or
adversarial filesystem confinement is claimed. Syncing the staged file does not
promise power-loss durability of the directory entry. Generated-shaped copied
hooks are managed by syntax and metadata, not authenticated binary identity;
modified relocated inner hooks are conservatively left user-owned.

No new configuration field, input cap or schema was introduced. Current SDK
configuration research was therefore unnecessary. Go documentation was consulted
for the local durable-file package and standard-library filesystem/lock APIs.
No hard implementation or verification blocker remains. Task status, final
evaluation and commits remain with the assigned owner.
