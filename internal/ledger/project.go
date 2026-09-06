package ledger

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ConfigFilename = "system-ledger.yaml"
	LedgerDirname  = ".system-ledger"
	LedgerFilename = "ledger.db"
)

const defaultConfig = `version: 1
services: []
`

type Config struct {
	Version    int              `yaml:"version"`
	Services   []Service        `yaml:"services"`
	Assistance AssistanceConfig `yaml:"assistance,omitempty"`
}

type AssistanceConfig struct {
	Provider string `yaml:"provider,omitempty"`
	Model    string `yaml:"model,omitempty"`
}

type Service struct {
	Name   string   `yaml:"name"`
	Owner  string   `yaml:"owner"`
	Source string   `yaml:"source"`
	Domain string   `yaml:"domain,omitempty"`
	Tags   []string `yaml:"tags,omitempty"`
}

type InitResult struct {
	ProjectRoot   string
	ConfigPath    string
	ConfigCreated bool
	LedgerPath    string
	LedgerCreated bool
}

type ScanResult struct {
	Stats       Stats
	Diagnostics []Diagnostic `json:"extraction_gaps"`
}

// Init creates only the project-owned locations that do not already exist.
func Init(projectRoot string) (InitResult, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return InitResult{}, fmt.Errorf("resolve project directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return InitResult{}, fmt.Errorf("create project directory: %w", err)
	}
	configPath := filepath.Join(root, ConfigFilename)
	configCreated, err := writeIfAbsent(configPath, []byte(defaultConfig), 0o644)
	if err != nil {
		return InitResult{}, fmt.Errorf("initialize manifest: %w", err)
	}
	ledgerDirectory := filepath.Join(root, LedgerDirname)
	if err := os.MkdirAll(ledgerDirectory, 0o755); err != nil {
		return InitResult{}, fmt.Errorf("create ledger directory: %w", err)
	}
	ledgerPath := filepath.Join(ledgerDirectory, LedgerFilename)
	ledgerCreated, err := writeIfAbsent(ledgerPath, nil, 0o600)
	if err != nil {
		return InitResult{}, fmt.Errorf("initialize ledger file: %w", err)
	}
	return InitResult{
		ProjectRoot:   root,
		ConfigPath:    configPath,
		ConfigCreated: configCreated,
		LedgerPath:    ledgerPath,
		LedgerCreated: ledgerCreated,
	}, nil
}

func LoadConfig(projectRoot string) (Config, error) {
	path := filepath.Join(projectRoot, ConfigFilename)
	content, err := readSource(projectRoot, path)
	if err != nil {
		return Config{}, fmt.Errorf("read manifest %q: %w", path, err)
	}
	return parseConfig(path, content)
}

func parseConfig(path string, content []byte) (Config, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("parse manifest %q: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("manifest %q must contain exactly one YAML document", path)
	}
	if config.Version != 1 {
		return Config{}, fmt.Errorf("manifest %q must declare version: 1", path)
	}
	seenNames := make(map[string]struct{})
	seenRoots := make(map[string]struct{})
	for i := range config.Services {
		service := &config.Services[i]
		service.Source = filepath.ToSlash(filepath.Clean(service.Source))
		if service.Name == "" || service.Owner == "" || service.Source == "" || service.Source == "." {
			return Config{}, fmt.Errorf("service %d must provide non-empty name, owner, and source", i+1)
		}
		if filepath.IsAbs(service.Source) || service.Source == ".." || strings.HasPrefix(service.Source, "../") {
			return Config{}, fmt.Errorf("service %q source must be a project-relative directory", service.Name)
		}
		if _, exists := seenNames[service.Name]; exists {
			return Config{}, fmt.Errorf("service name %q is duplicated", service.Name)
		}
		if _, exists := seenRoots[service.Source]; exists {
			return Config{}, fmt.Errorf("service source %q is duplicated", service.Source)
		}
		sort.Strings(service.Tags)
		seenNames[service.Name] = struct{}{}
		seenRoots[service.Source] = struct{}{}
	}
	sort.Slice(config.Services, func(i, j int) bool { return config.Services[i].Name < config.Services[j].Name })
	return config, nil
}

// Scan discovers supported source files under the project root, then binds the
// extracted assets to explicit service roots from the manifest.
func Scan(db *sql.DB, projectRoot string) (ScanResult, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return ScanResult{}, fmt.Errorf("resolve project directory: %w", err)
	}
	manifest, err := readSource(root, filepath.Join(root, ConfigFilename))
	if err != nil {
		return ScanResult{}, fmt.Errorf("read project manifest: %w", err)
	}
	config, err := parseConfig(ConfigFilename, manifest)
	if err != nil {
		return ScanResult{}, err
	}
	if err := validateServiceRoots(root, config.Services); err != nil {
		return ScanResult{}, err
	}
	stats, err := ingest(db, root, &config, manifest)
	if err != nil {
		return ScanResult{}, err
	}
	gaps, err := Diagnostics(db)
	if err != nil {
		return ScanResult{}, err
	}
	return ScanResult{Stats: stats, Diagnostics: gaps}, nil
}

func attachServices(tx *sql.Tx, config Config, content []byte) error {
	manifestID, err := insertSource(tx, ConfigFilename, content)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM service_configs`); err != nil {
		return err
	}
	for _, service := range config.Services {
		tags, err := json.Marshal(service.Tags)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO service_configs(name, owner, source_root, domain, tags) VALUES (?, ?, ?, ?, ?)`,
			service.Name, service.Owner, service.Source, service.Domain, string(tags)); err != nil {
			return fmt.Errorf("record service configuration %q: %w", service.Name, err)
		}
		attributes := map[string]string{"owner": service.Owner, "source": service.Source}
		if service.Domain != "" {
			attributes["domain"] = service.Domain
		}
		if len(service.Tags) != 0 {
			attributes["tags"] = strings.Join(service.Tags, ",")
		}
		serviceID, err := insertAsset(tx, manifestID, "service", service.Name, service.Name, attributes, "services."+service.Name)
		if err != nil {
			return err
		}
		assetRows, err := tx.Query(`SELECT a.id FROM assets a JOIN sources s ON s.id = a.source_id
			WHERE a.kind != 'service' AND (s.path = ? OR substr(s.path, 1, length(?)) = ?)
			ORDER BY s.path, a.kind, a.name, a.id`, service.Source, service.Source+"/", service.Source+"/")
		if err != nil {
			return err
		}
		var assetIDs []int64
		for assetRows.Next() {
			var assetID int64
			if err := assetRows.Scan(&assetID); err != nil {
				assetRows.Close()
				return err
			}
			assetIDs = append(assetIDs, assetID)
		}
		if err := assetRows.Err(); err != nil {
			assetRows.Close()
			return err
		}
		if err := assetRows.Close(); err != nil {
			return err
		}
		for _, assetID := range assetIDs {
			if err := insertRelationship(tx, serviceID, assetID, "owns_asset", "source-derived", manifestID,
				"services."+service.Name+".source"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateServiceRoots(projectRoot string, services []Service) error {
	for _, service := range services {
		root := filepath.Join(projectRoot, filepath.FromSlash(service.Source))
		if err := validateSourcePath(projectRoot, root); err != nil {
			return fmt.Errorf("service %q source escapes project root", service.Name)
		}
		info, err := os.Stat(root)
		if err != nil {
			return fmt.Errorf("service %q source %q is unavailable; update %s or create the directory: %w",
				service.Name, service.Source, ConfigFilename, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("service %q source %q is not a directory", service.Name, service.Source)
		}
	}
	return nil
}

func isWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func writeIfAbsent(path string, content []byte, mode os.FileMode) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	_, err = file.Write(content)
	return err == nil, err
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ProjectDatabasePath(projectRoot string) string {
	return filepath.Join(projectRoot, LedgerDirname, LedgerFilename)
}

func WriteScanText(out io.Writer, result ScanResult) {
	stats := result.Stats
	fmt.Fprintf(out, "Scanned %d candidate files: %d sources, %d services, %d APIs, %d operations, %d schemas, %d tables.\n",
		stats.CandidateFiles, stats.Sources, stats.Services, stats.APIs, stats.Operations, stats.Schemas, stats.Tables)
	if stats.SkippedFiles > 0 {
		fmt.Fprintf(out, "Skipped %d YAML/JSON files that are not OpenAPI documents.\n", stats.SkippedFiles)
	}
	fmt.Fprintf(out, "Go source inventory: %d functions, %d routes, %d queries; %d extraction gaps (see summary).\n",
		stats.Functions, stats.Routes, stats.Queries, stats.Gaps)
	writeDiagnostics(out, result.Diagnostics)
}
