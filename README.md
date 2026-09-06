# system-ledger

**Evidence-backed system maps and change impact analysis.**

System Ledger is a local-first, self-hostable Go CLI for reconstructing a
trustworthy architecture map from code and declarative artifacts. Point it at a
service repository or a small multi-service checkout to answer:

- What directly changes for an asset or request?
- Which configured service owns the affected source?
- What direct API, schema, table, and ownership links could break?

The SQLite ledger is local. Source files and deterministic rules are the
authority: System Ledger makes no network calls and has no hosted service,
embeddings, or LLM-provider dependency.

## Install

```sh
go install github.com/Siddhant-K-code/system-ledger/cmd/system-ledger@latest
```

Or run it from a clone with `go run ./cmd/system-ledger`.

## Five-minute quick start

Initialize from the repository root. This creates `system-ledger.yaml` only if
it is absent and creates the predictable local database at
`.system-ledger/ledger.db`.

```sh
cd your-system
system-ledger init
```

Describe each service with a project-relative source directory:

```yaml
# system-ledger.yaml
version: 1
services:
  - name: catalog-service
    owner: commerce-platform
    source: services/catalog
    domain: commerce
    tags: [api, catalog]
  - name: orders-service
    owner: fulfillment-platform
    source: services/orders
    domain: fulfillment
    tags: [api, orders]
```

Scan source evidence, construct conservative derived links, then inspect it:

```sh
system-ledger scan
system-ledger build
system-ledger summary
system-ledger impact Product
system-ledger path listProducts product
system-ledger doctor
system-ledger verify
```

For a runnable example, use the checked-in multi-service fixture:

```sh
cd examples/multi-service
go run ../../cmd/system-ledger init
go run ../../cmd/system-ledger scan
go run ../../cmd/system-ledger build
go run ../../cmd/system-ledger impact Product
```

The final command reports the direct `Product -> product` inferred match, its
incoming API operation, ownership by `catalog-service`, relationship origins,
and source/locator evidence.

## Commands and project scope

| Command | Purpose |
| --- | --- |
| `init [directory]` | Idempotently creates the manifest and ledger location. |
| `scan` | Discovers supported files under the project and applies service ownership. |
| `ingest <directory>` | Compatibility mode for one in-project source directory without service association. |
| `build` | Rebuilds only deterministic inferred links. |
| `summary` | Lists services, asset counts, relationship counts, and validation warnings. |
| `explain <name>` | Shows an asset's attributes, owner, direct links, and evidence. |
| `impact <query>` | Shows the direct, evidence-backed dependency graph for an asset. |
| `path <from> <to>` | Finds the deterministic shortest path across direct ledger relationships. |
| `verify` | Checks relational integrity, service roots, source provenance, and evidence. |
| `doctor` | Diagnoses manifest, scan/build freshness, service roots, and evidence drift. |

Every command accepts `--project <directory>`; it defaults to the current
directory. The default database is `<project>/.system-ledger/ledger.db`.
`--db <path>` remains available for scripts and existing usage. `explain`,
`impact`, `path`, `summary`, `verify`, `doctor`, and `init` support
`--format text|json`; JSON uses stable structs and deterministic ordering for
automation. Human output honors `--color auto|always|never` and `NO_COLOR`;
auto styling is enabled only for a terminal, so CI output stays plain.

`system-ledger --version` prints the build version (`dev` for local builds).
Release builds may set it with `go build -ldflags '-X main.version=vX.Y.Z'`.

`scan` only traverses the project root, ignores `.git`, `.system-ledger`,
`node_modules`, and `vendor`, and does not follow directories outside the
project. Service `source` values must be existing directories inside the
project. `ingest` likewise rejects a directory outside `--project`.

## Evidence model and supported sources

Each extracted asset and relationship records a source path and locator in
SQLite. Paths are project-relative. `verify` confirms the configuration's
source roots still exist, the stored sources remain under the project root, and
their bytes still match the last scan. It exits nonzero with remediation hints
when an issue is found.

Current supported evidence:

- **OpenAPI 3 JSON/YAML:** API documents, operations, component schemas,
  API-to-operation containment, and local
  `#/components/schemas/...` operation references.
- **SQL `.sql` files:** conventional `CREATE TABLE`, optional `IF NOT EXISTS`,
  temporary tables, quoted identifiers, and simple schema-qualified names.
- **Manifest services:** explicit service name, owner/team, source, optional
  domain, and tags. Assets in a configured source directory receive a
  `source-derived` `owns_asset` link with manifest evidence.
- **AsyncAPI 2.x/3.x JSON/YAML:** document assets, channels, inline
  `publish`/`subscribe` messages, component schemas, and local schema
  references. System Ledger does not infer producers or consumers from channel
  names or descriptive text.

`build` adds one deliberately narrow inference: a schema and SQL table have a
`matches_table_name` edge only when their names exactly match after lowercasing
and removing non-alphanumeric characters. Parser-observed links are
`declared`, manifest associations are `source-derived`, and derived links are
`inferred`. System Ledger does not make fuzzy, semantic, singularization, or
cross-file guesses.

SQL columns, constraints, views, stored procedures, dynamic SQL, and
vendor-specific extensions are intentionally unsupported in this release.
Non-OpenAPI YAML/JSON candidate files are counted as skipped by `scan`; invalid
supported OpenAPI documents fail explicitly rather than being guessed.

## Reproducibility

Re-running `scan` replaces extracted material with a deterministic file walk
and deterministic ordering. Re-running `build` replaces only inferred edges.
The manifest and source content are evidence, so outputs remain inspectable
and `verify` catches drift before results are trusted.

## Operational checks

`doctor` exits zero for a healthy ledger and for advisory warnings (for example,
when `scan` completed but `build` has not run). It exits nonzero for broken
manifests, invalid service roots, missing or changed evidence, and relational
integrity failures. Each failing text check includes the exact repair command.
`verify` is the stricter CI gate and exits nonzero for any validation issue.

Example:

```text
$ system-ledger summary
Sources: 5 | Services: 2 | APIs: 2 | Operations: 2 | Schemas: 2 | Tables: 2
Services:
- catalog-service (owner: commerce-platform, source: services/catalog)

$ system-ledger path listProducts product
Path: operation "listProducts" -> table "product"
1. outgoing references_schema [declared] schema "Product" (owner: catalog-service)
2. outgoing matches_table_name [inferred] table "product"

$ system-ledger doctor
OK  manifest   Manifest is valid.
OK  evidence   Ledger evidence and service roots are valid.
OK  build      Ledger is built from the latest scan.
```

## Roadmap

**Current:** local OpenAPI and SQL DDL reconstruction, service ownership,
direct impact maps, provenance, integrity verification, and text/JSON reports.

**Future ideas (not implemented):** additional deterministic parsers,
cross-repository aggregation, richer declared dependency formats, and an
optional independently-auditable extraction layer. Hosted ingestion,
embeddings, and LLM-provider integrations are intentionally out of scope.

## Development

```sh
go test ./...
go build ./cmd/system-ledger
```
