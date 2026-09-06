// Package detached is synthetic maintenance code without a configured service.
package detached

import (
	"context"
	"database/sql"
)

func PreviewReports(ctx context.Context, db *sql.DB) (*sql.Rows, error) {
	return db.QueryContext(ctx, "SELECT id, title FROM reports")
}
