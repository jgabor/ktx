# Database migrations

KTX migrates its SQLite database automatically when a command first opens it.
Keep a database backup before installing a new binary when rollback matters.

## Codex record identity

Codex tool calls are stored under KTX-owned IDs containing exactly 16 lowercase
letters. A usable Codex runtime tuple produces the same KTX ID on retries;
runtime-shaped values such as `exec-*` are not stored as primary keys or aliases.

On upgrade, KTX rewrites eligible historical noncanonical Codex source IDs,
including `exec-*` IDs, and their dependent compressed-record references in one
transaction. Rows that cannot be attributed unambiguously to Codex are left
unchanged. A collision or conflicting mapping stops the migration with an error
and leaves the database unchanged. Reopening a successfully migrated database
is a no-op.

Commands previously emitted with an `exec-*` ID are intentionally unsupported
after migration and return not found. KTX does not retain compatibility aliases.
Use the canonical ID surfaced by current hook output or inspection commands:

```bash
ktx read <canonical-id>
ktx get <canonical-id>
ktx compress <canonical-id>
```

`ktx decompress <compressed-id>` continues to return the original source bytes
exactly; migrated compressed records point to the canonical source ID.
