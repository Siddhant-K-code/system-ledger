package ledger

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

type replacingReader struct {
	queryer
	writer   *sql.DB
	replaced bool
}

func (reader *replacingReader) Query(query string, args ...any) (*sql.Rows, error) {
	if !reader.replaced && strings.Contains(query, "SELECT DISTINCT a.id FROM relationships") {
		reader.replaced = true
		if _, err := reader.writer.Exec(`UPDATE assets SET name = 'Unrelated' WHERE name = 'listProducts'`); err != nil {
			return nil, err
		}
	}
	return reader.queryer.Query(query, args...)
}

func TestEvidencePackReadsOneDatabaseSnapshot(t *testing.T) {
	root := exampleProject(t)
	path := filepath.Join(t.TempDir(), "snapshot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(db, root); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	reader := &replacingReader{queryer: tx, writer: writer}
	pack, err := buildEvidencePack(reader, root, "listProducts")
	if err != nil {
		t.Fatal(err)
	}
	if !reader.replaced {
		t.Fatal("concurrent replacement was not exercised")
	}
	found := false
	for _, asset := range pack.Assets {
		if asset.ID == pack.SelectedAssetID {
			found = true
			if asset.Name != "listProducts" {
				t.Fatalf("pack mixed scan generations: %+v", asset)
			}
		}
	}
	if !found {
		t.Fatal("selected asset missing")
	}
}
