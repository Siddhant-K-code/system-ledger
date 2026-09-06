package ledger

import (
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxSourceBytes = 4 << 20

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
	Locator string `json:"locator"`
}

type sourceInput struct {
	Path    string
	Content []byte
}

func applySourceSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE diagnostics (id INTEGER PRIMARY KEY, code TEXT NOT NULL, message TEXT NOT NULL, path TEXT NOT NULL, locator TEXT NOT NULL)`,
		`CREATE TABLE scanned_files (path TEXT PRIMARY KEY, sha256 TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version) VALUES (3)`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply source schema migration: %w", err)
		}
	}
	return tx.Commit()
}

func insertDiagnostic(tx *sql.Tx, gap Diagnostic) error {
	_, err := tx.Exec(`INSERT INTO diagnostics(code, message, path, locator) VALUES (?, ?, ?, ?)`,
		gap.Code, gap.Message, gap.Path, gap.Locator)
	return err
}

func Diagnostics(db queryer) ([]Diagnostic, error) {
	rows, err := db.Query(`SELECT code, message, path, locator FROM diagnostics ORDER BY path, locator, code, message`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gaps := []Diagnostic{}
	for rows.Next() {
		var gap Diagnostic
		if err := rows.Scan(&gap.Code, &gap.Message, &gap.Path, &gap.Locator); err != nil {
			return nil, err
		}
		gaps = append(gaps, gap)
	}
	return gaps, rows.Err()
}

// Sources never follow symlinks, including links to other files inside the root.
func validateSourcePath(root, path string) error {
	if !isWithin(root, path) {
		return fmt.Errorf("source is outside the project root")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("project root must be a directory, not a symlink")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path contains a symlink")
		}
	}
	return nil
}

func readSource(root, path string) ([]byte, error) {
	if err := validateSourcePath(root, path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source is not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxSourceBytes {
		return nil, fmt.Errorf("source exceeds the %d-byte limit", maxSourceBytes)
	}
	return content, nil
}

func generatedGo(content []byte) bool {
	file, _ := parser.ParseFile(token.NewFileSet(), "", content, parser.PackageClauseOnly|parser.ParseComments)
	return file != nil && ast.IsGenerated(file)
}

func credentialFilename(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "credentials.") ||
		strings.HasPrefix(name, "secrets.") || strings.HasPrefix(name, "service-account.") ||
		strings.HasPrefix(name, "service_account.")
}

func sourceService(path string, config *Config) string {
	if config == nil {
		return ""
	}
	var matches []string
	for _, service := range config.Services {
		if path == service.Source || strings.HasPrefix(path, service.Source+"/") {
			matches = append(matches, service.Name)
		}
	}
	if len(matches) != 1 {
		return ""
	}
	return matches[0]
}

func writeDiagnostics(out io.Writer, gaps []Diagnostic) {
	if len(gaps) == 0 {
		return
	}
	fmt.Fprintln(out, "Extraction gaps (not integrity failures):")
	for _, gap := range gaps {
		fmt.Fprintf(out, "- %s: %s @ %s: %s\n", gap.Code, gap.Path, gap.Locator, gap.Message)
	}
}

func gapsForEvidence(db *sql.DB, evidence []EvidenceRef) ([]Diagnostic, error) {
	gaps, err := Diagnostics(db)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]bool)
	for _, reference := range evidence {
		paths[reference.Path] = true
	}
	result := []Diagnostic{}
	for _, gap := range gaps {
		if paths[gap.Path] {
			result = append(result, gap)
		}
	}
	return result, nil
}

func writeAssetDiagnostics(db *sql.DB, out io.Writer, id int64) error {
	teams, err := teamsFor(db, id)
	if err != nil {
		return err
	}
	if len(teams) > 0 {
		fmt.Fprintf(out, "Teams: %s\n", strings.Join(teams, ", "))
	}
	evidence, err := evidenceFor(db, "asset_id", id)
	if err != nil {
		return err
	}
	gaps, err := gapsForEvidence(db, evidence)
	if err != nil {
		return err
	}
	writeDiagnostics(out, gaps)
	return nil
}

func inventoryIssues(db queryer, root string) []string {
	rows, err := db.Query(`SELECT path, sha256 FROM scanned_files ORDER BY path`)
	if err != nil {
		return []string{"could not read scan inventory; rerun scan"}
	}
	expected := make(map[string]string)
	for rows.Next() {
		var path, hash string
		if err := rows.Scan(&path, &hash); err != nil {
			rows.Close()
			return []string{"invalid scan inventory; rerun scan"}
		}
		expected[path] = hash
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return []string{"could not read scan inventory; rerun scan"}
	}
	files, err := supportedFiles(root)
	if err != nil {
		return []string{"source inventory is unavailable; rerun scan"}
	}
	var issues []string
	for _, path := range files {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return []string{"source inventory is invalid; rerun scan"}
		}
		relative = filepath.ToSlash(relative)
		hash, found := expected[relative]
		if !found {
			issues = append(issues, fmt.Sprintf("new supported source %q is not scanned; rerun scan and build", relative))
		} else {
			content, err := readSource(root, path)
			if err != nil || sourceHash(content) != hash {
				issues = append(issues, fmt.Sprintf("scan input %q changed or is unavailable; rerun scan and build", relative))
			}
		}
		delete(expected, relative)
	}
	for path := range expected {
		issues = append(issues, fmt.Sprintf("scan input %q was removed or excluded; rerun scan and build", path))
	}
	return sortedUnique(issues)
}
