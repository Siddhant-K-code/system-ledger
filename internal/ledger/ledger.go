// Package ledger owns the local SQLite graph and deterministic extraction rules.
package ledger

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

const schemaVersion = 3

const sqlIdentifier = "(?:[A-Za-z_][A-Za-z0-9_$]*|\"[A-Za-z_][A-Za-z0-9_$]*\"|`[A-Za-z_][A-Za-z0-9_$]*`|\\[[A-Za-z_][A-Za-z0-9_$]*\\])"

var createTablePattern = regexp.MustCompile("(?is)^\\s*CREATE\\s+(?:TEMP(?:ORARY)?\\s+)?TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?(" + sqlIdentifier + "(?:\\s*\\.\\s*" + sqlIdentifier + ")*)\\s*\\(")

type Stats struct {
	Sources, APIs, Operations, Schemas, Tables int
	Services, CandidateFiles, SkippedFiles     int
	Functions, Routes, Queries, Gaps, Packages int
}

type Asset struct {
	ID            int64
	Kind, Name    string
	CanonicalName string
	Attributes    string
}

type Relation struct {
	ID                 int64
	FromID, ToID       int64
	Type, Origin       string
	FromKind, FromName string
	ToKind, ToName     string
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

// Migrate creates the built-in, versioned schema. It is safe to call before every command.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("ledger schema version %d is newer than this CLI supports", version)
	}
	if version == schemaVersion {
		return nil
	}
	if version == 2 {
		return applySourceSchema(db)
	}
	if version == 1 {
		if err := applyProjectSchema(db); err != nil {
			return err
		}
		return applySourceSchema(db)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE sources (
			id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, sha256 TEXT NOT NULL
		)`,
		`CREATE TABLE assets (
			id INTEGER PRIMARY KEY, source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
			kind TEXT NOT NULL, name TEXT NOT NULL, canonical_name TEXT NOT NULL, attributes TEXT NOT NULL DEFAULT '{}',
			UNIQUE(source_id, kind, canonical_name)
		)`,
		`CREATE TABLE relationships (
			id INTEGER PRIMARY KEY, from_asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
			to_asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
			relationship_type TEXT NOT NULL, origin TEXT NOT NULL CHECK(origin IN ('declared', 'inferred')),
			UNIQUE(from_asset_id, to_asset_id, relationship_type, origin)
		)`,
		`CREATE TABLE evidence (
			id INTEGER PRIMARY KEY, source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
			asset_id INTEGER REFERENCES assets(id) ON DELETE CASCADE,
			relationship_id INTEGER REFERENCES relationships(id) ON DELETE CASCADE,
			locator TEXT NOT NULL,
			CHECK ((asset_id IS NOT NULL AND relationship_id IS NULL) OR (asset_id IS NULL AND relationship_id IS NOT NULL))
		)`,
		`CREATE INDEX idx_assets_name ON assets(name, canonical_name)`,
		`CREATE INDEX idx_relationships_from ON relationships(from_asset_id)`,
		`CREATE INDEX idx_relationships_to ON relationships(to_asset_id)`,
		`CREATE INDEX idx_evidence_asset ON evidence(asset_id)`,
		`CREATE INDEX idx_evidence_relationship ON evidence(relationship_id)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply schema migration: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES (1)`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := applyProjectSchema(db); err != nil {
		return err
	}
	return applySourceSchema(db)
}

func applyProjectSchema(db *sql.DB) error {
	// SQLite cannot widen a CHECK constraint in place, so rebuild the two
	// dependent tables while foreign-key enforcement is temporarily disabled.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for migration: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE relationships_v2 (
			id INTEGER PRIMARY KEY, from_asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
			to_asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
			relationship_type TEXT NOT NULL, origin TEXT NOT NULL,
			UNIQUE(from_asset_id, to_asset_id, relationship_type, origin)
		)`,
		`INSERT INTO relationships_v2 SELECT id, from_asset_id, to_asset_id, relationship_type, origin FROM relationships`,
		`CREATE TABLE evidence_v2 (
			id INTEGER PRIMARY KEY, source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
			asset_id INTEGER REFERENCES assets(id) ON DELETE CASCADE,
			relationship_id INTEGER REFERENCES relationships_v2(id) ON DELETE CASCADE,
			locator TEXT NOT NULL,
			CHECK ((asset_id IS NOT NULL AND relationship_id IS NULL) OR (asset_id IS NULL AND relationship_id IS NOT NULL))
		)`,
		`INSERT INTO evidence_v2 SELECT id, source_id, asset_id, relationship_id, locator FROM evidence`,
		`DROP TABLE evidence`,
		`DROP TABLE relationships`,
		`ALTER TABLE relationships_v2 RENAME TO relationships`,
		`ALTER TABLE evidence_v2 RENAME TO evidence`,
		`CREATE INDEX idx_relationships_from ON relationships(from_asset_id)`,
		`CREATE INDEX idx_relationships_to ON relationships(to_asset_id)`,
		`CREATE INDEX idx_evidence_asset ON evidence(asset_id)`,
		`CREATE INDEX idx_evidence_relationship ON evidence(relationship_id)`,
		`CREATE TABLE project_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE service_configs (
			name TEXT PRIMARY KEY, owner TEXT NOT NULL, source_root TEXT NOT NULL UNIQUE,
			domain TEXT NOT NULL DEFAULT '', tags TEXT NOT NULL DEFAULT '[]'
		)`,
		`INSERT INTO schema_migrations(version) VALUES (2)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply project schema migration: %w", err)
		}
	}
	return tx.Commit()
}

// Ingest replaces prior extracted material with facts from one directory. This makes
// reruns deterministic and idempotent while retaining the ledger's migration history.
func Ingest(db *sql.DB, root string) (Stats, error) {
	return ingest(db, root, nil, nil)
}

func ingest(db *sql.DB, root string, config *Config, manifest []byte) (Stats, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Stats{}, fmt.Errorf("resolve input directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return Stats{}, fmt.Errorf("read input directory: %w", err)
	}
	if !info.IsDir() {
		return Stats{}, fmt.Errorf("input path %q is not a directory", root)
	}
	files, err := supportedFiles(root)
	if err != nil {
		return Stats{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return Stats{}, err
	}
	defer tx.Rollback()
	for _, table := range []string{"diagnostics", "scanned_files", "evidence", "relationships", "assets", "sources", "service_configs"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return Stats{}, fmt.Errorf("clear previous %s: %w", table, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO project_metadata(key, value) VALUES ('project_root', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, root); err != nil {
		return Stats{}, fmt.Errorf("record project root: %w", err)
	}
	var stats Stats
	var goFiles []sourceInput
	goBytes := 0
	sourceIDs := make(map[string]int64)
	stats.CandidateFiles = len(files)
	for _, path := range files {
		bytes, err := readSource(root, path)
		if err != nil {
			return Stats{}, fmt.Errorf("read %q: %w", path, err)
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return Stats{}, fmt.Errorf("make source path relative to project: %w", err)
		}
		relativePath = filepath.ToSlash(relativePath)
		if config != nil && relativePath == ConfigFilename && sourceHash(bytes) != sourceHash(manifest) {
			return Stats{}, fmt.Errorf("manifest changed during scan; rerun scan")
		}
		if _, err := tx.Exec(`INSERT INTO scanned_files(path, sha256) VALUES (?, ?)`, relativePath, sourceHash(bytes)); err != nil {
			return Stats{}, err
		}
		if filepath.Ext(path) == ".go" && generatedGo(bytes) {
			if err := insertDiagnostic(tx, Diagnostic{Code: "generated_file", Message: "Generated Go source excluded.", Path: relativePath, Locator: "file"}); err != nil {
				return Stats{}, err
			}
			continue
		}
		sourceID, err := insertSource(tx, relativePath, bytes)
		if err != nil {
			return Stats{}, err
		}
		stats.Sources++
		sourceIDs[relativePath] = sourceID
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go":
			goBytes += len(bytes)
			if goBytes > 64<<20 {
				return Stats{}, fmt.Errorf("Go source inventory exceeds 64 MiB; select a narrower project root")
			}
			goFiles = append(goFiles, sourceInput{Path: relativePath, Content: bytes})
		case ".sql":
			if err := ingestSQL(tx, sourceID, string(bytes), &stats); err != nil {
				return Stats{}, fmt.Errorf("ingest SQL %q: %w", path, err)
			}
		default:
			recognized, err := ingestOpenAPI(tx, sourceID, bytes, &stats)
			if err != nil {
				return Stats{}, fmt.Errorf("parse OpenAPI %q: %w", path, err)
			}
			if !recognized {
				recognized, err = ingestAsyncAPI(tx, sourceID, bytes, &stats)
				if err != nil {
					return Stats{}, fmt.Errorf("parse AsyncAPI %q: %w", path, err)
				}
			}
			if !recognized {
				if _, err := tx.Exec(`DELETE FROM sources WHERE id = ?`, sourceID); err != nil {
					return Stats{}, err
				}

				stats.Sources--
				stats.SkippedFiles++
			}
		}
	}
	if err := ingestGo(tx, goFiles, sourceIDs, config, &stats); err != nil {
		return Stats{}, err
	}
	if config != nil {
		if err := attachServices(tx, *config, manifest); err != nil {
			return Stats{}, err
		}
		stats.Services = len(config.Services)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM diagnostics`).Scan(&stats.Gaps); err != nil {
		return Stats{}, err
	}
	if _, err := tx.Exec(`DELETE FROM project_metadata WHERE key = 'last_build_at'`); err != nil {
		return Stats{}, err
	}
	if _, err := tx.Exec(`INSERT INTO project_metadata(key, value) VALUES ('last_scan_at', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Stats{}, err
	}
	if err := tx.Commit(); err != nil {
		return Stats{}, fmt.Errorf("commit ingestion: %w", err)
	}
	return stats, nil
}

func ingestAsyncAPI(tx *sql.Tx, sourceID int64, content []byte, stats *Stats) (bool, error) {
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		return false, err
	}
	version, ok := document["asyncapi"].(string)
	if !ok {
		return false, nil
	}
	if !strings.HasPrefix(version, "2.") && !strings.HasPrefix(version, "3.") {
		return false, fmt.Errorf("unsupported AsyncAPI version %q; v0 supports 2.x and 3.x channel/message structures", version)
	}
	title, _ := asMap(document["info"])["title"].(string)
	if title == "" {
		title = "AsyncAPI document"
	}
	apiID, err := insertAsset(tx, sourceID, "asyncapi", title, title, map[string]string{"asyncapi": version}, "info")
	if err != nil {
		return true, err
	}
	stats.APIs++
	schemas := asMap(asMap(document["components"])["schemas"])
	schemaIDs := make(map[string]int64, len(schemas))
	for _, name := range sortedKeys(schemas) {
		id, err := insertAsset(tx, sourceID, "schema", name, name, map[string]string{}, "components.schemas."+name)
		if err != nil {
			return true, err
		}
		schemaIDs[name] = id
		stats.Schemas++
	}
	for _, channelName := range sortedKeys(asMap(document["channels"])) {
		channelID, err := insertAsset(tx, sourceID, "channel", channelName, channelName, map[string]string{}, "channels."+channelName)
		if err != nil {
			return true, err
		}
		if err := insertRelationship(tx, apiID, channelID, "contains_channel", "declared", sourceID, "channels."+channelName); err != nil {
			return true, err
		}
		channel := asMap(asMap(document["channels"])[channelName])
		for _, operationName := range []string{"publish", "subscribe"} {
			message := asMap(asMap(channel[operationName])["message"])
			if message == nil {
				continue
			}
			messageName, _ := message["name"].(string)
			if messageName == "" {
				messageName = channelName + " " + operationName
			}
			locator := "channels." + channelName + "." + operationName + ".message"
			messageID, err := insertAsset(tx, sourceID, "message", messageName, channelName+" "+operationName, map[string]string{"operation": operationName}, locator)
			if err != nil {
				return true, err
			}
			if err := insertRelationship(tx, channelID, messageID, "contains_message", "declared", sourceID, locator); err != nil {
				return true, err
			}
			refs := make(map[string]struct{})
			collectSchemaRefs(message, refs)
			for _, name := range sortedSet(refs) {
				if schemaID, found := schemaIDs[name]; found {
					if err := insertRelationship(tx, messageID, schemaID, "references_schema", "declared", sourceID, locator); err != nil {
						return true, err
					}
				}
			}
		}
	}
	return true, nil
}

func supportedFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && ignoredDirectory(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || credentialFilename(d.Name()) {
			return nil
		}

		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".yaml", ".yml", ".sql":
			files = append(files, path)
		case ".go":
			if !strings.HasSuffix(d.Name(), "_test.go") {
				files = append(files, path)
			}
		}
		if len(files) > 10000 {
			return fmt.Errorf("project exceeds 10000 candidate files; select a narrower project root")
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func ignoredDirectory(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor":
		return true
	default:
		return false
	}
}

func insertSource(tx *sql.Tx, path string, content []byte) (int64, error) {
	sum := sha256.Sum256(content)
	result, err := tx.Exec(`INSERT INTO sources(path, sha256) VALUES (?, ?)`, path, hex.EncodeToString(sum[:]))
	if err != nil {
		return 0, fmt.Errorf("record source %q: %w", path, err)
	}
	return result.LastInsertId()
}

func ingestOpenAPI(tx *sql.Tx, sourceID int64, content []byte, stats *Stats) (bool, error) {
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		return false, err
	}
	if _, ok := document["openapi"].(string); !ok {
		return false, nil
	}
	info := asMap(document["info"])
	title, _ := info["title"].(string)
	if title == "" {
		title = "OpenAPI document"
	}
	apiID, err := insertAsset(tx, sourceID, "api", title, title, map[string]string{"openapi": fmt.Sprint(document["openapi"])}, "info")
	if err != nil {
		return true, err
	}
	stats.APIs++

	schemas := asMap(asMap(document["components"])["schemas"])
	schemaIDs := make(map[string]int64, len(schemas))
	schemaNames := sortedKeys(schemas)
	for _, name := range schemaNames {
		id, err := insertAsset(tx, sourceID, "schema", name, name, map[string]string{}, "components.schemas."+name)
		if err != nil {
			return true, err
		}
		schemaIDs[name] = id
		stats.Schemas++
	}
	paths := asMap(document["paths"])
	for _, path := range sortedKeys(paths) {
		pathItem := asMap(paths[path])
		for _, method := range []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"} {
			rawOperation, exists := pathItem[method]
			if !exists {
				continue
			}
			operation := asMap(rawOperation)
			if operation == nil {
				continue
			}
			canonical := strings.ToUpper(method) + " " + path
			name, _ := operation["operationId"].(string)
			if name == "" {
				name = canonical
			}
			locator := "paths." + path + "." + method
			opID, err := insertAsset(tx, sourceID, "operation", name, canonical, map[string]string{"method": strings.ToUpper(method), "path": path}, locator)
			if err != nil {
				return true, err
			}
			stats.Operations++
			if err := insertRelationship(tx, apiID, opID, "contains_operation", "declared", sourceID, locator); err != nil {
				return true, err
			}
			references := make(map[string]struct{})
			collectSchemaRefs(operation, references)
			for _, schemaName := range sortedSet(references) {
				schemaID, exists := schemaIDs[schemaName]
				if !exists {
					continue // External and missing component references are intentionally not guessed.
				}
				if err := insertRelationship(tx, opID, schemaID, "references_schema", "declared", sourceID, locator); err != nil {
					return true, err
				}
			}
		}
	}
	return true, nil
}

func ingestSQL(tx *sql.Tx, sourceID int64, text string, stats *Stats) error {
	for _, statement := range ddlStatements(text) {
		match := createTablePattern.FindStringSubmatch(statement)
		if match == nil {
			continue
		}
		rawName := match[1]
		name := tableLeafName(rawName)
		if name == "" {
			continue
		}
		if _, err := insertAsset(tx, sourceID, "table", name, normalizeSQLName(rawName), map[string]string{}, "CREATE TABLE "+strings.TrimSpace(rawName)); err != nil {
			return err
		}
		stats.Tables++
	}
	return nil
}

func insertAsset(tx *sql.Tx, sourceID int64, kind, name, canonical string, attributes map[string]string, locator string) (int64, error) {
	encoded, err := json.Marshal(attributes)
	if err != nil {
		return 0, err
	}
	result, err := tx.Exec(`INSERT INTO assets(source_id, kind, name, canonical_name, attributes) VALUES (?, ?, ?, ?, ?)`,
		sourceID, kind, name, canonical, string(encoded))
	if err != nil {
		return 0, fmt.Errorf("record %s %q: %w", kind, name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO evidence(source_id, asset_id, locator) VALUES (?, ?, ?)`, sourceID, id, locator); err != nil {
		return 0, fmt.Errorf("record asset evidence: %w", err)
	}
	return id, nil
}

func insertRelationship(tx *sql.Tx, fromID, toID int64, relationType, origin string, sourceID int64, locator string) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO relationships(from_asset_id, to_asset_id, relationship_type, origin) VALUES (?, ?, ?, ?)`,
		fromID, toID, relationType, origin)
	if err != nil {
		return err
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM relationships WHERE from_asset_id = ? AND to_asset_id = ? AND relationship_type = ? AND origin = ?`,
		fromID, toID, relationType, origin).Scan(&id); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO evidence(source_id, relationship_id, locator)
		SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM evidence WHERE source_id = ? AND relationship_id = ? AND locator = ?)`,
		sourceID, id, locator, sourceID, id, locator)
	return err
}

// Build removes only inferred edges and adds the explicitly conservative v0 rule:
// schema and table names must have an exact normalized match.
func Build(db *sql.DB) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := Verify(tx); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM relationships WHERE origin = 'inferred'`); err != nil {
		return 0, err
	}
	rows, err := tx.Query(`
		SELECT s.id, s.name, s.source_id, t.id, t.name, t.source_id
		FROM assets s JOIN assets t ON 1 = 1
		WHERE s.kind = 'schema' AND t.kind = 'table'
		ORDER BY s.name, s.id, t.name, t.id`)
	if err != nil {
		return 0, err
	}
	type matchCandidate struct {
		schemaID, schemaSourceID, tableID, tableSourceID int64
		schemaName, tableName                            string
	}
	var candidates []matchCandidate
	for rows.Next() {
		var candidate matchCandidate
		if err := rows.Scan(&candidate.schemaID, &candidate.schemaName, &candidate.schemaSourceID,
			&candidate.tableID, &candidate.tableName, &candidate.tableSourceID); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	count := 0
	for _, candidate := range candidates {
		if normalizeName(candidate.schemaName) != normalizeName(candidate.tableName) {
			continue
		}
		if err := insertRelationship(tx, candidate.schemaID, candidate.tableID, "matches_table_name", "inferred",
			candidate.schemaSourceID, "normalized-name:"+normalizeName(candidate.schemaName)); err != nil {
			return 0, err
		}
		// Preserve both inputs as provenance for the derived conclusion.
		var relationshipID int64
		if err := tx.QueryRow(`SELECT id FROM relationships WHERE from_asset_id = ? AND to_asset_id = ? AND relationship_type = 'matches_table_name' AND origin = 'inferred'`,
			candidate.schemaID, candidate.tableID).Scan(&relationshipID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO evidence(source_id, relationship_id, locator)
			SELECT ?, ?, ? WHERE NOT EXISTS (
				SELECT 1 FROM evidence WHERE source_id = ? AND relationship_id = ? AND locator = ?)`,
			candidate.tableSourceID, relationshipID, "normalized-name:"+normalizeName(candidate.tableName),
			candidate.tableSourceID, relationshipID, "normalized-name:"+normalizeName(candidate.tableName)); err != nil {
			return 0, err
		}
		count++
	}
	if _, err := tx.Exec(`INSERT INTO project_metadata(key, value) VALUES ('last_build_at', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return 0, fmt.Errorf("record build: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func Explain(db *sql.DB, out io.Writer, name string) error {
	asset, err := findAsset(db, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %s\n", asset.Kind, asset.Name)
	if asset.CanonicalName != asset.Name {
		fmt.Fprintf(out, "Canonical name: %s\n", asset.CanonicalName)
	}
	if asset.Attributes != "{}" {
		fmt.Fprintf(out, "Attributes: %s\n", asset.Attributes)
	}
	owners, err := ownersFor(db, asset.ID)
	if err != nil {
		return err
	}
	if len(owners) > 0 {
		fmt.Fprintf(out, "Owners: %s\n", strings.Join(owners, ", "))
	}
	if err := writeEvidence(db, out, "Evidence", "asset_id", asset.ID); err != nil {
		return err
	}
	fmt.Fprintln(out, "Direct links:")
	if err := writeRelations(db, out, asset.ID); err != nil {
		return err
	}
	return writeAssetDiagnostics(db, out, asset.ID)
}

func Impact(db *sql.DB, out io.Writer, query string) error {
	asset, err := findAsset(db, query)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Direct impact graph for %s %q:\n", asset.Kind, asset.Name)
	owners, err := ownersFor(db, asset.ID)
	if err != nil {
		return err
	}
	if len(owners) > 0 {
		fmt.Fprintf(out, "Owners: %s\n", strings.Join(owners, ", "))
	}
	if err := writeRelations(db, out, asset.ID); err != nil {
		return err
	}
	return writeAssetDiagnostics(db, out, asset.ID)
}

func findAsset(db queryer, query string) (Asset, error) {
	if strings.HasPrefix(query, "id:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(query, "id:"), 10, 64)
		if err != nil {
			return Asset{}, fmt.Errorf("asset ID must use id:<integer>")
		}
		var asset Asset
		err = db.QueryRow(`SELECT id, kind, name, canonical_name, attributes FROM assets WHERE id = ?`, id).
			Scan(&asset.ID, &asset.Kind, &asset.Name, &asset.CanonicalName, &asset.Attributes)
		if err != nil {
			return Asset{}, fmt.Errorf("asset ID %q is unavailable: %w", query, err)
		}
		return asset, nil
	}
	kind, name := "", query
	if prefix, rest, found := strings.Cut(query, ":"); found {
		switch prefix {
		case "api", "operation", "schema", "table", "service", "function", "route", "query", "package", "channel", "message":
			kind, name = prefix, rest
		}
	}
	rows, err := db.Query(`SELECT id, kind, name, canonical_name, attributes
		FROM assets WHERE (name = ? OR canonical_name = ?) AND (? = '' OR kind = ?) ORDER BY kind, name, id`, name, name, kind, kind)
	if err != nil {
		return Asset{}, err
	}
	defer rows.Close()
	var candidates []Asset
	for rows.Next() {
		var asset Asset
		if err := rows.Scan(&asset.ID, &asset.Kind, &asset.Name, &asset.CanonicalName, &asset.Attributes); err != nil {
			return Asset{}, err
		}
		candidates = append(candidates, asset)
	}
	if err := rows.Err(); err != nil {
		return Asset{}, err
	}
	switch len(candidates) {
	case 0:
		return Asset{}, fmt.Errorf("no asset named %q", query)
	case 1:
		return candidates[0], nil
	default:
		names := make([]string, len(candidates))
		for i, candidate := range candidates {
			names[i] = fmt.Sprintf("id:%d %s:%s (%s)", candidate.ID, candidate.Kind, candidate.Name, candidate.CanonicalName)
		}
		return Asset{}, fmt.Errorf("asset %q is ambiguous; candidates: %s", query, strings.Join(names, ", "))
	}
}

func writeRelations(db *sql.DB, out io.Writer, assetID int64) error {
	rows, err := db.Query(`
		SELECT r.id, r.from_asset_id, r.to_asset_id, r.relationship_type, r.origin,
		       f.kind, f.name, t.kind, t.name
		FROM relationships r
		JOIN assets f ON f.id = r.from_asset_id
		JOIN assets t ON t.id = r.to_asset_id
		WHERE r.from_asset_id = ? OR r.to_asset_id = ?
		ORDER BY CASE WHEN r.from_asset_id = ? THEN 0 ELSE 1 END, r.relationship_type, f.kind, f.name, t.kind, t.name`,
		assetID, assetID, assetID)
	if err != nil {
		return err
	}
	var relations []Relation
	for rows.Next() {
		var relation Relation
		if err := rows.Scan(&relation.ID, &relation.FromID, &relation.ToID, &relation.Type, &relation.Origin,
			&relation.FromKind, &relation.FromName, &relation.ToKind, &relation.ToName); err != nil {
			rows.Close()
			return err
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	found := false
	for _, relation := range relations {
		direction := "OUT"
		connectedKind, connectedName := relation.ToKind, relation.ToName
		if relation.ToID == assetID {
			direction = "IN"
			connectedKind, connectedName = relation.FromKind, relation.FromName
		}
		fmt.Fprintf(out, "- %s %s [%s, %s] %s %q\n", direction, relation.Type, relation.Origin, direction, connectedKind, connectedName)
		if err := writeEvidence(db, out, "  Evidence", "relationship_id", relation.ID); err != nil {
			return err
		}
		found = true
	}
	if !found {
		fmt.Fprintln(out, "- none")
	}
	return nil
}

func writeEvidence(db *sql.DB, out io.Writer, label, column string, id int64) error {
	if column != "asset_id" && column != "relationship_id" {
		return fmt.Errorf("unsupported evidence column %q", column)
	}
	rows, err := db.Query(`SELECT s.path, e.locator FROM evidence e JOIN sources s ON s.id = e.source_id WHERE e.`+column+` = ? ORDER BY s.path, e.locator`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	var references []string
	for rows.Next() {
		var path, locator string
		if err := rows.Scan(&path, &locator); err != nil {
			return err
		}
		references = append(references, path+" @ "+locator)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %s\n", label, strings.Join(references, "; "))
	return nil
}

func asMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	result, _ := value.(map[string]any)
	return result
}

func collectSchemaRefs(value any, refs map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
			refs[strings.TrimPrefix(ref, "#/components/schemas/")] = struct{}{}
		}
		for _, child := range typed {
			collectSchemaRefs(child, refs)
		}
	case []any:
		for _, child := range typed {
			collectSchemaRefs(child, refs)
		}
	}
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSet(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeName(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func normalizeSQLName(value string) string {
	parts := strings.Split(value, ".")
	for i := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(parts[i]), "`\"[]")
	}
	return strings.Join(parts, ".")
}

func tableLeafName(value string) string {
	parts := strings.Split(normalizeSQLName(value), ".")
	return parts[len(parts)-1]
}
