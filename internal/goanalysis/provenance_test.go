package goanalysis

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCrossFileLiteralEvidenceHasExactLocations(t *testing.T) {
	result := analyzeSources(t, map[string]string{
		"services/reports/constants.go": "package p\n" +
			"const columns = \"SELECT id \"\n" +
			"const relation = `FROM reports\nWHERE token = 'literal-secret-cross-file'`\n" +
			"const query = columns + relation\n" +
			"const verb = \"GET \"\n" +
			"const routePath = \"/reports\"\n" +
			"const pattern = verb + routePath\n" +
			"const alias = query\n",
		"services/reports/use.go": `package p
import ("database/sql"; "net/http")
func target(http.ResponseWriter,*http.Request){}
func run(db *sql.DB) {
 localQuery := alias
 localPattern := pattern
 db.Query(localQuery)
 http.HandleFunc(localPattern,target)
}`,
	})
	queries, routes := nodesOf(result, "go_query"), nodesOf(result, "go_route")
	if len(queries) != 1 || len(routes) != 1 || len(result.TableRefs) != 1 {
		t.Fatalf("unexpected inventory: %+v", result)
	}
	wantQuery := []Locator{
		{Path: "services/reports/constants.go", Line: 2, EndLine: 2},
		{Path: "services/reports/constants.go", Line: 3, EndLine: 4},
	}
	wantRoute := []Locator{
		{Path: "services/reports/constants.go", Line: 6, EndLine: 6},
		{Path: "services/reports/constants.go", Line: 7, EndLine: 7},
	}
	if queries[0].Path != "services/reports/use.go" || queries[0].Line != 7 || !reflect.DeepEqual(queries[0].Evidence, wantQuery) {
		t.Fatalf("query use/definition evidence: %+v", queries[0])
	}
	if routes[0].Path != "services/reports/use.go" || routes[0].Line != 8 || !reflect.DeepEqual(routes[0].Evidence, wantRoute) {
		t.Fatalf("route use/definition evidence: %+v", routes[0])
	}
	for _, node := range result.Nodes {
		if node.Kind != "go_query" && node.Kind != "go_route" && len(node.Evidence) != 0 {
			t.Fatalf("unexpected evidence on %s", node.Kind)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"literal-secret-cross-file", "SELECT id ", "FROM reports", "const columns"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("evidence serialized source text %q", private)
		}
	}
}

func TestSafeAssignmentsMergeLiteralEvidence(t *testing.T) {
	result := analyzeSources(t, map[string]string{"a.go": `package p
import "database/sql"
const base = "SELECT id FROM reports"
func run(db *sql.DB, cond bool) {
 query := base
 query = query + ""
 if cond {
  query = "SELECT id FROM reports"
 }
 db.Query(query)
}`})
	queries := nodesOf(result, "go_query")
	want := []Locator{
		{Path: "a.go", Line: 3, EndLine: 3},
		{Path: "a.go", Line: 6, EndLine: 6},
		{Path: "a.go", Line: 8, EndLine: 8},
	}
	if len(queries) != 1 || !reflect.DeepEqual(queries[0].Evidence, want) {
		t.Fatalf("safe assignment evidence: %+v", queries)
	}
}

func TestReassignmentReplacesPreviousLiteralEvidence(t *testing.T) {
	result := analyzeSources(t, map[string]string{"a.go": `package p
import "database/sql"
const first = "SELECT id FROM first_reports"
const second = "SELECT id FROM second_reports"
func run(db *sql.DB) {
 query := first
 query = second
 db.Query(query)
}`})
	queries := nodesOf(result, "go_query")
	if len(queries) != 1 || !reflect.DeepEqual(queries[0].Evidence, []Locator{{Path: "a.go", Line: 4, EndLine: 4}}) {
		t.Fatalf("assignment retained stale evidence: %+v", queries)
	}
}

func TestMemoizedOriginsStayDeduplicatedAndIndependent(t *testing.T) {
	start := time.Now()
	result := analyzeSources(t, map[string]string{"a.go": doublingSource("", 30)})
	queries := nodesOf(result, "go_query")
	if time.Since(start) > 2*time.Second {
		t.Fatal("empty doubling provenance exceeded the regression time budget")
	}
	want := []Locator{{Path: "a.go", Line: 3, EndLine: 3}, {Path: "a.go", Line: 34, EndLine: 34}}
	if len(queries) != 1 || !reflect.DeepEqual(queries[0].Evidence, want) || hasGap(result, "go_evaluation_limit") {
		t.Fatalf("memoized provenance was duplicated: %+v", result)
	}

	repeated := analyzeSources(t, map[string]string{"a.go": `package p
import "database/sql"
const query = "SELECT id FROM reports"
func run(db *sql.DB){ db.Query(query); db.Query(query) }`})
	queries = nodesOf(repeated, "go_query")
	if len(queries) != 2 || len(queries[0].Evidence) != 1 || len(queries[1].Evidence) != 1 {
		t.Fatalf("repeated queries: %+v", queries)
	}
	queries[0].Evidence[0].Line = -1
	if queries[1].Evidence[0].Line != 3 {
		t.Fatal("exported evidence slices share mutable storage")
	}
}

func TestUniqueLiteralOriginsAreBounded(t *testing.T) {
	for _, kind := range []string{"go_query", "go_route"} {
		for _, count := range []int{maxEvaluationOrigins, maxEvaluationOrigins + 1} {
			t.Run(fmt.Sprintf("%s/%d", kind, count), func(t *testing.T) {
				start := time.Now()
				result := analyzeSources(t, map[string]string{"a.go": provenanceSource(kind, count)})
				if time.Since(start) > 2*time.Second {
					t.Fatal("origin expansion exceeded the regression time budget")
				}
				nodes := nodesOf(result, kind)
				if count > maxEvaluationOrigins {
					if len(nodes) != 0 || len(result.TableRefs) != 0 || !hasGap(result, "go_evaluation_limit") {
						t.Fatalf("origin cap was not enforced: %+v", result)
					}
				} else if len(nodes) != 1 || len(nodes[0].Evidence) != count || hasGap(result, "go_evaluation_limit") {
					t.Fatalf("supported origin count was dropped: %+v", result)
				}
			})
		}
	}
}

func provenanceSource(kind string, count int) string {
	var source strings.Builder
	base := "SELECT id FROM reports"
	source.WriteString("package p\n")
	if kind == "go_route" {
		base = "GET /reports"
		source.WriteString("import \"net/http\"\nfunc target(http.ResponseWriter,*http.Request){}\n")
	} else {
		source.WriteString("import \"database/sql\"\n")
	}
	var names []string
	for i := 0; i < count; i++ {
		text := ""
		if i == 0 {
			text = base
		}
		name := fmt.Sprintf("C%d", i)
		names = append(names, name)
		fmt.Fprintf(&source, "const %s=%q\n", name, text)
	}
	for level := 0; len(names) > 1; level++ {
		var next []string
		for i := 0; i < len(names); i += 2 {
			if i+1 == len(names) {
				next = append(next, names[i])
				continue
			}
			name := fmt.Sprintf("M%d_%d", level, i)
			fmt.Fprintf(&source, "const %s=%s+%s\n", name, names[i], names[i+1])
			next = append(next, name)
		}
		names = next
	}
	if kind == "go_route" {
		fmt.Fprintf(&source, "func run(){http.HandleFunc(%s,target)}\n", names[0])
	} else {
		fmt.Fprintf(&source, "func run(db *sql.DB){db.Query(%s)}\n", names[0])
	}
	return source.String()
}
