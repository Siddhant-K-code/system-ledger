package ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCrossFileSQLConstantsRetainSupportingEvidence(t *testing.T) {
	root := goProject(t)
	writeProjectFile(t, root, "services/reports/store.go", `package reports
import "database/sql"
var db *sql.DB
func loadReports() { db.Query(reportSQL) }
`)
	writeProjectFile(t, root, "services/reports/queries.go", `package reports
const reportSQL = "SELECT id FROM public.reports"
`)
	db := newTestDB(t)
	if _, err := Scan(db, root); err != nil {
		t.Fatal(err)
	}
	var found int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relationships r
		JOIN evidence e ON e.relationship_id = r.id JOIN sources s ON s.id = e.source_id
		WHERE r.relationship_type = 'reads_table' AND s.path = 'services/reports/queries.go' AND e.locator = 'L2-L2'`).Scan(&found); err != nil || found != 1 {
		t.Fatalf("constant's definition missing from table edge: %d %v", found, err)
	}
	pack, err := BuildEvidencePack(db, root, "route:GET /reports")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "services/reports/queries.go") || strings.Contains(string(encoded), "SELECT id FROM") {
		t.Fatalf("pack must include constant location/hash but not literal: %s", encoded)
	}
	writeProjectFile(t, root, "services/reports/queries.go", `package reports
const reportSQL = "SELECT id FROM a_different_table"
`)
	if _, err := BuildEvidencePack(db, root, "route:GET /reports"); err == nil {
		t.Fatal("changed supporting constant accepted as current")
	}
}
