package ledger

import (
	"container/list"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PathStep struct {
	Asset            AssetRef      `json:"asset"`
	Owners           []string      `json:"owners"`
	Teams            []string      `json:"teams"`
	Direction        string        `json:"direction"`
	RelationshipType string        `json:"relationship_type"`
	Origin           string        `json:"origin"`
	Evidence         []EvidenceRef `json:"evidence"`
}

type PathReport struct {
	From  AssetRef   `json:"from"`
	To    AssetRef   `json:"to"`
	Steps []PathStep `json:"steps"`
}

type DoctorReport struct {
	Healthy bool          `json:"healthy"`
	Checks  []DoctorCheck `json:"checks"`
}

type DoctorCheck struct {
	Status  string `json:"status"`
	Name    string `json:"name"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

func RecordBuild(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO project_metadata(key, value) VALUES ('last_build_at', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func RecordScan(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO project_metadata(key, value) VALUES ('last_scan_at', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, time.Now().UTC().Format(time.RFC3339))
	return err
}

func FindPath(db *sql.DB, fromQuery, toQuery string) (PathReport, error) {
	from, err := findAsset(db, fromQuery)
	if err != nil {
		return PathReport{}, err
	}
	to, err := findAsset(db, toQuery)
	if err != nil {
		return PathReport{}, err
	}
	if from.ID == to.ID {
		return PathReport{From: AssetRef{Kind: from.Kind, Name: from.Name}, To: AssetRef{Kind: to.Kind, Name: to.Name}, Steps: []PathStep{}}, nil
	}
	type edge struct {
		id, next                            int64
		direction, relationshipType, origin string
		nextKind, nextName                  string
	}
	adjacency := map[int64][]edge{}
	rows, err := db.Query(`SELECT r.id, r.from_asset_id, r.to_asset_id, r.relationship_type, r.origin,
		t.kind, t.name FROM relationships r
		JOIN assets t ON t.id = r.to_asset_id
		ORDER BY r.relationship_type, r.origin, r.from_asset_id, t.kind, t.name, r.id`)
	if err != nil {
		return PathReport{}, err
	}
	for rows.Next() {
		var id, fromID, toID int64
		var relationshipType, origin, toKind, toName string
		if err := rows.Scan(&id, &fromID, &toID, &relationshipType, &origin, &toKind, &toName); err != nil {
			rows.Close()
			return PathReport{}, err
		}
		adjacency[fromID] = append(adjacency[fromID], edge{id, toID, "outgoing", relationshipType, origin, toKind, toName})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PathReport{}, err
	}
	rows.Close()
	type trail struct {
		previous int64
		edge     edge
	}
	visited := map[int64]bool{from.ID: true}
	trails := map[int64]trail{}
	queue := list.New()
	queue.PushBack(from.ID)
	for queue.Len() > 0 && !visited[to.ID] {
		current := queue.Remove(queue.Front()).(int64)
		for _, candidate := range adjacency[current] {
			if visited[candidate.next] {
				continue
			}
			visited[candidate.next] = true
			trails[candidate.next] = trail{current, candidate}
			queue.PushBack(candidate.next)
			if candidate.next == to.ID {
				break
			}
		}
	}
	if !visited[to.ID] {
		return PathReport{}, fmt.Errorf("no relationship path between %s %q and %s %q", from.Kind, from.Name, to.Kind, to.Name)
	}
	var reversed []PathStep
	for current := to.ID; current != from.ID; {
		trail := trails[current]
		owners, err := ownersFor(db, trail.edge.next)
		if err != nil {
			return PathReport{}, err
		}
		teams, err := teamsFor(db, trail.edge.next)
		if err != nil {
			return PathReport{}, err
		}
		evidence, err := evidenceFor(db, "relationship_id", trail.edge.id)
		if err != nil {
			return PathReport{}, err
		}
		reversed = append(reversed, PathStep{
			Asset: AssetRef{Kind: trail.edge.nextKind, Name: trail.edge.nextName}, Owners: owners, Teams: teams,
			Direction: trail.edge.direction, RelationshipType: trail.edge.relationshipType,
			Origin: trail.edge.origin, Evidence: evidence,
		})
		current = trail.previous
	}
	steps := make([]PathStep, len(reversed))
	for i := range reversed {
		steps[len(reversed)-1-i] = reversed[i]
	}
	return PathReport{From: AssetRef{Kind: from.Kind, Name: from.Name}, To: AssetRef{Kind: to.Kind, Name: to.Name}, Steps: steps}, nil
}

func Doctor(db *sql.DB, root string) DoctorReport {
	checks := []DoctorCheck{}
	manifest := filepath.Join(root, ConfigFilename)
	if _, err := os.Stat(manifest); err != nil {
		checks = append(checks, DoctorCheck{"failure", "manifest", "Manifest is missing.", "Run: system-ledger init --project " + root})
	} else if _, err := LoadConfig(root); err != nil {
		checks = append(checks, DoctorCheck{"failure", "manifest", err.Error(), "Fix " + ConfigFilename + ", then run: system-ledger scan"})
	} else {
		checks = append(checks, DoctorCheck{"ok", "manifest", "Manifest is valid.", ""})
	}
	issues := validationIssues(db)
	if len(issues) > 0 {
		checks = append(checks, DoctorCheck{"failure", "evidence", issues[0], "Restore sources or configuration, then run: system-ledger scan && system-ledger build"})
	} else {
		checks = append(checks, DoctorCheck{"ok", "evidence", "Ledger evidence and service roots are valid.", ""})
	}
	if projectRoot(db) == "" {
		checks = append(checks, DoctorCheck{"warning", "scan", "No completed scan is recorded.", "Run: system-ledger scan"})
	} else if metadata(db, "last_build_at") == "" {
		checks = append(checks, DoctorCheck{"warning", "build", "Inferred links are missing or older than the last scan.", "Run: system-ledger build"})
	} else {
		checks = append(checks, DoctorCheck{"ok", "build", "Ledger is built from the latest scan.", ""})
	}
	healthy := true
	gaps, err := Diagnostics(db)
	if err != nil {
		checks = append(checks, DoctorCheck{"failure", "coverage", "Cannot read extraction diagnostics.", "Rerun scan."})
	} else if len(gaps) > 0 {
		checks = append(checks, DoctorCheck{"warning", "coverage", fmt.Sprintf("%d extraction gaps; static inventory is not a deployed build or exhaustive impact map.", len(gaps)), "Inspect: system-ledger summary"})
	}
	for _, check := range checks {
		if check.Status == "failure" {
			healthy = false
		}
	}
	return DoctorReport{Healthy: healthy, Checks: checks}
}

func metadata(db queryer, key string) string {
	var value string
	_ = db.QueryRow(`SELECT value FROM project_metadata WHERE key = ?`, key).Scan(&value)
	return value
}

func RenderPath(db *sql.DB, out io.Writer, from, to, format string) error {
	report, err := FindPath(db, from, to)
	if err != nil {
		return err
	}
	if format == "json" {
		return writeJSON(out, report)
	}
	fmt.Fprintf(out, "Path: %s %q -> %s %q\n", report.From.Kind, report.From.Name, report.To.Kind, report.To.Name)
	if len(report.Steps) == 0 {
		fmt.Fprintln(out, "Already at the requested asset.")
	}
	for index, step := range report.Steps {
		fmt.Fprintf(out, "%d. %s %s [%s] %s %q", index+1, step.Direction, step.RelationshipType, step.Origin, step.Asset.Kind, step.Asset.Name)
		if len(step.Owners) > 0 {
			fmt.Fprintf(out, " (owner: %s)", strings.Join(step.Owners, ", "))
		}
		if len(step.Teams) > 0 {
			fmt.Fprintf(out, " (team: %s)", strings.Join(step.Teams, ", "))
		}
		fmt.Fprintln(out)
		for _, evidence := range step.Evidence {
			fmt.Fprintf(out, "   %s @ %s\n", evidence.Path, evidence.Locator)
		}
	}
	return nil
}

func RenderDoctor(db *sql.DB, out io.Writer, root, format string) error {
	report := Doctor(db, root)
	if format == "json" {
		if err := writeJSON(out, report); err != nil {
			return err
		}
	} else {
		for _, check := range report.Checks {
			fmt.Fprintf(out, "%s  %-10s %s\n", strings.ToUpper(check.Status), check.Name, check.Message)
			if check.Fix != "" {
				fmt.Fprintf(out, "    %s\n", check.Fix)
			}
		}
	}
	if !report.Healthy {
		return fmt.Errorf("doctor found failures")
	}
	return nil
}
