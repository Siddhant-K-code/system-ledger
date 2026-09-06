package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Siddhant-K-code/system-ledger/internal/ledger"
	"github.com/Siddhant-K-code/system-ledger/internal/present"
	_ "modernc.org/sqlite"
)

var version = "dev"

const usage = `system-ledger maps source evidence into a local architecture ledger.

Usage:
  system-ledger <command> [options] [arguments]

Commands:
  init [directory]    Initialize a project manifest and local ledger location.
  scan                Discover supported sources and associate configured services.
  ingest <directory>  Compatibility ingestion for one source directory.
  build               Construct conservative deterministic inferred links.
  summary             Show services, asset counts, relationships, and warnings.
  explain <name>      Explain an asset and its directly linked assets.
  impact <query>      Show a direct dependency graph for an asset.
  path <from> <to>    Find a shortest direct dependency path.
  verify              Validate ledger integrity, configuration, and provenance.
  doctor              Diagnose project, ledger, and evidence health.
  ask                 Preview or explicitly authorize evidence-assisted suggestions.

All commands accept --project <directory> (default: current directory).
--db <path> remains available for an explicit ledger location. Report commands
also accept --format text|json and --color auto|always|never. Run
"system-ledger <command> --help" for details.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(out, usage)
		return nil
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintln(out, version)
		return nil
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	projectPath := fs.String("project", ".", "Project root directory")
	dbPath := fs.String("db", "", "Explicit SQLite ledger path (overrides --project)")
	format := fs.String("format", "text", "Output format: text or json (report commands only)")
	color := fs.String("color", "auto", "Color mode: auto, always, or never")
	var ask askOptions
	if command == "ask" {
		fs.StringVar(&ask.Asset, "asset", "", "Required asset name, canonical name, kind:name, or id:<integer>")
		fs.StringVar(&ask.Provider, "provider", "", "Provider: openai or anthropic (overrides project setting)")
		fs.StringVar(&ask.Model, "model", "", "Exact compatible provider model ID (overrides project setting)")
		fs.BoolVar(&ask.DryRun, "dry-run", false, "Preview the exact request body without a key or network call")
		fs.BoolVar(&ask.AllowRemote, "allow-remote", false, "Explicitly consent to sending the bounded evidence request")
	}
	fs.Usage = func() {
		fmt.Fprintf(errOut, "Usage: system-ledger %s [--project dir] [--db path] %s\n", command, commandArguments(command))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unsupported format %q; use text or json", *format)
	}
	if *color != "auto" && *color != "always" && *color != "never" {
		return fmt.Errorf("unsupported color mode %q; use auto, always, or never", *color)
	}
	renderer := present.New(*color, out)
	root, err := filepath.Abs(*projectPath)
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}
	if command == "init" && fs.NArg() == 1 {
		root, err = filepath.Abs(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("resolve project directory: %w", err)
		}
	} else if command == "init" && fs.NArg() > 1 {
		fs.Usage()
		return errors.New("init accepts at most one directory")
	}
	if command == "init" {
		result, err := ledger.Init(root)
		if err != nil {
			return err
		}
		if err := runWithDatabase(ledger.ProjectDatabasePath(result.ProjectRoot), func(db *sql.DB) error {
			return ledger.Migrate(db)
		}); err != nil {
			return err
		}
		if result.ConfigCreated {
			if *format == "json" {
				return jsonOutput(out, result)
			}
			fmt.Fprintf(out, "%s\nManifest: %s\nLedger:   %s\nNext: system-ledger scan --project %s && system-ledger build --project %s\n",
				renderer.Success("Project initialized."), result.ConfigPath, result.LedgerPath, result.ProjectRoot, result.ProjectRoot)
		} else {
			if *format == "json" {
				return jsonOutput(out, result)
			}
			fmt.Fprintf(out, "%s\nManifest: %s\nNext: system-ledger scan --project %s\n",
				renderer.Warning("Project already initialized; existing manifest preserved."), result.ConfigPath, result.ProjectRoot)
		}
		return nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("read project directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project path %q is not a directory", root)
	}
	if *dbPath == "" {
		*dbPath = ledger.ProjectDatabasePath(root)
	}
	if command == "ask" {
		if err := ask.resolve(root, fs.Args()); err != nil {
			return err
		}
	}
	return runWithDatabase(*dbPath, func(db *sql.DB) error {
		if err := ledger.Migrate(db); err != nil {
			return err
		}
		switch command {
		case "ask":
			return runAsk(db, root, ask, fs.Arg(0), out, *format)
		case "scan":
			if fs.NArg() != 0 {
				fs.Usage()
				return errors.New("scan accepts no arguments")
			}
			result, err := ledger.Scan(db, root)
			if err != nil {
				return err
			}
			if *format == "json" {
				return jsonOutput(out, result)
			}
			fmt.Fprintln(out, renderer.Heading("Scan complete"))
			ledger.WriteScanText(out, result)
		case "ingest":
			if fs.NArg() != 1 {
				fs.Usage()
				return errors.New("ingest requires exactly one directory")
			}
			input, err := filepath.Abs(fs.Arg(0))
			if err != nil {
				return err
			}
			if !within(root, input) {
				return fmt.Errorf("ingest directory %q must be within project root %q", input, root)
			}
			stats, err := ledger.Ingest(db, input)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\nIngested %d sources: %d APIs, %d operations, %d schemas, %d tables.\n",
				renderer.Heading("Ingestion complete"),
				stats.Sources, stats.APIs, stats.Operations, stats.Schemas, stats.Tables)
		case "build":
			if fs.NArg() != 0 {
				fs.Usage()
				return errors.New("build accepts no arguments")
			}
			count, err := ledger.Build(db)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\nBuilt %d inferred relationships.\n", renderer.Heading("Build complete"), count)
		case "summary":
			if fs.NArg() != 0 {
				fs.Usage()
				return errors.New("summary accepts no arguments")
			}
			return ledger.WriteSummary(db, out, *format)
		case "explain":
			if fs.NArg() != 1 {
				fs.Usage()
				return errors.New("explain requires exactly one asset name")
			}
			return ledger.RenderExplain(db, out, fs.Arg(0), *format)
		case "impact":
			if fs.NArg() != 1 {
				fs.Usage()
				return errors.New("impact requires exactly one asset query")
			}
			return ledger.RenderImpact(db, out, fs.Arg(0), *format)
		case "path":
			if fs.NArg() != 2 {
				fs.Usage()
				return errors.New("path requires a source and destination asset")
			}
			return ledger.RenderPath(db, out, fs.Arg(0), fs.Arg(1), *format)
		case "verify":
			if fs.NArg() != 0 {
				fs.Usage()
				return errors.New("verify accepts no arguments")
			}
			return ledger.RenderVerify(db, out, *format)
		case "doctor":
			if fs.NArg() != 0 {
				fs.Usage()
				return errors.New("doctor accepts no arguments")
			}
			return ledger.RenderDoctor(db, out, root, *format)
		default:
			fmt.Fprint(errOut, usage)
			return fmt.Errorf("unknown command %q", command)
		}
		return nil
	})
}

func runWithDatabase(path string, run func(*sql.DB) error) error {
	if err := ensureDatabaseParent(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return run(db)
}

func commandArguments(command string) string {
	switch command {
	case "init":
		return "[directory]"
	case "ingest":
		return "<directory>"
	case "explain":
		return "<name>"
	case "impact":
		return "<query>"
	case "path":
		return "<from> <to>"
	case "scan", "build", "summary", "verify", "doctor":
		return ""
	case "ask":
		return "--asset <name-or-id> [--provider name] [--model id] (--dry-run | --allow-remote) '<question>'"
	default:
		return "[arguments]"
	}
}

func jsonOutput(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func ensureDatabaseParent(path string) error {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	parent := filepath.Dir(path)
	if parent == "." {
		return nil
	}
	return os.MkdirAll(parent, 0o755)
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
