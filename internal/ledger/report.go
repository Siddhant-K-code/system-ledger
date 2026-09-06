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
	"slices"
	"sort"
	"strings"
)

type EvidenceRef struct {
	Path    string `json:"path"`
	Locator string `json:"locator"`
}

type AssetRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type LinkReport struct {
	Direction        string        `json:"direction"`
	RelationshipType string        `json:"relationship_type"`
	Origin           string        `json:"origin"`
	Asset            AssetRef      `json:"asset"`
	Evidence         []EvidenceRef `json:"evidence"`
}

type AssetReport struct {
	Kind          string          `json:"kind"`
	Name          string          `json:"name"`
	CanonicalName string          `json:"canonical_name"`
	Attributes    json.RawMessage `json:"attributes"`
	Owners        []string        `json:"owners"`
	Evidence      []EvidenceRef   `json:"evidence"`
	Links         []LinkReport    `json:"links"`
}

type SummaryReport struct {
	Services      []ServiceSummary      `json:"services"`
	Assets        AssetCounts           `json:"assets"`
	Relationships []RelationshipSummary `json:"relationships"`
	Sources       int                   `json:"sources"`
	LastScanAt    string                `json:"last_scan_at,omitempty"`
	LastBuildAt   string                `json:"last_build_at,omitempty"`
	Warnings      []string              `json:"warnings"`
}

type ServiceSummary struct {
	Name       string   `json:"name"`
	Owner      string   `json:"owner"`
	SourceRoot string   `json:"source_root"`
	Domain     string   `json:"domain,omitempty"`
	Tags       []string `json:"tags"`
}

type AssetCounts struct {
	APIs       int `json:"apis"`
	Operations int `json:"operations"`
	Schemas    int `json:"schemas"`
	Tables     int `json:"tables"`
	Services   int `json:"services"`
}

type RelationshipSummary struct {
	Type   string `json:"type"`
	Origin string `json:"origin"`
	Count  int    `json:"count"`
}

type VerificationReport struct {
	Valid  bool     `json:"valid"`
	Issues []string `json:"issues"`
}

func RenderExplain(db *sql.DB, out io.Writer, name, format string) error {
	if format == "text" {
		return Explain(db, out, name)
	}
	asset, err := assetReport(db, name)
	if err != nil {
		return err
	}
	return writeJSON(out, asset)
}

func RenderImpact(db *sql.DB, out io.Writer, query, format string) error {
	if format == "text" {
		return Impact(db, out, query)
	}
	asset, err := assetReport(db, query)
	if err != nil {
		return err
	}
	return writeJSON(out, struct {
		Asset AssetReport `json:"asset"`
	}{Asset: asset})
}

func assetReport(db *sql.DB, query string) (AssetReport, error) {
	asset, err := findAsset(db, query)
	if err != nil {
		return AssetReport{}, err
	}
	attributes := json.RawMessage(asset.Attributes)
	if !json.Valid(attributes) {
		return AssetReport{}, fmt.Errorf("asset %q has invalid attributes JSON", asset.Name)
	}
	evidence, err := evidenceFor(db, "asset_id", asset.ID)
	if err != nil {
		return AssetReport{}, err
	}
	owners, err := ownersFor(db, asset.ID)
	if err != nil {
		return AssetReport{}, err
	}
	links, err := linksFor(db, asset.ID)
	if err != nil {
		return AssetReport{}, err
	}
	return AssetReport{
		Kind: asset.Kind, Name: asset.Name, CanonicalName: asset.CanonicalName,
		Attributes: attributes, Owners: owners, Evidence: evidence, Links: links,
	}, nil
}

func linksFor(db *sql.DB, assetID int64) ([]LinkReport, error) {
	rows, err := db.Query(`
		SELECT r.id, r.from_asset_id, r.to_asset_id, r.relationship_type, r.origin,
		       f.kind, f.name, t.kind, t.name
		FROM relationships r
		JOIN assets f ON f.id = r.from_asset_id
		JOIN assets t ON t.id = r.to_asset_id
		WHERE r.from_asset_id = ? OR r.to_asset_id = ?
		ORDER BY CASE WHEN r.from_asset_id = ? THEN 0 ELSE 1 END, r.relationship_type, r.origin, f.kind, f.name, t.kind, t.name`,
		assetID, assetID, assetID)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, fromID, toID                                             int64
		relationshipType, origin, fromKind, fromName, toKind, toName string
	}
	var raw []row
	for rows.Next() {
		var value row
		if err := rows.Scan(&value.id, &value.fromID, &value.toID, &value.relationshipType, &value.origin,
			&value.fromKind, &value.fromName, &value.toKind, &value.toName); err != nil {
			rows.Close()
			return nil, err
		}
		raw = append(raw, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	links := make([]LinkReport, 0, len(raw))
	for _, value := range raw {
		direction := "outgoing"
		connected := AssetRef{Kind: value.toKind, Name: value.toName}
		if value.toID == assetID {
			direction = "incoming"
			connected = AssetRef{Kind: value.fromKind, Name: value.fromName}
		}
		evidence, err := evidenceFor(db, "relationship_id", value.id)
		if err != nil {
			return nil, err
		}
		links = append(links, LinkReport{
			Direction: direction, RelationshipType: value.relationshipType, Origin: value.origin,
			Asset: connected, Evidence: evidence,
		})
	}
	return links, nil
}

func evidenceFor(db *sql.DB, column string, id int64) ([]EvidenceRef, error) {
	if column != "asset_id" && column != "relationship_id" {
		return nil, fmt.Errorf("unsupported evidence column %q", column)
	}
	rows, err := db.Query(`SELECT s.path, e.locator FROM evidence e JOIN sources s ON s.id = e.source_id
		WHERE e.`+column+` = ? ORDER BY s.path, e.locator`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var references []EvidenceRef
	for rows.Next() {
		var reference EvidenceRef
		if err := rows.Scan(&reference.Path, &reference.Locator); err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	return references, rows.Err()
}

func ownersFor(db *sql.DB, assetID int64) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT service.name
		FROM relationships r JOIN assets service ON service.id = r.from_asset_id
		WHERE r.to_asset_id = ? AND r.relationship_type = 'owns_asset' AND service.kind = 'service'
		ORDER BY service.name`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

func WriteSummary(db *sql.DB, out io.Writer, format string) error {
	report, err := Summary(db)
	if err != nil {
		return err
	}
	if format == "json" {
		return writeJSON(out, report)
	}
	fmt.Fprintf(out, "Sources: %d | Services: %d | APIs: %d | Operations: %d | Schemas: %d | Tables: %d\n",
		report.Sources, report.Assets.Services, report.Assets.APIs, report.Assets.Operations, report.Assets.Schemas, report.Assets.Tables)
	if report.LastScanAt != "" {
		fmt.Fprintf(out, "Last scan: %s", report.LastScanAt)
		if report.LastBuildAt != "" {
			fmt.Fprintf(out, " | Last build: %s", report.LastBuildAt)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "Services:")
	if len(report.Services) == 0 {
		fmt.Fprintln(out, "- none configured")
	} else {
		for _, service := range report.Services {
			fmt.Fprintf(out, "- %s (owner: %s, source: %s)\n", service.Name, service.Owner, service.SourceRoot)
		}
	}
	fmt.Fprintln(out, "Relationships:")
	if len(report.Relationships) == 0 {
		fmt.Fprintln(out, "- none")
	} else {
		for _, relationship := range report.Relationships {
			fmt.Fprintf(out, "- %s [%s]: %d\n", relationship.Type, relationship.Origin, relationship.Count)
		}
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(out, "Validation warnings:")
		for _, warning := range report.Warnings {
			fmt.Fprintf(out, "- %s\n", warning)
		}
	}
	return nil
}

func Summary(db *sql.DB) (SummaryReport, error) {
	report := SummaryReport{
		Services:      make([]ServiceSummary, 0),
		Relationships: make([]RelationshipSummary, 0),
		Warnings:      make([]string, 0),
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&report.Sources); err != nil {
		return SummaryReport{}, err
	}
	report.LastScanAt = metadata(db, "last_scan_at")
	report.LastBuildAt = metadata(db, "last_build_at")
	rows, err := db.Query(`SELECT kind, COUNT(*) FROM assets GROUP BY kind ORDER BY kind`)
	if err != nil {
		return SummaryReport{}, err
	}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return SummaryReport{}, err
		}
		switch kind {
		case "api":
			report.Assets.APIs = count
		case "operation":
			report.Assets.Operations = count
		case "schema":
			report.Assets.Schemas = count
		case "table":
			report.Assets.Tables = count
		case "service":
			report.Assets.Services = count
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SummaryReport{}, err
	}
	rows.Close()
	serviceRows, err := db.Query(`SELECT name, owner, source_root, domain, tags FROM service_configs ORDER BY name`)
	if err != nil {
		return SummaryReport{}, err
	}
	for serviceRows.Next() {
		var service ServiceSummary
		var tags string
		if err := serviceRows.Scan(&service.Name, &service.Owner, &service.SourceRoot, &service.Domain, &tags); err != nil {
			serviceRows.Close()
			return SummaryReport{}, err
		}
		if err := json.Unmarshal([]byte(tags), &service.Tags); err != nil {
			serviceRows.Close()
			return SummaryReport{}, fmt.Errorf("read tags for service %q: %w", service.Name, err)
		}
		report.Services = append(report.Services, service)
	}
	if err := serviceRows.Err(); err != nil {
		serviceRows.Close()
		return SummaryReport{}, err
	}
	serviceRows.Close()
	relationshipRows, err := db.Query(`SELECT relationship_type, origin, COUNT(*) FROM relationships
		GROUP BY relationship_type, origin ORDER BY relationship_type, origin`)
	if err != nil {
		return SummaryReport{}, err
	}
	for relationshipRows.Next() {
		var relation RelationshipSummary
		if err := relationshipRows.Scan(&relation.Type, &relation.Origin, &relation.Count); err != nil {
			relationshipRows.Close()
			return SummaryReport{}, err
		}
		report.Relationships = append(report.Relationships, relation)
	}
	if err := relationshipRows.Err(); err != nil {
		relationshipRows.Close()
		return SummaryReport{}, err
	}
	relationshipRows.Close()
	report.Warnings = validationIssues(db)
	if report.Warnings == nil {
		report.Warnings = make([]string, 0)
	}
	return report, nil
}

func RenderVerify(db *sql.DB, out io.Writer, format string) error {
	report := Verification(db)
	if format == "json" {
		if err := writeJSON(out, report); err != nil {
			return err
		}
	} else if report.Valid {
		fmt.Fprintln(out, "Ledger verification passed.")
	} else {
		for _, issue := range report.Issues {
			fmt.Fprintf(out, "Verification failed: %s\n", issue)
		}
	}
	if !report.Valid {
		return fmt.Errorf("ledger verification failed; rerun scan after correcting the reported sources or configuration")
	}
	return nil
}

func Verify(db *sql.DB) error {
	report := Verification(db)
	if !report.Valid {
		return fmt.Errorf("ledger verification failed: %s", strings.Join(report.Issues, "; "))
	}
	return nil
}

func Verification(db *sql.DB) VerificationReport {
	issues := validationIssues(db)
	if issues == nil {
		issues = make([]string, 0)
	}
	return VerificationReport{Valid: len(issues) == 0, Issues: issues}
}

func validationIssues(db *sql.DB) []string {
	var issues []string
	checks := []struct {
		name, query string
	}{
		{"foreign-key violations", `PRAGMA foreign_key_check`},
		{"assets without evidence", `SELECT a.id FROM assets a LEFT JOIN evidence e ON e.asset_id = a.id WHERE e.id IS NULL`},
		{"relationships without evidence", `SELECT r.id FROM relationships r LEFT JOIN evidence e ON e.relationship_id = r.id WHERE e.id IS NULL`},
		{"orphaned evidence", `SELECT e.id FROM evidence e LEFT JOIN sources s ON s.id = e.source_id WHERE s.id IS NULL`},
	}
	for _, check := range checks {
		rows, err := db.Query(check.query)
		if err != nil {
			issues = append(issues, fmt.Sprintf("could not run %s check: %v", check.name, err))
			continue
		}
		if rows.Next() {
			var id any
			_ = rows.Scan(&id)
			issues = append(issues, fmt.Sprintf("%s (record %v)", check.name, id))
		}
		rows.Close()
	}
	root := projectRoot(db)
	if root == "" {
		issues = append(issues, "project root is not recorded; rerun scan or ingest")
		return sortedUnique(issues)
	}
	serviceRows, err := db.Query(`SELECT name, source_root FROM service_configs ORDER BY name`)
	if err != nil {
		issues = append(issues, fmt.Sprintf("could not read configured service roots: %v", err))
	} else {
		for serviceRows.Next() {
			var name, sourceRoot string
			if err := serviceRows.Scan(&name, &sourceRoot); err != nil {
				issues = append(issues, fmt.Sprintf("could not read configured service root: %v", err))
				break
			}
			path := filepath.Join(root, filepath.FromSlash(sourceRoot))
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() || !isWithin(root, path) {
				issues = append(issues, fmt.Sprintf("service %q source root %q is missing or invalid; update %s and rerun scan",
					name, sourceRoot, ConfigFilename))
			}
		}
		serviceRows.Close()
	}
	sourceRows, err := db.Query(`SELECT path, sha256 FROM sources ORDER BY path`)
	if err != nil {
		issues = append(issues, fmt.Sprintf("could not read sources: %v", err))
	} else {
		for sourceRows.Next() {
			var path, expected string
			if err := sourceRows.Scan(&path, &expected); err != nil {
				issues = append(issues, fmt.Sprintf("could not read source record: %v", err))
				break
			}
			fullPath := filepath.Join(root, filepath.FromSlash(path))
			if !isWithin(root, fullPath) {
				issues = append(issues, fmt.Sprintf("evidence source %q escapes the project root; rerun scan", path))
				continue
			}
			content, err := os.ReadFile(fullPath)
			if err != nil {
				issues = append(issues, fmt.Sprintf("evidence source %q is unavailable; rerun scan after restoring it", path))
				continue
			}
			if sourceHash(content) != expected {
				issues = append(issues, fmt.Sprintf("evidence source %q changed since scan; rerun scan and build", path))
			}
		}
		sourceRows.Close()
	}
	return sortedUnique(issues)
}

func projectRoot(db *sql.DB) string {
	var root string
	if err := db.QueryRow(`SELECT value FROM project_metadata WHERE key = 'project_root'`).Scan(&root); err != nil {
		return ""
	}
	return root
}

func sourceHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func sortedUnique(values []string) []string {
	sort.Strings(values)
	return slices.Compact(values)
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
