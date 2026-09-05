package ledger

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exampleProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join("..", "..", "examples", "multi-service")
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInitIsIdempotentAndNonDestructive(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	first, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ConfigCreated || !pathExists(first.LedgerPath) {
		t.Fatalf("unexpected first initialization: %+v", first)
	}
	const custom = "version: 1\nservices: []\n"
	if err := os.WriteFile(first.ConfigPath, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if second.ConfigCreated || string(content) != custom {
		t.Fatalf("init overwrote manifest: %+v content=%q", second, content)
	}
}

func TestScanAssociatesServicesAndProducesStableReports(t *testing.T) {
	root := exampleProject(t)
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "ignored", "schema.sql"), []byte("CREATE TABLE ignored (id INTEGER);"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := newTestDB(t)
	result, err := Scan(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats != (Stats{Sources: 4, APIs: 2, Operations: 2, Schemas: 2, Tables: 2, Services: 2, CandidateFiles: 5, SkippedFiles: 1}) {
		t.Fatalf("unexpected scan stats: %+v", result.Stats)
	}
	second, err := Scan(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Stats != result.Stats {
		t.Fatalf("scan is not deterministic: first=%+v second=%+v", result.Stats, second.Stats)
	}
	if count, err := Build(db); err != nil || count != 2 {
		t.Fatalf("build = %d, %v; want 2, nil", count, err)
	}
	var explain bytes.Buffer
	if err := Explain(db, &explain, "Product"); err != nil {
		t.Fatal(err)
	}
	if got := explain.String(); !strings.Contains(got, "Owners: catalog-service") || !strings.Contains(got, "[source-derived") {
		t.Fatalf("owner or service relationship missing:\n%s", got)
	}
	var impact bytes.Buffer
	if err := RenderImpact(db, &impact, "Product", "json"); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Asset AssetReport `json:"asset"`
	}
	if err := json.Unmarshal(impact.Bytes(), &decoded); err != nil {
		t.Fatalf("impact is not JSON: %v\n%s", err, impact.String())
	}
	if decoded.Asset.Name != "Product" || len(decoded.Asset.Owners) != 1 || decoded.Asset.Owners[0] != "catalog-service" {
		t.Fatalf("unexpected impact report: %+v", decoded.Asset)
	}
	var summary bytes.Buffer
	if err := WriteSummary(db, &summary, "json"); err != nil {
		t.Fatal(err)
	}
	var report SummaryReport
	if err := json.Unmarshal(summary.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Assets != (AssetCounts{APIs: 2, Operations: 2, Schemas: 2, Tables: 2, Services: 2}) || len(report.Relationships) != 4 || report.Warnings == nil {
		t.Fatalf("unexpected summary: %+v", report)
	}
	var summaryAgain bytes.Buffer
	if err := WriteSummary(db, &summaryAgain, "json"); err != nil {
		t.Fatal(err)
	}
	if summary.String() != summaryAgain.String() {
		t.Fatalf("summary JSON is not stable:\n%s\n%s", summary.String(), summaryAgain.String())
	}
	if err := Verify(db); err != nil {
		t.Fatalf("valid scanned project did not verify: %v", err)
	}
}

func TestVerifyReportsMissingServiceRoot(t *testing.T) {
	root := exampleProject(t)
	db := newTestDB(t)
	if _, err := Scan(db, root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "services", "catalog")); err != nil {
		t.Fatal(err)
	}
	report := Verification(db)
	if report.Valid || !strings.Contains(strings.Join(report.Issues, "\n"), `service "catalog-service" source root "services/catalog"`) {
		t.Fatalf("unexpected verification report: %+v", report)
	}
}
