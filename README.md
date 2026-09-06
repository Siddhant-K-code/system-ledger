# system-ledger

**Evidence-backed system maps and change impact analysis for engineers inheriting unfamiliar systems.**

System Ledger is a local-first Go CLI that turns declarative and Go source evidence
into an inspectable architecture graph. It helps answer what changes for an
asset, who owns it, and which direct links could break. The local SQLite ledger
retains a source path and locator for every asset and relationship.

It is **offline by default** and runs no hosted service. Scanning, graph building,
and reports never call a model. Optional, explicitly authorized OpenAI or
Anthropic assistance uses your own API key and model; it proposes explanations
without changing the deterministic ledger. No embeddings or external data
services are needed.

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
runtime calls or data lineage. The existing recording and diagram show the
declarative workflow; the Go source and optional assistance extensions are
described below.

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
| Preview optional assistance | `ask --asset <name> --dry-run '<question>'` | Shows the exact bounded evidence request without a key or network call. |

Project commands accept `--project <directory>` and `--db <path>` when an
explicit database location is needed. `--version` prints the build version and
does not take project flags. `init`, `scan`, `explain`, `impact`, `path`, `summary`,
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
| Go `.go` | Functions/methods, bounded static `net/http` registrations, direct calls, supported `database/sql` queries | `source-derived` |
| `system-ledger.yaml` | Service name, owner, source root, optional domain and tags | `source-derived` |
| `build` | Exact normalized schema/table name matches | `inferred` |

The renderer never treats an inferred link as declared. It does not make fuzzy
matches, singularization guesses, semantic guesses, or producer/consumer
claims from prose. Every reported relationship includes the source evidence
that caused it.

`scan` limits traversal to the project root and ignores dot-directories (including
`.git` and `.system-ledger`), `node_modules`, and `vendor`. It skips symlinks,
`_test.go`, generated Go files with the standard `Code generated ... DO NOT EDIT.`
marker, dotfiles (including `.env`), and conventional `credentials.*`,
`secrets.*`, and `service-account.*` / `service_account.*` files. It does not load
`.env` or execute application code, generators, build hooks, or dependency
downloads. Service roots and evidence paths cannot pass through symlinks.
Each candidate file is limited to 4 MiB, a scan to 10,000 candidates, and the
combined analyzed Go source to 64 MiB.
SQL DDL support excludes columns, constraints,
views, procedures, dynamic SQL, and vendor-specific extensions. AsyncAPI
support intentionally covers local component schema references and inline
channel messages only.

### Actual Go source evidence

`examples/go-http` is entirely synthetic. It demonstrates a route, its handler,
a helper, a literal SQL query, and a table defined in that service's DDL. It
also includes unsupported cases, intentionally reported as extraction gaps.
The detached maintenance code has no service mapping, so its table target is
deliberately unresolved even though a table with the same name exists elsewhere.

From this repository, build and run the Go example without executing the
example application:

```sh
go build -o bin/system-ledger ./cmd/system-ledger
./bin/system-ledger scan --project examples/go-http
./bin/system-ledger build --project examples/go-http
./bin/system-ledger path --project examples/go-http 'GET /reports' table:reports
./bin/system-ledger explain --project examples/go-http 'reports.*Server.loadReports'
./bin/system-ledger summary --project examples/go-http --format json
./bin/system-ledger doctor --project examples/go-http
./bin/system-ledger verify --project examples/go-http
```

The path follows `handles`, `calls`, `queries`, and `reads_table`, with
`reports-service` / `reporting-platform` ownership and Go/DDL evidence.
Selectors accept an exact name, canonical name, `kind:name` (such as
`table:reports`), or `id:<integer>`. Integer ledger IDs are local to a scan;
assistance uses separate stable identity hashes and source-hash-based evidence
identifiers.

Source-derived links are **static code observations**, not proof a route is
registered in production, a function runs, or a query reaches a particular
database at runtime. A directed path is a path through known facts, not
exhaustive change impact or complete lineage. Existing `matches_table_name`
links remain inferred candidates, not evidence of a query.

Declarations have package/directory/receiver-qualified identities and source
line evidence. Same-package direct calls across files are supported only when
binding is statically resolvable. A method's name alone never makes it an HTTP
registration or database query. Unresolved handlers, dispatch, wrappers, dynamic
patterns, and dynamic SQL remain coverage gaps rather than guessed edges.
`summary`, `explain`, and `doctor` surface these gaps separately from integrity
failures.

The bounded HTTP subset covers actual `net/http` imports (including aliases),
`Handle` / `HandleFunc`, proven `*http.ServeMux` receivers, `NewServeMux`, and
`DefaultServeMux`. Literal/constant patterns retain ordinary paths and Go 1.22
`METHOD /path` specificity. Named handlers, receiver methods, and
`http.HandlerFunc(named)` can resolve; anonymous handlers, wrappers, interfaces,
and promoted dispatch can remain gaps.
For `Handle`, a function-valued custom handler resolves through a proven
`ServeHTTP` method, not by assuming it invokes its underlying function.

All non-excluded files are inventoried, including platform-suffixed files;
neither build tags nor filename build constraints select a deployed build.
Explicit build tags generate a diagnostic. Conflicting declarations across
variants do not resolve by name alone. Imported nonstandard packages are not
loaded, and calls inside anonymous functions are not attributed to their
surrounding declaration. Generic specialization and interface dispatch remain
unresolved; type parameters do not fall back to same-named package declarations.
This is AST analysis, not a target build or a full type check.

Constant evaluation is memoized and bounded to 200,000 evaluation steps, depth
128, 64 KiB per string, 128 unique supporting origins per value, and 8 MiB
cumulative string allocation per analysis.
Exhaustion emits `go_evaluation_limit` and omits dependent facts rather than
expanding unbounded expressions.
Resolved route/SQL constants retain supporting source spans as well as their
use sites, so cross-file literal definitions have their own hashes in evidence.

The SQL slice recognizes proven `*database/sql.DB` / `*database/sql.Tx`
`Query`, `QueryRow`, and `Exec` calls and their `Context` variants. It resolves
literals, string constant aliases/concatenation, and conservatively known local
strings. A token-based bounded grammar supports single-table `SELECT`,
`INSERT ... VALUES`, `UPDATE ... SET`, and `DELETE`, simple predicates,
placeholders (`?`, `$1`, `$name`, `:name`), projection aliases, and basic
`ORDER BY` / `LIMIT` / `OFFSET` / `RETURNING` clauses. Joins, CTEs, subqueries,
unions, grouping, `DISTINCT`, upserts, multiple statements, dynamic SQL, and
ORM inference produce no table references. Dialect-specific executable comments
are unsupported, not silently treated as ordinary comments. DDL discovery remains
a limited top-level `CREATE TABLE name (...)` declaration extractor with simple
ASCII identifiers (optionally quoted and schema-qualified), not a database
schema validator. Quoted identifiers containing dots and `CREATE TABLE AS`
are not supported.

Table links require a supported SQL reference and exactly one matching scanned
DDL table inside the query's uniquely configured service. Identifiers must
match exactly, including any schema qualifier. An unqualified query is not
linked to a qualified DDL table by guessing the database search path.
Missing ownership, overlapping service roots, ambiguous tables, and references
outside the service stay unresolved. This scope is a manifest-based static
association; connection configuration and database search paths are not analyzed.

### Explicitly opt-in BYOK assistance

Provider and model defaults are optional and contain **no secrets**:

```yaml
version: 1
services:
  - name: reports
    owner: reporting-team
    source: services/reports
assistance:
  provider: openai
  model: YOUR_COMPATIBLE_MODEL_ID
```

`YOUR_COMPATIBLE_MODEL_ID` is a placeholder, not a working model name. Choose
an exact model ID available to your account that supports the provider's JSON
schema output format. There is no model allowlist, discovery request, automatic
fallback, or retry. Per-command `--provider` and `--model` override their
respective project defaults. A provider and a model are required for preview
and remote mode; neither needs a key for preview.

Provision `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` in your shell environment
using your normal secret manager. Do not paste keys into chat, command-line
arguments, the manifest, or source files. Only these standard environment
variables supply credentials; there is no `--api-key` flag.

All flags must come **before** the positional question (the CLI uses Go's
standard `flag` parser):

```sh
system-ledger ask --project /path/to/system --asset 'GET /reports' \
  --provider openai --model YOUR_COMPATIBLE_MODEL_ID --dry-run \
  'What would need to change to add a report field?'

# Review the preview and your organization's data-sharing policy before opting in.
system-ledger ask --project /path/to/system --asset 'GET /reports' \
  --provider openai --model YOUR_COMPATIBLE_MODEL_ID --allow-remote \
  'What would need to change to add a report field?'

# To use Anthropic instead, select --provider anthropic and a compatible model.
```

`ask` without `--dry-run` or `--allow-remote` fails locally. A configured model
and an existing key are **not consent**. Preview shows the actual JSON body and
official destination without authorization headers. Only the selected asset
and a bounded deterministic dependency neighborhood are included, with stable
IDs, owners, relationships and their origins, source locations/hashes, and
extraction gaps. It does not include source excerpts, comments, entire files,
SQL literal bodies, or arbitrary documents. Source and inventory drift require
a fresh scan; stale facts cannot be presented as current.

Context traverses at most four hops in either direction, excluding service
ownership and Go package-containment edges (owners are attached as metadata,
not used to pull in sibling assets). Limits are 32 assets, 64 relationships,
256 evidence locations, and 64 gaps. Context boundaries are reported; an
oversized evidence pack fails rather than silently sending extra material.
The question is limited to 8 KiB, the serialized request to 256 KiB, the HTTP
response to 1 MiB, and model JSON to 64 KiB. Requests allow at most 2,048 output
tokens and 30 seconds, support interruption, make zero retries, and refuse
redirects. Only the official HTTPS endpoints are exposed.

Even typed names, paths, ownership, and questions can be confidential.
Explicit opt-in is consent to this request, **not a guarantee the context is
safe to disclose**. Review the exact preview and provider retention, abuse
monitoring, account settings, and organizational obligations.
OpenAI requests use `store: false`; that is **not** a zero-data-retention
guarantee. See [OpenAI data controls](https://platform.openai.com/docs/guides/your-data)
and [Anthropic privacy information](https://privacy.claude.com/).

Provider replies are untrusted, **unverified proposed explanations requiring
review**. Suggestions reference only transmitted assets and evidence; unknown
references, invalid JSON, refusals, incomplete output, and tool requests are
rejected. Citation existence does not establish semantic correctness.
Suggestions, assumptions, and unanswered questions never become graph facts.
Responses include provider/model metadata and usage only when supplied, not
invented dollar costs or confidence scores. No output can execute a tool,
change a file, fetch a URL, or initiate another request.

Both protocol adapters are exercised with offline mock servers. Live
provider-account compatibility is untested; a mock response is not a live
demonstration. Providers can reject a model or schema; choose a compatible
model rather than expecting a silent fallback.

Troubleshooting: missing-key errors name the required environment variable;
401/403 mean authentication or permission must be corrected; 429 means rate
or quota limits; 5xx means provider failure. Timeouts and incomplete/refused
responses fail explicitly. Check account/model compatibility for schema
rejections. No response body, credential, or raw transport error is echoed on
failure, and no retry incurs a surprise additional request.

## Operations and verification

Run `scan` after source or manifest changes, then `build`. `doctor` exits zero
for a healthy project and advisory warnings such as an unbuilt recent scan. It
exits nonzero for broken configuration, missing service roots, source drift, or
ledger failures. `verify` is the strict CI gate and exits nonzero for every
validation issue.

A scan transaction replaces the source inventory, assets, ownership, diagnostics,
and relationships together. Parse failures leave the preceding ledger intact
(and visibly stale); removing or renaming files retracts old code facts on the
next successful scan, including when the last supported source is removed.
Every successful scan invalidates the previous build marker. Integrity checks
detect additions as well as changed hashes, removals, and symlink replacements.
Extraction gaps are advisories, not certification failures: successful `verify`
means recorded provenance is current, not that analysis coverage is complete.
Existing v2 ledgers migrate additively; rerun `scan` and `build` to populate the
new source-inventory checks.

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
