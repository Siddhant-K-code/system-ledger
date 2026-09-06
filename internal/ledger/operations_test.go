package ledger

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAsyncAPIAndPath(t *testing.T) {
	root := t.TempDir()
	asyncAPI, err := os.ReadFile(filepath.Join("testdata", "asyncapi", "billing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "events.yaml"), asyncAPI, 0o600); err != nil {
		t.Fatal(err)
	}
	db := newTestDB(t)
	stats, err := Ingest(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if stats.APIs != 1 || stats.Schemas != 1 {
		t.Fatalf("unexpected AsyncAPI stats: %+v", stats)
	}
	if _, err := Ingest(db, root); err != nil {
		t.Fatalf("AsyncAPI reingestion failed: %v", err)
	}
	report, err := FindPath(db, "Billing events", "Invoice")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Steps) != 3 || report.Steps[2].Asset.Name != "Invoice" || report.Steps[2].Origin != "declared" {
		t.Fatalf("unexpected path: %+v", report)
	}
	var output bytes.Buffer
	if err := RenderPath(db, &output, "Billing events", "Invoice", "json"); err != nil {
		t.Fatal(err)
	}
	var decoded PathReport
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || len(decoded.Steps) != 3 {
		t.Fatalf("invalid path JSON: %v %s", err, output.String())
	}
	if _, err := FindPath(db, "Invoice", "Billing events"); err == nil {
		t.Fatal("reverse dependency path should not be traversed")
	}
	if _, err := FindPath(db, "Invoice", "missing"); err == nil {
		t.Fatal("missing asset did not fail")
	}
}

func TestDoctorHealthyWarningAndFailure(t *testing.T) {
	root := exampleProject(t)
	db := newTestDB(t)
	if _, err := Scan(db, root); err != nil {
		t.Fatal(err)
	}
	warning := Doctor(db, root)
	if !warning.Healthy || warning.Checks[2].Status != "warning" {
		t.Fatalf("expected unbuilt warning, got %+v", warning)
	}
	if _, err := Build(db); err != nil {
		t.Fatal(err)
	}
	healthy := Doctor(db, root)
	if !healthy.Healthy {
		t.Fatalf("expected healthy doctor report, got %+v", healthy)
	}
	if err := os.Remove(filepath.Join(root, "services", "orders", "schema.sql")); err != nil {
		t.Fatal(err)
	}
	failed := Doctor(db, root)
	if failed.Healthy || !strings.Contains(failed.Checks[1].Message, "unavailable") {
		t.Fatalf("expected source failure, got %+v", failed)
	}
}
