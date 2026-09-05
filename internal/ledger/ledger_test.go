package ledger

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func fixtureDirectory(t *testing.T) string {
	t.Helper()
	source := filepath.Join("testdata", "demo")
	target := t.TempDir()
	for _, name := range []string{"openapi.yaml", "schema.sql"} {
		content, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func TestIngestIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	root := fixtureDirectory(t)
	first, err := Ingest(db, root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ingest(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first != (Stats{Sources: 2, APIs: 1, Operations: 2, Schemas: 2, Tables: 2, CandidateFiles: 2}) {
		t.Fatalf("unexpected stats: first=%+v second=%+v", first, second)
	}
	var assets, evidence int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assets`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM evidence`).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if assets != 7 || evidence != 11 {
		t.Fatalf("assets=%d evidence=%d, want 7 and 11", assets, evidence)
	}
}

func TestBuildExplainImpactAndVerify(t *testing.T) {
	db := newTestDB(t)
	if _, err := Ingest(db, fixtureDirectory(t)); err != nil {
		t.Fatal(err)
	}
	count, err := Build(db)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("inferred links = %d, want 2", count)
	}
	var explain bytes.Buffer
	if err := Explain(db, &explain, "listPets"); err != nil {
		t.Fatal(err)
	}
	if got := explain.String(); !strings.Contains(got, "references_schema") || !strings.Contains(got, "openapi.yaml @") {
		t.Fatalf("explain missing evidence-backed link:\n%s", got)
	}
	var impact bytes.Buffer
	if err := Impact(db, &impact, "Pet"); err != nil {
		t.Fatal(err)
	}
	if got := impact.String(); !strings.Contains(got, "matches_table_name") || !strings.Contains(got, "IN references_schema") {
		t.Fatalf("impact missing links:\n%s", got)
	}
	if err := Verify(db); err != nil {
		t.Fatalf("valid ledger did not verify: %v", err)
	}
}

func TestVerifyRejectsAssetWithoutEvidence(t *testing.T) {
	db := newTestDB(t)
	if _, err := Ingest(db, fixtureDirectory(t)); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`DELETE FROM evidence WHERE asset_id = (SELECT id FROM assets WHERE name = 'Pet')`); err != nil {
		t.Fatal(err)
	}
	if err := Verify(db); err == nil || !strings.Contains(err.Error(), "assets without evidence") {
		t.Fatalf("Verify error = %v, want missing evidence failure", err)
	}
}

func TestMigrateV1LedgerToProjectSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`,
		`INSERT INTO schema_migrations(version) VALUES (1)`,
		`CREATE TABLE sources (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, sha256 TEXT NOT NULL)`,
		`CREATE TABLE assets (id INTEGER PRIMARY KEY, source_id INTEGER NOT NULL REFERENCES sources(id), kind TEXT NOT NULL, name TEXT NOT NULL, canonical_name TEXT NOT NULL, attributes TEXT NOT NULL DEFAULT '{}', UNIQUE(source_id, kind, canonical_name))`,
		`CREATE TABLE relationships (id INTEGER PRIMARY KEY, from_asset_id INTEGER NOT NULL REFERENCES assets(id), to_asset_id INTEGER NOT NULL REFERENCES assets(id), relationship_type TEXT NOT NULL, origin TEXT NOT NULL CHECK(origin IN ('declared', 'inferred')), UNIQUE(from_asset_id, to_asset_id, relationship_type, origin))`,
		`CREATE TABLE evidence (id INTEGER PRIMARY KEY, source_id INTEGER NOT NULL REFERENCES sources(id), asset_id INTEGER REFERENCES assets(id), relationship_id INTEGER REFERENCES relationships(id), locator TEXT NOT NULL, CHECK ((asset_id IS NOT NULL AND relationship_id IS NULL) OR (asset_id IS NULL AND relationship_id IS NOT NULL)))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO service_configs(name, owner, source_root) VALUES ('catalog', 'team', 'services/catalog')`); err != nil {
		t.Fatalf("v2 service table unavailable: %v", err)
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema version = %d, %v; want 2, nil", version, err)
	}
}
