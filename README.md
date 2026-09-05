# system-ledger

Evidence-backed system maps and change impact analysis for multi-repository systems.

`system-ledger` is a local-first, self-hostable Go CLI. It reconstructs a small,
auditable system map from source artifacts: OpenAPI documents and SQL DDL. Every
asset and relationship is accompanied by file and locator provenance in a local
SQLite database. The CLI makes no network calls, uses no embeddings, and has no
LLM or hosted-service dependency.

## Quick start

```sh
go run ./cmd/system-ledger ingest internal/ledger/testdata/demo
go run ./cmd/system-ledger build
go run ./cmd/system-ledger explain listPets
go run ./cmd/system-ledger impact Pet
go run ./cmd/system-ledger verify
```

The default ledger path is `.system-ledger/ledger.db`. Pass `--db path/to/ledger.db`
to any command to use a different local database.

## Commands

```text
system-ledger ingest <directory>  Parse OpenAPI JSON/YAML and SQL DDL.
system-ledger build               Add deterministic inferred links.
system-ledger explain <name>      Describe an asset and its direct links.
system-ledger impact <query>      Show a direct dependency graph.
system-ledger verify              Check ledger and source integrity.
```

Each command supports `--help`.

## v0 extraction and inference rules

- **OpenAPI:** OpenAPI 3 documents in `.json`, `.yaml`, or `.yml` are recognized.
  The ledger records the API document, operations, and component schemas.
  Local `#/components/schemas/...` references in operations become declared
  `references_schema` links; API-to-operation links are declared as
  `contains_operation`.
- **SQL:** `.sql` files are scanned for conventional `CREATE TABLE` statements,
  including optional `IF NOT EXISTS`, temporary tables, quoted identifiers, and
  simple schema-qualified names. v0 intentionally does not parse columns,
  constraints, views, stored procedures, vendor-specific dynamic SQL, or dialect
  extensions. Unsupported constructs are not guessed.
- **Inference:** `build` only adds `matches_table_name` links when a schema name
  and table leaf name match after lowercasing and removing non-alphanumeric
  characters. These links are marked `inferred`; all parser-observed links are
  marked `declared`. No fuzzy matching, singularization, or semantic guesses are
  performed.

`ingest` replaces extracted content with a deterministic scan of the selected
directory, so rerunning it is idempotent. Run `build` after every ingest because
inferred links are deliberately a separate, reproducible step.

## Data and integrity model

The SQLite ledger has `sources`, `assets`, `relationships`, and `evidence`
tables, with built-in schema migrations. Assets and links have direct evidence.
For inferred schema/table matches, evidence records both contributing source
files. `verify` checks foreign keys, evidence coverage, and whether source files
remain present and byte-identical to what was ingested; it exits nonzero on any
failure.

## Development

```sh
go test ./...
go build ./cmd/system-ledger
```
