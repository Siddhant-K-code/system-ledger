package goanalysis

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func analyzeSources(t *testing.T, sources map[string]string) Result {
	t.Helper()
	var files []File
	for name, content := range sources {
		files = append(files, File{Path: name, Content: []byte(content)})
	}
	result, err := Analyze(files)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func nodesOf(result Result, kind string) []Node {
	var nodes []Node
	for _, node := range result.Nodes {
		if node.Kind == kind {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func relationsOf(result Result, kind string) []Relation {
	var relations []Relation
	for _, relation := range result.Relations {
		if relation.Kind == kind {
			relations = append(relations, relation)
		}
	}
	return relations
}

func hasGap(result Result, kind string) bool {
	for _, gap := range result.Gaps {
		if gap.Kind == kind {
			return true
		}
	}
	return false
}

func TestCrossFileRouteHandlerHelperQuery(t *testing.T) {
	result := analyzeSources(t, map[string]string{
		"services/reports/routes.go": `package reports
import h "net/http"
func Routes() *h.ServeMux {
 mux := h.NewServeMux()
 mux.HandleFunc("GET /reports", Reports)
 return mux
}`,
		"services/reports/handlers.go": `package reports
import ("net/http"; sq "database/sql"; "context")
var db *sq.DB
func Reports(w http.ResponseWriter, r *http.Request) {
 load(r.Context(), db)
}
func load(ctx context.Context, db *sq.DB) {
 db.QueryContext(ctx, query)
}
const query = prefix + "reports WHERE id = ?"
const prefix = "SELECT id FROM "
`,
	})
	routes := nodesOf(result, "go_route")
	if len(routes) != 1 || routes[0].Attributes["pattern"] != "GET /reports" || routes[0].Attributes["method"] != "GET" || routes[0].Attributes["path"] != "/reports" || routes[0].Path != "services/reports/routes.go" || routes[0].Line != 5 {
		t.Fatalf("routes: %+v", routes)
	}
	handlers := relationsOf(result, "handles")
	if len(handlers) != 1 || handlers[0].From != routes[0].Key || !strings.Contains(handlers[0].To, "reports.Reports") || handlers[0].Line != 5 {
		t.Fatalf("handler edges: %+v", handlers)
	}
	calls := relationsOf(result, "calls")
	if len(calls) != 1 || !strings.Contains(calls[0].From, "reports.Reports") || !strings.Contains(calls[0].To, "reports.load") || calls[0].Line != 5 {
		t.Fatalf("calls: %+v", calls)
	}
	queries := nodesOf(result, "go_query")
	if len(queries) != 1 || queries[0].Line != 8 || queries[0].Attributes["operation"] != "SELECT" || queries[0].Attributes["table"] != "reports" {
		t.Fatalf("queries: %+v", queries)
	}
	if len(result.TableRefs) != 1 || result.TableRefs[0].Table != "reports" || result.TableRefs[0].Access != "read" || result.TableRefs[0].QueryKey != queries[0].Key || result.TableRefs[0].Line != 8 {
		t.Fatalf("table references: %+v", result.TableRefs)
	}
	for _, function := range nodesOf(result, "go_function") {
		if function.Attributes["package"] != "reports" || function.Line <= 0 || function.EndLine < function.Line {
			t.Fatalf("function source: %+v", function)
		}
	}
}

func TestImportAliasesAndLexicalShadows(t *testing.T) {
	tests := []struct {
		name                               string
		body                               string
		wantRoutes, wantQueries, wantCalls int
	}{
		{"aliases", `h.HandleFunc("/ok", target); db.Query("SELECT id FROM reports"); target()`, 1, 1, 1},
		{"http parameter", `func(h fake) { h.HandleFunc("/fake", target) }(fake{})`, 0, 0, 0},
		{"http local", `h := fake{}; h.HandleFunc("/fake", target)`, 0, 0, 0},
		{"db local", `db := fake{}; db.Query("SELECT id FROM invented")`, 0, 0, 0},
		{"function local", `target := func(){}; target()`, 0, 0, 0},
		{"block scope restored", `{ h := fake{}; h.HandleFunc("/fake", target) }; h.HandleFunc("/ok", target)`, 1, 0, 0},
		{"if scope restored", `if h := (fake{}); true { h.HandleFunc("/fake", target) }; h.HandleFunc("/ok", target)`, 1, 0, 0},
		{"range shadow", `for _, h := range []fake{} { h.HandleFunc("/fake", target) }; h.HandleFunc("/ok", target)`, 1, 0, 0},
		{"unrelated Query method", `f := fake{}; f.Query("SELECT id FROM invented")`, 0, 0, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := analyzeSources(t, map[string]string{"a.go": `package p
import (h "net/http"; sq "database/sql")
type fake struct{}
func (fake) Query(s string) {}
func (fake) HandleFunc(s string, h func()) {}
func target(){}
func run(db *sq.DB) { ` + test.body + ` }`})
			if got := len(nodesOf(result, "go_route")); got != test.wantRoutes {
				t.Errorf("routes=%d want %d: %+v", got, test.wantRoutes, result)
			}
			if got := len(nodesOf(result, "go_query")); got != test.wantQueries {
				t.Errorf("queries=%d want %d", got, test.wantQueries)
			}
			if test.name == "function local" || test.name == "aliases" || test.name == "unrelated Query method" {
				if got := len(relationsOf(result, "calls")); got != test.wantCalls {
					t.Errorf("calls=%d want %d", got, test.wantCalls)
				}
			}
		})
	}
}

func TestNamesAreQualifiedByDirectoryPackageAndReceiver(t *testing.T) {
	result := analyzeSources(t, map[string]string{
		"a/one.go": `package p
type A struct{}; type B struct{}
func (a *A) Serve() { a.load() }
func (a *A) load() {}
func (b *B) load() {}
func load() {}
func run() { load() }`,
		"b/one.go": `package p
func load(){}
func run(){load()}`,
		"a/two.go": `package other
func load(){}
func run(){load()}`,
	})
	functions := nodesOf(result, "go_function")
	if len(functions) != 9 || len(nodesOf(result, "go_package")) != 3 {
		t.Fatalf("inventory: %+v", result.Nodes)
	}
	seen := map[string]bool{}
	for _, fn := range functions {
		if seen[fn.Key] {
			t.Fatalf("duplicate key %s", fn.Key)
		}
		seen[fn.Key] = true
	}
	calls := relationsOf(result, "calls")
	if len(calls) != 4 {
		t.Fatalf("calls: %+v", calls)
	}
	for _, edge := range calls {
		if strings.Contains(edge.From, "p.*A.Serve") && !strings.Contains(edge.To, "p.*A.load") {
			t.Fatalf("receiver collision: %+v", edge)
		}
	}
}

func TestMuxOriginsAndNamedHandlerLinks(t *testing.T) {
	result := analyzeSources(t, map[string]string{"a.go": `package p
import h "net/http"
type Server struct { mux *h.ServeMux }
type fake struct{}
func target(w h.ResponseWriter,r *h.Request){}
func (s *Server) Routes(m *h.ServeMux, unknown fake) {
 m.HandleFunc("/one",target)
 s.mux.Handle("/two",h.HandlerFunc(target))
 local := h.NewServeMux()
 local.HandleFunc("POST /three",target)
 other := &h.ServeMux{}
 other.HandleFunc("/four",target)
 var declared h.ServeMux
 declared.HandleFunc("/five",target)
 h.DefaultServeMux.HandleFunc("GET\t /six",target)
 local = unknown
 local.HandleFunc("/not-proven",target)
 unknown.HandleFunc("/not-http",target)
 m.HandleFunc("/closure",func(w h.ResponseWriter,r *h.Request){target(w,r)})
}`})
	if len(nodesOf(result, "go_route")) != 7 || len(relationsOf(result, "handles")) != 6 {
		t.Fatalf("routes/handlers: %+v", result)
	}
	if !hasGap(result, "go_unresolved_handler") || !hasGap(result, "go_unresolved_route_receiver") || len(relationsOf(result, "calls")) != 0 {
		t.Fatalf("gaps/calls: %+v", result)
	}
}

func TestSQLTypesFieldsAliasesAndTx(t *testing.T) {
	result := analyzeSources(t, map[string]string{"types.go": `package p
import sq "database/sql"
type DB = sq.DB
type Repo struct { db *DB; tx *sq.Tx }
type NotDB sq.DB
func (r *Repo) load() {
 r.db.Query("SELECT id FROM reports")
 r.tx.Exec("UPDATE reports SET name = ? WHERE id = ?")
}
func run(db *sq.DB, tx *sq.Tx, fake NotDB) {
 repo := &Repo{}
 repo.load()
 tx.QueryRowContext(nil,"SELECT name FROM reports")
 started, err := db.Begin()
 started.ExecContext(nil,"DELETE FROM reports WHERE id = ?")
 opened, err := sq.Open("driver","dsn")
 opened.Exec("INSERT INTO reports (name) VALUES (?)")
 fake.Query("SELECT invented FROM nowhere")
 var sq fakeType
 unknown, err := sq.Open("driver","dsn")
 unknown.Query("SELECT id FROM nowhere")
 _,_ = err,repo
}
type fakeType struct{}
`})
	if len(nodesOf(result, "go_query")) != 5 || len(result.TableRefs) != 5 || len(relationsOf(result, "calls")) != 1 {
		t.Fatalf("SQL type evidence: %+v", result)
	}
}

func TestStringConstantsAndConservativeFlow(t *testing.T) {
	tests := []struct {
		name, body string
		queries    int
		dynamic    bool
	}{
		{"constant alias", `db.Query(q)`, 1, false},
		{"local concatenation", `const suffix="reports"; text:=prefix+suffix; db.Query(text)`, 1, false},
		{"reassigned dynamic", `text:=q; text=unknown; db.Query(text)`, 0, true},
		{"parallel assignment", `text, other:=q, unknown; text,other=other,text; db.Query(text)`, 0, true},
		{"branch changes", `text:=q; if cond {text=unknown}; db.Query(text)`, 0, true},
		{"branch same", `text:=q; if cond {text=q}; db.Query(text)`, 1, false},
		{"branch own shadow", `text:=q; if cond {text:=unknown; _=text}; db.Query(text)`, 1, false},
		{"loop reassign", `text:=q; for cond {db.Query(text);text=unknown}`, 0, true},
		{"global string var", `db.Query(variable)`, 0, true},
		{"escaped address", `text:=q; mutate(&text); db.Query(text)`, 0, true},
		{"aliased address", `text:=q; pointer:=&text; *pointer=unknown; db.Query(text)`, 0, true},
		{"closure writes", `text:=q; f:=func(){text=unknown}; f(); db.Query(text)`, 0, true},
		{"later closure invocation", `text:=q; f:=func(){text=unknown}; text=q; f(); db.Query(text)`, 0, true},
		{"later escaped mutation", `text:=q; p:=&text; text=q; mutate(p); db.Query(text)`, 0, true},
		{"goto values", `text:=q; again: db.Query(text); text=unknown; if cond {goto again}`, 0, true},
		{"switch fallthrough", `text:=q; switch {case cond: text=unknown; fallthrough; default: db.Query(text)}`, 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := analyzeSources(t, map[string]string{
				"query.go": `package p
const prefix = "SELECT id FROM "
const q = prefix + "reports"
var variable = q`,
				"a.go": `package p
import "database/sql"
func mutate(s *string){}
func run(db *sql.DB, unknown string, cond bool) { ` + test.body + ` }`,
			})
			if got := len(nodesOf(result, "go_query")); got != test.queries {
				t.Fatalf("queries=%d want %d: %+v", got, test.queries, result)
			}
			if hasGap(result, "go_dynamic_sql") != test.dynamic {
				t.Fatalf("dynamic SQL gap: %+v", result.Gaps)
			}
		})
	}
}

func TestUnsupportedSQLHasNoTableEdges(t *testing.T) {
	result := analyzeSources(t, map[string]string{"a.go": `package p
import "database/sql"
func run(db *sql.DB, text string) {
 db.Query("SELECT a.id FROM reports a JOIN owners b ON a.owner_id = b.id")
 db.Query("WITH x AS (SELECT id FROM reports) SELECT id FROM x")
 db.Query("SELECT FROM")
 db.Query(text)
}`})
	if len(result.TableRefs) != 0 || !hasGap(result, "go_unsupported_sql") || !hasGap(result, "go_dynamic_sql") {
		t.Fatalf("unsupported SQL facts: %+v", result)
	}
}

func TestResultNeverContainsSQLLiteralValues(t *testing.T) {
	result := analyzeSources(t, map[string]string{"a.go": `package p
import "database/sql"
func run(db *sql.DB) {
 db.Query("SELECT id FROM reports WHERE token = 'literal-secret-123'")
 db.Query("SELECT id FROM reports 'trailing-secret-456'")
 db.Query("SELECT \"function-secret-789\"() FROM reports")
 db.Query("SELECT 'projection-secret-abc' FROM reports")
}`})
	if len(result.TableRefs) != 2 || !hasGap(result, "go_unsupported_sql") {
		t.Fatalf("unexpected supported/unsupported inventory: %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"literal-secret-123", "trailing-secret-456", "function-secret-789", "projection-secret-abc", "SELECT id FROM"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("result leaks SQL contents %q", private)
		}
	}
	for _, query := range nodesOf(result, "go_query") {
		if _, containsSQL := query.Attributes["sql"]; containsSQL {
			t.Fatalf("query includes raw SQL: %+v", query)
		}
	}
}

func TestBuildVariantsAreInventoriedWithoutGuessedLinks(t *testing.T) {
	result := analyzeSources(t, map[string]string{
		"a_linux.go":   "//go:build linux\n\npackage p\nfunc target(){}\n",
		"a_windows.go": "//go:build windows\n\npackage p\nfunc target(){}\n",
		"run.go":       "package p\nfunc run(){target()}\n",
	})
	if len(nodesOf(result, "go_function")) != 3 || len(relationsOf(result, "calls")) != 0 || !hasGap(result, "go_ambiguous_call") || !hasGap(result, "go_build_constraints") {
		t.Fatalf("build variants: %+v", result)
	}
}

func TestMalformedParseFailsWholeInventory(t *testing.T) {
	result, err := Analyze([]File{{Path: "ok.go", Content: []byte("package p\nfunc Good(){}")}, {Path: "bad.go", Content: []byte("package p\nfunc Bad( {")}})
	if err == nil || !strings.Contains(err.Error(), "bad.go:2") || len(result.Nodes) != 0 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestIgnoredFilesAndStableInputOrdering(t *testing.T) {
	files := []File{
		{Path: "b.go", Content: []byte("package p\nfunc B(){A()}")},
		{Path: "a.go", Content: []byte("package p\nfunc A(){}")},
		{Path: "bad_test.go", Content: []byte("malformed")},
		{Path: "generated.go", Content: []byte("// Code generated by example. DO NOT EDIT.\nmalformed")},
		{Path: "notgo.txt", Content: []byte("malformed")},
	}
	first, err := Analyze(files)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
		files[i], files[j] = files[j], files[i]
	}
	second, err := Analyze(files)
	if err != nil || !reflect.DeepEqual(first, second) || len(nodesOf(first, "go_function")) != 2 {
		t.Fatalf("unstable output: %v\n%+v\n%+v", err, first, second)
	}
}

func TestGeneratedMarkerMustBeRealHeaderComment(t *testing.T) {
	result := analyzeSources(t, map[string]string{
		"block.go": "/*\n// Code generated by someone. DO NOT EDIT.\n*/\npackage p\nfunc Block(){}",
		"body.go":  "package p\n// Code generated by someone. DO NOT EDIT.\nfunc Body(){}",
	})
	if len(nodesOf(result, "go_function")) != 2 {
		t.Fatalf("non-generated sources were skipped: %+v", result)
	}
}

func TestFunctionIdentitySurvivesLineChanges(t *testing.T) {
	first := analyzeSources(t, map[string]string{"a.go": "package p\nfunc Work(){}"})
	second := analyzeSources(t, map[string]string{"a.go": "package p\n\n// Work is documented.\nfunc Work(){}"})
	if nodesOf(first, "go_function")[0].Key != nodesOf(second, "go_function")[0].Key {
		t.Fatal("function identity changed with its line number")
	}
}

func TestPrecisePhysicalLinesIgnoreLineDirective(t *testing.T) {
	result := analyzeSources(t, map[string]string{"a.go": `package p
import "net/http"
//line forged.go:999
func Route() {
 http.HandleFunc("/health", Health)
}
func Health(w http.ResponseWriter, r *http.Request) {}
`})
	for _, route := range nodesOf(result, "go_route") {
		if route.Path != "a.go" || route.Line != 5 {
			t.Fatalf("source location followed //line: %+v", route)
		}
	}
}

func TestSyntheticFixtureEndToEnd(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "go-http")
	paths, err := filepath.Glob(filepath.Join(root, "services", "reports", "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("fixture paths: %v %v", paths, err)
	}
	var files []File
	for _, name := range paths {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, File{Path: filepath.ToSlash(relative), Content: content})
	}
	result, err := Analyze(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodesOf(result, "go_route")) != 2 || len(result.TableRefs) != 1 || result.TableRefs[0].Table != "reports" || !hasGap(result, "go_dynamic_sql") {
		t.Fatalf("fixture inventory: %+v", result)
	}
	var listHandler, helper string
	for _, node := range nodesOf(result, "go_function") {
		switch node.Attributes["name"] {
		case "ListReports":
			listHandler = node.Key
		case "loadReports":
			helper = node.Key
		}
	}
	linkedHandler, linkedHelper, linkedQuery := false, false, false
	for _, relation := range result.Relations {
		linkedHandler = linkedHandler || relation.Kind == "handles" && relation.To == listHandler
		linkedHelper = linkedHelper || relation.Kind == "calls" && relation.From == listHandler && relation.To == helper
		linkedQuery = linkedQuery || relation.Kind == "queries" && relation.From == helper && relation.To == result.TableRefs[0].QueryKey
	}
	if !linkedHandler || !linkedHelper || !linkedQuery {
		t.Fatalf("fixture chain missing: %+v", result.Relations)
	}
}
