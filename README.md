# system-ledger

**Evidence-backed system maps and change impact analysis for engineers inheriting unfamiliar systems.**

System Ledger is a local-first Go CLI that turns declarative source evidence
into an inspectable architecture graph. It helps answer what changes for an
asset, who owns it, and which direct links could break. The local SQLite ledger
retains a source path and locator for every asset and relationship.

It makes no network calls, runs no hosted service, and has no LLM, embeddings,
or external data dependency.

## See it work

The demo scans the checked-in multi-service example, builds conservative links,
then follows an operation to its table with owner and evidence details.

![Terminal recording: scan and build a System Ledger project, inspect its summary, find the listProducts to product path, then confirm doctor reports a healthy project.](docs/assets/demo.gif)

[Static final frame](docs/assets/demo-preview.png) ·
[Accessible command transcript](docs/assets/demo-transcript.txt) ·
[Reproduce the recording](docs/recording.md)

## How it works

<img src="docs/assets/system-ledger.svg" alt="System Ledger processing map: OpenAPI, AsyncAPI, SQL, and service metadata flow into a local SQLite evidence graph, queried by explain, impact, path, doctor, and verify." width="100%">

The diagram is a processing and dependency model, not a claim to discover
runtime calls or data lineage.

## Quick start

**Requirements:** Go 1.23 or newer.

### Install for your own project

```sh
go install github.com/Siddhant-K-code/system-ledger/cmd/system-ledger@latest
system-ledger --version
```

Initialize an existing source directory, add service roots to the manifest, and
run the scan/build cycle:

```sh
system-ledger init --project /path/to/system
# Edit /path/to/system/system-ledger.yaml
system-ledger scan --project /path/to/system
system-ledger build --project /path/to/system
system-ledger summary --project /path/to/system
```

### Clone and run the included demo

Start from an empty working folder. The checked-in example has two services,
owners, OpenAPI documents, and SQL tables:

```sh
git clone https://github.com/Siddhant-K-code/system-ledger.git
cd system-ledger
go build -o bin/system-ledger ./cmd/system-ledger

project="$(mktemp -d)"
cp -R examples/multi-service/. "$project/"

./bin/system-ledger scan --project "$project"
./bin/system-ledger build --project "$project"
./bin/system-ledger summary --project "$project"
./bin/system-ledger path --project "$project" listProducts product
./bin/system-ledger doctor --project "$project"
./bin/system-ledger verify --project "$project"

rm -rf "$project"
```

`init` creates a non-destructive `system-ledger.yaml` and the local ledger path
`<project>/.system-ledger/ledger.db`. Add one project-relative source root per
service:

```yaml
version: 1
services:
  - name: catalog-service
    owner: commerce-platform
    source: services/catalog
    domain: commerce
    tags: [api, catalog]
```

## What the CLI reports

| Task | Command | Result |
| --- | --- | --- |
| Discover evidence | `scan` | Deterministically finds supported source files and maps them to configured services. |
| Construct safe links | `build` | Rebuilds only conservative derived links. |
| Orient yourself | `summary` | Shows services, owners, assets, relationships, timestamps, and validation warnings. |
| Inspect an asset | `explain <name>` | Shows attributes, ownership, direct links, and source locators. |
| Assess direct change impact | `impact <name>` | Shows an asset's direct incoming and outgoing links. |
| Trace a dependency | `path <from> <to>` | Finds the deterministic shortest directed path, with relationship origin and evidence. |
| Diagnose project health | `doctor` | Checks manifest, roots, source drift, and whether a build is current. |
| Gate automation | `verify` | Enforces ledger, service-root, and evidence integrity. |

Project commands accept `--project <directory>` and `--db <path>` when an
explicit database location is needed. `--version` prints the build version and
does not take project flags. `init`, `explain`, `impact`, `path`, `summary`,
`doctor`, and `verify` accept `--format text|json`; JSON is raw, stable, and
machine-readable. Use each command's `--help` for its complete flag contract.

Example path output:

```text
$ system-ledger path listProducts product
Path: operation "listProducts" -> table "product"
1. outgoing references_schema [declared] schema "Product" (owner: catalog-service)
   services/catalog/openapi.yaml @ paths./products.get
2. outgoing matches_table_name [inferred] table "product" (owner: catalog-service)
   services/catalog/openapi.yaml @ normalized-name:product
   services/catalog/schema.sql @ normalized-name:product
```

## Evidence rules and supported input

| Source | Extracted facts | Relationship origin |
| --- | --- | --- |
| OpenAPI 3 JSON/YAML | APIs, operations, component schemas, local schema references | `declared` |
| AsyncAPI 2.x/3.x JSON/YAML | Documents, channels, inline publish/subscribe messages, component schemas, local payload references | `declared` |
| SQL `.sql` | Conventional `CREATE TABLE` identifiers | Assets only |
| `system-ledger.yaml` | Service name, owner, source root, optional domain and tags | `source-derived` |
| `build` | Exact normalized schema/table name matches | `inferred` |

The renderer never treats an inferred link as declared. It does not make fuzzy
matches, singularization guesses, semantic guesses, or producer/consumer
claims from prose. Every reported relationship includes the source evidence
that caused it.

`scan` limits traversal to the project root and ignores `.git`,
`.system-ledger`, `node_modules`, and `vendor`. Service roots must remain
inside the project. SQL support intentionally excludes columns, constraints,
views, procedures, dynamic SQL, and vendor-specific extensions. AsyncAPI
support intentionally covers local component schema references and inline
channel messages only.

## Operations and verification

Run `scan` after source or manifest changes, then `build`. `doctor` exits zero
for a healthy project and advisory warnings such as an unbuilt recent scan. It
exits nonzero for broken configuration, missing service roots, source drift, or
ledger failures. `verify` is the strict CI gate and exits nonzero for every
validation issue.

```sh
system-ledger doctor --project . --format json
system-ledger verify --project . --format json
```

## Develop

```sh
go test ./...
go vet ./...
go build ./cmd/system-ledger
```

Contributions should preserve deterministic ordering, local provenance, and
explicit inference rules. Please include a focused fixture or test when
extending an extractor or graph rule.

## License

[MIT](LICENSE).
