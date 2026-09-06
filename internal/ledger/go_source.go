package ledger

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Siddhant-K-code/system-ledger/internal/goanalysis"
)

func ingestGo(tx *sql.Tx, inputs []sourceInput, sources map[string]int64, config *Config, stats *Stats) error {
	files := make([]goanalysis.File, len(inputs))
	for i, file := range inputs {
		files[i] = goanalysis.File{Path: file.Path, Content: file.Content}
	}
	result, err := goanalysis.Analyze(files)
	if err != nil {
		return fmt.Errorf("analyze Go source (previous ledger preserved): %w", err)
	}
	ids := make(map[string]int64)
	for _, node := range result.Nodes {
		sourceID, found := sources[node.Path]
		if !found {
			return fmt.Errorf("Go fact has no ingested source: %q", node.Path)
		}
		kind := strings.TrimPrefix(node.Kind, "go_")
		id, err := insertAsset(tx, sourceID, kind, node.Name, node.Key, node.Attributes, goLocator(node.Locator))
		if err != nil {
			return err
		}
		ids[node.Key] = id
		for _, location := range node.Evidence {
			supportingSource, found := sources[location.Path]
			if !found {
				return fmt.Errorf("Go fact has untracked supporting source: %q", location.Path)
			}
			if _, err := tx.Exec(`INSERT INTO evidence(source_id, asset_id, locator)
				SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM evidence WHERE source_id = ? AND asset_id = ? AND locator = ?)`,
				supportingSource, id, goLocator(location), supportingSource, id, goLocator(location)); err != nil {
				return err
			}
		}
		switch kind {
		case "package":
			stats.Packages++
		case "function":
			stats.Functions++
		case "route":
			stats.Routes++
		case "query":
			stats.Queries++
		}
	}
	for _, relation := range result.Relations {
		from, fromFound := ids[relation.From]
		to, toFound := ids[relation.To]
		sourceID, sourceFound := sources[relation.Path]
		if !fromFound || !toFound || !sourceFound {
			return fmt.Errorf("Go relationship has unresolved extracted endpoints or source")
		}
		if err := insertRelationship(tx, from, to, relation.Kind, "source-derived", sourceID, goLocator(relation.Locator)); err != nil {
			return err
		}
	}
	for _, reference := range result.TableRefs {
		if strings.Contains(reference.Schema, ".") || strings.Contains(reference.Table, ".") {
			if err := insertDiagnostic(tx, Diagnostic{Code: "go_unresolved_table", Message: "Quoted identifiers containing dots are outside the supported DDL identifier subset.", Path: reference.Path, Locator: goLocator(reference.Locator)}); err != nil {
				return err
			}
			continue
		}
		target := reference.Table
		if reference.Schema != "" {
			target = reference.Schema + "." + target
		}
		table, reason, err := resolveSourceTable(tx, config, reference.Path, target)
		if err != nil {
			return err
		}
		if reason != "" {
			if err := insertDiagnostic(tx, Diagnostic{Code: "go_unresolved_table", Message: reason, Path: reference.Path, Locator: goLocator(reference.Locator)}); err != nil {
				return err
			}
			continue
		}
		queryID, found := ids[reference.QueryKey]
		sourceID, sourceFound := sources[reference.Path]
		if !found || !sourceFound {
			return fmt.Errorf("Go SQL reference has no extracted query or source")
		}
		var kind string
		switch reference.Access {
		case "read":
			kind = "reads_table"
		case "write":
			kind = "writes_table"
		default:
			return fmt.Errorf("unsupported extracted SQL access type")
		}
		if err := insertRelationship(tx, queryID, table.ID, kind, "source-derived", sourceID, goLocator(reference.Locator)); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT source_id, locator FROM evidence WHERE asset_id = ? ORDER BY source_id, locator`, queryID)
		if err != nil {
			return err
		}
		type support struct {
			source  int64
			locator string
		}
		var supports []support
		for rows.Next() {
			var item support
			if err := rows.Scan(&item.source, &item.locator); err != nil {
				rows.Close()
				return err
			}
			supports = append(supports, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range supports {
			if err := insertRelationship(tx, queryID, table.ID, kind, "source-derived", item.source, item.locator); err != nil {
				return err
			}
		}
		if err := insertRelationship(tx, queryID, table.ID, kind, "source-derived", table.SourceID, table.Locator); err != nil {
			return err
		}
	}
	for _, gap := range result.Gaps {
		if err := insertDiagnostic(tx, Diagnostic{Code: gap.Kind, Message: gap.Message, Path: gap.Path, Locator: goLocator(gap.Locator)}); err != nil {
			return err
		}
	}
	return nil
}

func goLocator(location goanalysis.Locator) string {
	return fmt.Sprintf("L%d-L%d", location.Line, location.EndLine)
}
