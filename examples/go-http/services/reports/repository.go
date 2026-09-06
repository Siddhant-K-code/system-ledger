package reports

import "context"

type Report struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

const reportColumns = "SELECT id, title FROM reports"
const listReportsSQL = reportColumns + " ORDER BY id"

func (s *Server) loadReports(ctx context.Context) ([]Report, error) {
	rows, err := s.db.QueryContext(ctx, listReportsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reports := []Report{}
	for rows.Next() {
		var report Report
		if err := rows.Scan(&report.ID, &report.Title); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

func (s *Server) searchReports(ctx context.Context, activeOnly bool) ([]Report, error) {
	query := reportColumns
	if activeOnly {
		query += " WHERE status = 'active'"
	}
	// Conditional SQL is intentionally unsupported by the static analyzer.
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reports := []Report{}
	for rows.Next() {
		var report Report
		if err := rows.Scan(&report.ID, &report.Title); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, rows.Err()
}
