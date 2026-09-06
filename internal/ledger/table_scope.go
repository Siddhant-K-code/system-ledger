package ledger

import (
	"database/sql"
)

type tableTarget struct {
	ID       int64
	SourceID int64
	Locator  string
}

// Scope is explicit service configuration plus exact SQL identifiers, not
// cross-service name inference or knowledge of a connection's search_path.
func resolveSourceTable(tx *sql.Tx, config *Config, sourcePath, target string) (tableTarget, string, error) {
	service := sourceService(sourcePath, config)
	if service == "" {
		return tableTarget{}, "SQL source needs exactly one configured service root.", nil
	}
	rows, err := tx.Query(`SELECT a.id, a.source_id, a.canonical_name, s.path, e.locator
		FROM assets a JOIN sources s ON s.id = a.source_id JOIN evidence e ON e.asset_id = a.id WHERE a.kind = 'table'
		ORDER BY s.path, a.canonical_name, a.id`)
	if err != nil {
		return tableTarget{}, "", err
	}
	defer rows.Close()
	var matches []tableTarget
	for rows.Next() {
		var id, sourceID int64
		var canonical, path, locator string
		if err := rows.Scan(&id, &sourceID, &canonical, &path, &locator); err != nil {
			return tableTarget{}, "", err
		}
		if sourceService(path, config) != service {
			continue
		}
		if canonical == target {
			matches = append(matches, tableTarget{ID: id, SourceID: sourceID, Locator: locator})
		}
	}
	if err := rows.Err(); err != nil {
		return tableTarget{}, "", err
	}
	switch len(matches) {
	case 0:
		return tableTarget{}, "No exact scanned DDL table matches inside the query's service.", nil
	case 1:
		return matches[0], "", nil
	default:
		return tableTarget{}, "Multiple scanned DDL tables match inside the query's service; schema or DDL scope is ambiguous.", nil
	}
}
