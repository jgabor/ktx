# Historical campaign archive

This directory preserves exact source, scripts, locks, and proof snapshots from
one external campaign. It is evidence, not maintained tooling. The `.go.txt`
and `.sh.txt` suffixes keep archived source out of package discovery and routine
execution.

[`manifest.json`](manifest.json) maps every former path to its archived path and
unchanged SHA-256 digest. It also links the current
[correction](../../evidence/skillsbench-campaign-errata.json), raw authority,
immutable historical publications, and the
[sanitized proof derivative](../../evidence/skillsbench-proof-derivative.json).
The derivative exposes bounded tracked facts without replacing any original
snapshot or campaign byte.

## Manual recovery boundary

Archived procedures are not fresh-checkout checks. Recovery requires all of the
following:

- a disposable checkout;
- restoration to the former paths in `manifest.json`;
- the exact ignored inputs required by the procedure, up to all 927 raw files;
  and
- any historical machine-local root required by that procedure.

Checksum manifests still name former paths and are not current-tree commands.
Recorded source roots are provenance, not authority; an empty or unavailable
root only makes its specific historical procedure unavailable. Original proof
snapshots also retain bounded checkout paths and temporary roots. Do not rewrite
those bytes or claim that they are sanitized.

One exception predates publication: home-directory paths in
`external-validation/evidence/contract-repair/strace.log` were replaced with
`~`. Its SHA-256 in `SHA256SUMS`, `manifest.json`, and the proof derivative
records the original bytes and no longer matches the archived file.

Routine verification uses the maintained tracked-only check under
[`tools/bench/external-identity/`](../../external-identity/). Do not restore or
run archived procedures merely to make a fresh checkout pass.
