package ledger

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestEvidencePackIsStableScopedAndCurrent(t *testing.T) {
	root := exampleProject(t)
	db := newTestDB(t)
	if _, err := Scan(db, root); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(db); err != nil {
		t.Fatal(err)
	}
	first, err := BuildEvidencePack(db, root, "listProducts")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "orders-service") || strings.Contains(string(encoded), "services/orders") {
		t.Fatalf("unrelated owner/asset leaked: %s", encoded)
	}
	if !strings.Contains(string(encoded), "commerce-platform") || !strings.Contains(string(encoded), "system-ledger.yaml") {
		t.Fatalf("ownership provenance missing: %s", encoded)
	}
	if first.SelectedAssetID == "" || len(first.Assets) == 0 || len(first.Evidence) == 0 {
		t.Fatalf("empty evidence pack: %+v", first)
	}
	if _, err := Scan(db, root); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(db); err != nil {
		t.Fatal(err)
	}
	second, err := BuildEvidencePack(db, root, "listProducts")
	if err != nil {
		t.Fatal(err)
	}
	encodedAgain, err := json.Marshal(second)
	if err != nil || string(encoded) != string(encodedAgain) {
		t.Fatalf("pack changed without source changes: %s\n%s\n%v", encoded, encodedAgain, err)
	}
	writeProjectFile(t, root, "services/catalog/new.go", "package catalog\nfunc NewFact() {}")
	if _, err := BuildEvidencePack(db, root, "listProducts"); err == nil {
		t.Fatal("new source failed to invalidate pack")
	}
	if _, err := BuildEvidencePack(db, t.TempDir(), "listProducts"); err == nil {
		t.Fatal("cross-project pack accepted")
	}
}

func TestEvidencePackHasHardNeighborhoodBoundary(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, ConfigFilename, "version: 1\nservices: []\n")
	var code strings.Builder
	code.WriteString("package large\nfunc Root() {\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&code, "helper%d()\n", i)
	}
	code.WriteString("}\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&code, "func helper%d() {}\n", i)
	}
	writeProjectFile(t, root, "large.go", code.String())
	db := newTestDB(t)
	if _, err := Scan(db, root); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM assets WHERE kind = 'function' AND name LIKE '%Root%'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	pack, err := BuildEvidencePack(db, root, fmt.Sprintf("id:%d", id))
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Assets) > packAssets || len(pack.Relationships) > packRelationships || len(pack.Evidence) > packEvidence || len(pack.Gaps) > packGaps {
		t.Fatalf("unbounded pack: %+v", pack)
	}
	found := false
	for _, gap := range pack.Gaps {
		found = found || gap.Code == "context_boundary"
	}
	if !found {
		t.Fatal("context truncation not surfaced")
	}
}

func TestEvidencePackOmitsSQLAndComments(t *testing.T) {
	root := goProject(t)
	writeProjectFile(t, root, "services/reports/note.go", "// PRIVATE_COMMENT_SENTINEL\npackage reports\n")
	db := newTestDB(t)
	if _, err := Scan(db, root); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM assets WHERE kind = 'query'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	pack, err := BuildEvidencePack(db, root, fmt.Sprintf("id:%d", id))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{"PRIVATE_COMMENT_SENTINEL", "SELECT id FROM public.reports", "func loadReports"} {
		if strings.Contains(string(encoded), excluded) {
			t.Fatalf("source text leaked: %s", encoded)
		}
	}
}
