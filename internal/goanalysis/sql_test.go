package goanalysis

import "testing"

func TestSQLGrammarSupportedStatements(t *testing.T) {
	tests := []struct{ sql, schema, table, access string }{
		{"SELECT id, name FROM reports WHERE id = $1", "", "reports", "read"},
		{"SELECT * FROM public.reports", "public", "reports", "read"},
		{`SELECT "Id" FROM "Reporting"."Reports";`, "Reporting", "Reports", "read"},
		{"SELECT COUNT(*) FROM reports WHERE (id > ? AND name LIKE ?) OR deleted IS NULL", "", "reports", "read"},
		{"SELECT COUNT(*) AS total FROM reports", "", "reports", "read"},
		{"SELECT reports.* FROM reports", "", "reports", "read"},
		{"SELECT r.id FROM reports AS r WHERE r.id IN (1, 2, ?) ORDER BY r.id DESC LIMIT 10 OFFSET 2", "", "reports", "read"},
		{"INSERT INTO reports (id, name) VALUES (?, ?), (?, ?) RETURNING id", "", "reports", "write"},
		{"UPDATE public.reports SET name = ?, count = count + 1 WHERE id = ?", "public", "reports", "write"},
		{"DELETE FROM reports WHERE id = :id RETURNING id", "", "reports", "write"},
		{"/* FROM bogus */ SELECT name FROM reports WHERE name = 'FROM not_a_table' -- JOIN ghosts", "", "reports", "read"},
	}
	for _, test := range tests {
		t.Run(test.sql, func(t *testing.T) {
			ref, err := parseSQL(test.sql)
			if err != nil || ref.schema != test.schema || ref.table != test.table || ref.access != test.access {
				t.Fatalf("reference=%+v error=%v", ref, err)
			}
		})
	}
}

func TestSQLGrammarRejectsUnsupportedOrMalformedStatements(t *testing.T) {
	tests := []string{
		"", "SELECT", "SELECT FROM reports", "SELECT id reports", "SELECT id FROM",
		"SELECT id FROM reports JOIN owners ON reports.id = owners.id",
		"SELECT id FROM reports, owners",
		"SELECT id FROM reports LEFT JOIN owners ON true",
		"WITH reports AS (SELECT id FROM hidden) SELECT id FROM reports",
		"SELECT id FROM (SELECT id FROM reports) r",
		"SELECT (SELECT id FROM hidden) FROM reports",
		"SELECT id FROM reports UNION SELECT id FROM owners",
		"SELECT id FROM reports; DELETE FROM owners",
		"SELECT id FROM reports WHERE",
		"SELECT id FROM reports WHERE id IN (SELECT id FROM hidden)",
		"SELECT id FROM reports GROUP BY id",
		"SELECT id FROM reports WHERE id =",
		"SELECT id FROM reports WHERE id = ? garbage",
		"INSERT INTO reports SELECT id FROM owners",
		"INSERT INTO reports (id) VALUES",
		"INSERT INTO reports (id) VALUES (?) ON CONFLICT DO NOTHING",
		"UPDATE reports",
		"UPDATE reports SET = ?",
		"UPDATE reports SET id =",
		"UPDATE reports SET id = *",
		"INSERT INTO reports (id) VALUES (*)",
		"SELECT * * * FROM reports",
		"SELECT id FROM reports WHERE * = ?",
		"SELECT id FROM reports LIMIT 'ten'",
		"DELETE reports",
		"DELETE FROM reports USING owners",
		"SELECT id FROM reports /* unterminated",
		"SELECT id FROM reports WHERE name = 'unterminated",
		"SELECT id FROM reports WHERE name = E'escape'",
		"SELECT custom_function(id) FROM reports",
		"SELECT id FROM reports WHERE id = $",
		"SELECT id FROM reports WHERE id = !",
	}
	for _, sql := range tests {
		t.Run(sql, func(t *testing.T) {
			if ref, err := parseSQL(sql); err == nil || ref != (sqlReference{}) {
				t.Fatalf("accepted unsupported SQL: %+v, %v", ref, err)
			}
		})
	}
}
