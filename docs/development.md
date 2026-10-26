# Developing and benchmarking ktx

This page is for people who build ktx from source, run its checks, or measure
compression with the built-in benchmarks. For everyday use, start with the
[README](../README.md).

## Build and verify

Build from a checkout with Mage. The binary lands in `build/ktx`:

```bash
mage build           # build build/ktx
mage opencodePackage # build the OpenCode package in plugins/opencode
mage verify          # the same checks CI runs
mage replayFixture   # quick offline check of the retained replay fixture
```

`mage opencodePackage` installs the OpenCode package's locked dependencies with
Bun and builds its sidebar target, so `plugins/opencode` holds both the
`./server` and `./tui` targets for a folder install. `mage check` runs it
before the package's Node and Bun tests.

`mage install` puts a development ktx on `PATH`. When OpenCode already loads
ktx, globally or in the current project, it also builds the package and
points OpenCode at this checkout's `plugins/opencode` folder.
`mage installRelease` installs the latest release instead and points OpenCode
at `@jgabor/ktx` pinned to that release. It stops before installing anything
when the release has no OpenCode package or npm doesn't have it yet. Both
targets remove only ktx's own plugin entries and leave exactly one in
OpenCode's server and TUI config.

Without Mage, use `go build -o build/ktx ./cmd/ktx`. Avoid a bare
`go build ./cmd/ktx`, which drops the binary in the repository root.

`mage verify` runs `mage check` (test, vet, lint, and build), `govulncheck`,
and the portable replay gate. CI runs this same command on every push and pull
request. You need both `golangci-lint` and `govulncheck` installed.

The checks also need Node and Bun. Tests run the OpenCode package's server
target, `plugins/opencode/server.ts`, read from the checkout, on Node 22.18 or
newer (23.6 or newer on the 23 line), and the generated-adapter gate runs it on
Bun, which OpenCode uses to load plugins. The ktx binary carries no copy of the
plugin; a test fails if it links a package from `plugins/`. Mage resolves both
executables before it isolates the run and passes them on as `KTX_TEST_NODE`
and `KTX_TEST_BUN`, so version-manager shims keep working. Set either variable
to an absolute path to pick a specific executable. Without Node or Bun,
`mage check` fails before it runs the plugin tests or the gate. If the first
`node` on `PATH` is older than 22.18, the plugin tests fail; set
`KTX_TEST_NODE` to a newer executable.

### Database isolation

Development and CI checks never touch your everyday ktx database. The standard
Mage targets give each run its own throwaway `HOME` and XDG folders, unset
`KTX_DB`, audit every database access, and keep reusable language and tool
caches separate. The
[database safety contract](reference/database-safety.md) spells out the rules.

### Extra engineering evidence

For a deeper, optional check, run
`KTX_ENGINEERING_EVIDENCE_DIR=<new-directory> mage engineeringVerify` with a
new directory. It keeps component, real-process load, and blocked-decompression
reports in a hash manifest and requires correctness, exact reconciliation, and
supported-lock checks. It does not add a timing gate.

### Schema release gate

Schema releases have their own production-scale gate. It is not part of
`mage check`. First prepare the disposable 3.2 GB fixture as described in the
[schema readiness contract](reference/schema-readiness.md#readiness-release-budget),
then run:

```bash
env -u KTX_DB \
  KTX_SCHEMA_RELEASE_WORK_ROOT="$root/work" \
  KTX_SCHEMA_RELEASE_REPORT="docs/evidence/schema-readiness/schema-readiness-release.json" \
  mage schemaReleaseGate
```

The named report counts as a pass only after a complete run. A failed attempt
is written to
`docs/evidence/schema-readiness/schema-readiness-release.json.attempt.failed.json`
and never replaces the passing report.

## Releasing

A release is a signed tag `vX.Y.Z` on the commit to release. ktx reports its
version from Go build info, so `go install github.com/jgabor/ktx/cmd/ktx@vX.Y.Z`
builds and reports that commit as `vX.Y.Z`. Pushing the tag runs CI on the
same commit. After `mage verify` passes, the `opencode-package` job in
`.github/workflows/ci.yml` packs the OpenCode package from that commit as
`@jgabor/ktx@X.Y.Z`, the pin `ktx init opencode` installs for ktx `vX.Y.Z`,
and the `publish-opencode-package` job publishes that tarball.
`plugins/opencode/package.json` has no `version`; the pack job sets it from the
tag. Only a pushed tag that is exactly `vX.Y.Z` reaches the publish job. Any
other `v` tag fails the pack job, so nothing is published.

On every other push and pull request, the pack job packs the package at a
prerelease version from `git describe --long` (for example `0.1.0-5-g5c06df3`,
or `0.1.0-0-g5c06df3` on the tagged commit itself, so it never matches a
published release) and runs `npm publish --dry-run`. Both paths first run
`mage opencodePackageContents`. It fails when npm would leave out the
`./server` or `./tui` target, a file either one imports, the README or the
license. `mage check` runs the same check, and you can run it yourself after
`mage opencodePackage`.

The publish job runs only for a pushed `v` tag, in the `npm-publish` GitHub
environment, and is the only job that gets an OIDC token (`id-token: write`).
It publishes through
[npm trusted publishing](https://docs.npmjs.com/trusted-publishers): npm
exchanges that token for a short-lived publish credential, so no npm token is
stored in GitHub. The trusted publisher names the environment, and the
environment admits only `v*` tags, so npm itself refuses a publish from any
other ref. Trusted publishing needs npm 11.5.1 or newer, which the job checks.
From a public repository it also adds a provenance attestation without
`--provenance`. The publish job does not rebuild: it publishes the tarball the
pack job built and checked on the tagged commit.

### One-time npm setup

1. Use the npm account that owns the `@jgabor` scope, with two-factor
   authentication turned on.
2. Create the `npm-publish` environment in the GitHub repository under
   Settings, Environments, New environment. Under Deployment branches and
   tags, choose Selected branches and tags and add one rule of type Tag with
   the pattern `v*`, so only a `v` tag can run a job in it.
3. Publish the first version by hand. npm adds a trusted publisher only to a
   package that is already on the registry
   ([`npm trust`](https://docs.npmjs.com/cli/v11/commands/npm-trust): "The
   package you're configuring must already exist on the npm registry"), and
   `@jgabor/ktx` is not there yet. Commit the changelog move from step 1 of
   [Each release](#each-release), then publish the first release from its tag
   before you push the tag:

   ```bash
   git tag -s vX.Y.Z -m "ktx vX.Y.Z"
   git checkout vX.Y.Z
   mage opencodePackage
   mage opencodePackageContents
   cd plugins/opencode
   npm pkg set version=X.Y.Z
   npm login
   npm publish --ignore-scripts
   git restore package.json
   cd ../..
   git push origin vX.Y.Z
   ```

   The tag's CI run finds `@jgabor/ktx@X.Y.Z` on npm and skips its own
   publish. This hand-published version has no provenance attestation.

4. Add the trusted publisher on npmjs.com under the package's Settings,
   Trusted publishing, GitHub Actions:
   - Organization or user: `jgabor`
   - Repository: `ktx`
   - Workflow filename: `ci.yml`
   - Environment: `npm-publish`
   - Allowed actions: allow `npm publish`. Configurations created after
     September 3, 2026 allow only `npm stage publish` until you select it.

   With npm 11.15.0 or newer, `npm trust github @jgabor/ktx --repo jgabor/ktx
--file ci.yml --env npm-publish --allow-publish` does the same. Add it no more than two days
   before you push the next release tag. A new configuration expires unless its
   first publish succeeds within two days; delete an expired one and add it
   again.

5. After the first publish through CI succeeds, set the package's publishing
   access to require two-factor authentication and disallow tokens, and
   revoke any token you made for the first publish. Trusted publishing keeps
   working.

### Each release

1. Move the `[Unreleased]` entries in `CHANGELOG.md` under the new version and
   commit.
2. Tag the commit and push the tag:

   ```bash
   git tag -s vX.Y.Z -m "ktx vX.Y.Z"
   git push origin vX.Y.Z
   ```

3. Wait for the tag's CI run. The `opencode-package` job runs only after
   `verify` passes, and `publish-opencode-package` only after it. Re-running
   the publish job after a successful publish skips the publish.

### Confirm the package and binary versions match

```bash
npm view @jgabor/ktx version     # X.Y.Z
go install github.com/jgabor/ktx/cmd/ktx@vX.Y.Z
ktx --version                    # vX.Y.Z
ktx init opencode --show         # the entry is @jgabor/ktx@X.Y.Z and matches this ktx
```

The package page on npmjs.com shows the provenance attestation, with the
commit and the workflow run that published it. That commit should be the
commit `git rev-list -n 1 vX.Y.Z` prints. `mage installRelease` also checks
that npm has the package for the latest release before it changes anything.

## Benchmarking

Everything a benchmark needs runs inside the ktx binary: stages, task loading,
scoring, and summaries. No external scripts or separate evaluator are needed.
Named pipelines, tasks, methodologies, and corpus fixtures live in
[`tools/bench`](../tools/bench).

### Screen configurations offline

Compare complete configurations without a database, credentials, or model
calls:

```bash
./build/ktx bench screen \
  --population tools/bench/evidence/configuration-screen-population-v1.json \
  --config tools/bench/pipelines/passthrough.toml \
  --config tools/bench/screen-configs/keep-ends-73-1.toml \
  --anchor-config tools/bench/screen-configs/keep-ends-100-50.toml \
  --format json
```

Repeat `--config` for each configuration. The JSON holds only aggregate
measurements, hashes, and opaque configuration IDs. External plugins are
rejected, so screening never sends source content to another process.

### Run a live comparison

Live comparisons call a real model, so they cost money. Start with the free
preflight, then run the comparison:

```bash
./build/ktx bench compare --preflight --build --spec prompt-cache-token-hit-rate --task crossfile-nav --format json

./build/ktx bench compare \
  --spec prompt-cache-token-hit-rate \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task crossfile-nav \
  --baseline passthrough \
  --candidate conservative-a \
  --cache-control observed \
  --warmup 1 \
  --rounds 5
```

Preflight returns before task loading. It makes no model call, needs no
login, opens no ktx database, and creates no experiment files. With `--build`
it can use the network and changes Docker image and build-cache state. Compare
and rank create archives, use fresh isolated databases, call the provider over
the network, and can incur charges.

Compare and rank need an explicit registered `--spec`. Focused command help
lists every current spec, task, baseline, and candidate ID. Current
prompt-cache comparisons use methodology v6, summary schema 5, and
`cache-reuse-v2`. They make no provider-internal cache claims. The
[canonical benchmark workflow](guides/pipelines.md#canonical-benchmark-workflow)
walks through the full lifecycle.

### Inspect experiments

Experiments keep manifests, attempts, pricing snapshots, and deterministic
summaries under `.ktx/experiments/<id>`. Refer to them by ID only:

```bash
ktx bench list
ktx bench show <experiment-id>
ktx bench validate <experiment-id>
ktx bench summarize <experiment-id>
ktx bench validate <experiment-id>
```

List, show, and validate only read the archive and never open the ktx
database. Summarize rewrites the derived `summary.json` without calling a
provider or opening the ktx database.

Archives can contain repository snapshots, prompts, outputs, streams, stderr,
identifiers, databases, and secrets. Review anything you derive from them
before sharing. Historical reports are static evidence; the current binary is
not guaranteed to accept their archives.

### Replay offline

Replay a recorded session without calling the agent again:

```bash
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to replay}"
ktx bench run --replay "$KTX_TARGET_SESSION_ID"
```

For a strict, portable check, export a benchmark capture to a file and replay
that file without the source database:

```bash
: "${KTX_OWNED_DB:?set this to an explicitly operator-owned database path}"
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to export}"
KTX_DB="$KTX_OWNED_DB" ktx bench export --output replay-v1.json "$KTX_TARGET_SESSION_ID"
ktx bench run --replay-file replay-v1.json
```

Exit codes: cost or cache drift is informational and exits 0, an integrity or
parser failure exits 4, and a missing session exits 2. File replay uses only
the fixture and a controlled built-in pipeline; any divergence fails. Fixtures
contain raw recorded stream data, so check them for secrets and personal data
before sharing. The fixture format and refresh steps are in the
[pipelines guide](guides/pipelines.md#portable-offline-replay).

## Where to go next

- [Documentation index](README.md) for every guide, contract, and report.
- [`ARCHITECTURE.md`](../ARCHITECTURE.md) for the design intent.
- [`AGENTS.md`](../AGENTS.md) for repository working conventions.
