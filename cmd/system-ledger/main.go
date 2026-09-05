package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Siddhant-K-code/system-ledger/internal/ledger"
	_ "modernc.org/sqlite"
)

const usage = `system-ledger maps API and database evidence into a local SQLite ledger.

Usage:
  system-ledger <command> [options] [arguments]

Commands:
  ingest <directory>  Parse OpenAPI JSON/YAML and SQL DDL into the ledger.
  build               Construct conservative deterministic inferred links.
  explain <name>      Explain an asset and its directly linked assets.
  impact <query>      Show the direct dependency graph for an asset.
  verify              Validate ledger integrity and source provenance.

Every command accepts --db <path> (default: .system-ledger/ledger.db).
Run "system-ledger <command> --help" for command-specific help.
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

	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	dbPath := fs.String("db", ".system-ledger/ledger.db", "Path to the SQLite ledger")
	fs.Usage = func() {
		fmt.Fprintf(errOut, "Usage: system-ledger %s [--db path] %s\n", command, commandArguments(command))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := ensureDatabaseParent(*dbPath); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer db.Close()
	if err := ledger.Migrate(db); err != nil {
		return err
	}

	switch command {
	case "ingest":
		if fs.NArg() != 1 {
			fs.Usage()
			return errors.New("ingest requires exactly one directory")
		}
		stats, err := ledger.Ingest(db, fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Ingested %d sources: %d APIs, %d operations, %d schemas, %d tables.\n",
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
		fmt.Fprintf(out, "Built %d inferred relationships.\n", count)
	case "explain":
		if fs.NArg() != 1 {
			fs.Usage()
			return errors.New("explain requires exactly one asset name")
		}
		return ledger.Explain(db, out, fs.Arg(0))
	case "impact":
		if fs.NArg() != 1 {
			fs.Usage()
			return errors.New("impact requires exactly one asset query")
		}
		return ledger.Impact(db, out, fs.Arg(0))
	case "verify":
		if fs.NArg() != 0 {
			fs.Usage()
			return errors.New("verify accepts no arguments")
		}
		if err := ledger.Verify(db); err != nil {
			return err
		}
		fmt.Fprintln(out, "Ledger verification passed.")
	default:
		fmt.Fprint(errOut, usage)
		return fmt.Errorf("unknown command %q", command)
	}
	return nil
}

func commandArguments(command string) string {
	switch command {
	case "ingest":
		return "<directory>"
	case "explain":
		return "<name>"
	case "impact":
		return "<query>"
	case "build", "verify":
		return ""
	default:
		return "[arguments]"
	}
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
