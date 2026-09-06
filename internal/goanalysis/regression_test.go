package goanalysis

import (
	"fmt"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

func TestInterfaceAssignmentsNeverBecomeConcreteReceivers(t *testing.T) {
	tests := []string{
		`var q Querier; q=db; q.Query("SELECT id FROM reports")`,
		`var q Querier; q=db; change:=func(){q=fake{}}; change(); q.Query("SELECT id FROM reports")`,
		`var q Querier; q=db; pointer:=&q; change(pointer); q.Query("SELECT id FROM reports")`,
		`var q Querier; if true {q=db}; q=db; q.Query("SELECT id FROM reports")`,
		`q:=Querier(db); q=db; mutate:=func(){q=fake{}}; mutate(); q.Query("SELECT id FROM reports")`,
		`q:=makeQuerier(); q=db; q.Query("SELECT id FROM reports")`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			result := analyzeSources(t, map[string]string{"a.go": `package p
import "database/sql"
type Querier interface{ Query(string,...any)(*sql.Rows,error) }
type fake struct{}
func(fake) Query(string,...any)(*sql.Rows,error){return nil,nil}
func makeQuerier() Querier {return fake{}}
func change(q *Querier){*q=fake{}}
func run(db *sql.DB){` + body + `}`})
			if len(nodesOf(result, "go_query")) != 0 || len(result.TableRefs) != 0 || !hasGap(result, "go_unresolved_sql_receiver") {
				t.Fatalf("interface was devirtualized: %+v", result)
			}
		})
	}
	result := analyzeSources(t, map[string]string{"a.go": `package p
import "net/http"
type Muxer interface{ HandleFunc(string,func(http.ResponseWriter,*http.Request)) }
type fake struct{}
func(fake) HandleFunc(string,func(http.ResponseWriter,*http.Request)){}
func target(http.ResponseWriter,*http.Request){}
func run(m *http.ServeMux) {
 var q Muxer
 q=m
 change:=func(){q=fake{}}
 change()
 q.HandleFunc("/reports",target)
}`})
	if len(nodesOf(result, "go_route")) != 0 || !hasGap(result, "go_unresolved_route_receiver") {
		t.Fatalf("interface mux was devirtualized: %+v", result)
	}
}

func TestGenericParametersShadowPackageTypes(t *testing.T) {
	result := analyzeSources(t, map[string]string{"a.go": `package p
import ("database/sql"; "net/http")
type DB = *sql.DB
type Mux = *http.ServeMux
type Worker struct{}
func (Worker) Run(){}
func target(http.ResponseWriter,*http.Request){}
func inspect[DB interface{Query(string)}](db DB) {
 db.Query("SELECT id FROM reports")
 var local DB
 local.Query("SELECT id FROM reports")
}
func routes[Mux interface{HandleFunc(string,func(http.ResponseWriter,*http.Request))}](mux Mux) {
 mux.HandleFunc("/reports",target)
}
func generic[Worker interface{Run()}](w Worker){w.Run()}
type Repo[T interface{Query(string)}, U interface{HandleFunc(string,func(http.ResponseWriter,*http.Request))}] struct{}
func (r Repo[DB,Mux]) inspect(db DB, mux Mux) {
 db.Query("SELECT id FROM reports")
 mux.HandleFunc("/reports",target)
}
func (r *(Repo[DB,Mux])) parenthesized(db DB, mux Mux) {
 db.Query("SELECT id FROM reports")
 mux.HandleFunc("/reports",target)
}
func choose[DB interface{Query(string)}](db DB) DB {return db}
type fake struct{}
func(fake) Query(string){}
func inferred(){choose(fake{}).Query("SELECT id FROM reports")}
`})
	if len(nodesOf(result, "go_query")) != 0 || len(nodesOf(result, "go_route")) != 0 || len(result.TableRefs) != 0 || !hasGap(result, "go_generic_type") {
		t.Fatalf("generic aliases produced concrete facts: %+v", result)
	}
	for _, edge := range relationsOf(result, "calls") {
		if strings.Contains(edge.To, "Worker.Run") || strings.Contains(edge.To, "fake.Query") {
			t.Fatalf("generic dispatch linked a package type: %+v", edge)
		}
	}
}

func TestHandleUsesServeHTTPNotUnderlyingFunction(t *testing.T) {
	result := analyzeSources(t, map[string]string{"a.go": `package p
import h "net/http"
type handler func(h.ResponseWriter,*h.Request)
func target(h.ResponseWriter,*h.Request){}
func(handler) ServeHTTP(h.ResponseWriter,*h.Request){}
type pointerHandler struct{}
func(*pointerHandler) ServeHTTP(h.ResponseWriter,*h.Request){}
func register() {
 var custom handler=target
 h.Handle("/custom",custom)
 h.HandleFunc("/function",custom)
 h.Handle("/adapter",h.HandlerFunc(target))
 var adapter h.HandlerFunc=target
 h.Handle("/declared-adapter",adapter)
 var opaque h.Handler=adapter
 h.Handle("/interface",opaque)
 h.Handle("/pointer",&pointerHandler{})
 h.Handle("/value",pointerHandler{})
}`})
	expected := map[string]string{
		"/custom": "p.handler.ServeHTTP", "/function": "p.target", "/adapter": "p.target",
		"/declared-adapter": "p.target", "/pointer": "p.*pointerHandler.ServeHTTP",
	}
	for _, route := range nodesOf(result, "go_route") {
		var targets []string
		for _, relation := range relationsOf(result, "handles") {
			if relation.From == route.Key {
				targets = append(targets, relation.To)
			}
		}
		want, linked := expected[route.Name]
		if !linked && len(targets) != 0 || linked && (len(targets) != 1 || !strings.HasSuffix(targets[0], want)) {
			t.Fatalf("%s links %v, want %q", route.Name, targets, want)
		}
	}
	if len(nodesOf(result, "go_route")) != 7 || len(relationsOf(result, "handles")) != len(expected) {
		t.Fatalf("unexpected route inventory: %+v", result)
	}
}

func TestAssignmentRangesInvalidateCapturedValues(t *testing.T) {
	tests := []string{
		`fn:=original; change:=func(){for _,fn=range []func(){replacement}{}}; change(); fn()`,
		`fn:=original; for cond {fn(); for _,fn=range []func(){replacement}{}}`,
		`query:="SELECT id FROM reports"; change:=func(){for query=range map[string]bool{"dynamic":true}{}}; change(); db.Query(query)`,
		`query:="SELECT id FROM reports"; for cond {db.Query(query); for query=range map[string]bool{"dynamic":true}{}}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			result := analyzeSources(t, map[string]string{"a.go": `package p
import "database/sql"
func original(){}
func replacement(){}
func run(db *sql.DB,cond bool){` + body + `}`})
			if len(relationsOf(result, "calls")) != 0 || len(nodesOf(result, "go_query")) != 0 || len(result.TableRefs) != 0 {
				t.Fatalf("range retained stale captured value: %+v", result)
			}
		})
	}
}

func TestSQLExecutableCommentsNeverProduceTableReferences(t *testing.T) {
	for _, comment := range []string{"/*!50000 actual_reports AS */", "/*M!100100 actual_reports AS */", "/*m! actual_reports AS */"} {
		t.Run(comment, func(t *testing.T) {
			sql := "SELECT id FROM " + comment + " reports"
			if ref, err := parseSQL(sql); err == nil || ref != (sqlReference{}) {
				t.Fatalf("accepted executable comment: %+v %v", ref, err)
			}
			result := analyzeSources(t, map[string]string{"a.go": `package p
import "database/sql"
func run(db *sql.DB){db.Query(` + fmt.Sprintf("%q", sql) + `)}`})
			if len(result.TableRefs) != 0 || !hasGap(result, "go_unsupported_sql") {
				t.Fatalf("executable comment produced a reference: %+v", result)
			}
		})
	}
}

func doublingSource(base string, levels int) string {
	var source strings.Builder
	source.WriteString("package p\nimport \"database/sql\"\n")
	fmt.Fprintf(&source, "const A0=%q\n", base)
	for i := 1; i <= levels; i++ {
		fmt.Fprintf(&source, "const A%d=A%d+A%d\n", i, i-1, i-1)
	}
	fmt.Fprintf(&source, `func run(db *sql.DB){db.Query("SELECT id FROM reports"+A%d)}`, levels)
	return source.String()
}

func TestConstantExpansionBudgetsAndMemoization(t *testing.T) {
	t.Run("nonempty doubling", func(t *testing.T) {
		start := time.Now()
		result := analyzeSources(t, map[string]string{"a.go": doublingSource("x", 30)})
		if time.Since(start) > 2*time.Second {
			t.Fatal("constant expansion exceeded the regression time budget")
		}
		if !hasGap(result, "go_evaluation_limit") || len(nodesOf(result, "go_query")) != 0 || len(result.TableRefs) != 0 {
			t.Fatalf("large expansion was not rejected: %+v", result)
		}
	})
	t.Run("empty doubling is memoized", func(t *testing.T) {
		start := time.Now()
		result := analyzeSources(t, map[string]string{"a.go": doublingSource("", 30)})
		if time.Since(start) > 2*time.Second {
			t.Fatal("empty constant expansion exceeded the regression time budget")
		}
		if hasGap(result, "go_evaluation_limit") || len(result.TableRefs) != 1 || result.TableRefs[0].Table != "reports" {
			t.Fatalf("immutable constants were not memoized: %+v", result)
		}
	})
	t.Run("deep aliases", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("package p\nimport \"database/sql\"\nconst A0=\"SELECT id FROM reports\"\n")
		for i := 1; i <= 1000; i++ {
			fmt.Fprintf(&source, "const A%d=A%d\n", i, i-1)
		}
		source.WriteString("func run(db *sql.DB){db.Query(A1000)}")
		start := time.Now()
		result := analyzeSources(t, map[string]string{"a.go": source.String()})
		if time.Since(start) > 2*time.Second {
			t.Fatal("alias evaluation exceeded the regression time budget")
		}
		if !hasGap(result, "go_evaluation_limit") || len(nodesOf(result, "go_query")) != 0 || len(result.TableRefs) != 0 {
			t.Fatalf("unbounded alias depth: %+v", result)
		}
	})
	t.Run("large literal", func(t *testing.T) {
		source := "package p\nimport \"database/sql\"\nfunc run(db *sql.DB){db.Query(" + fmt.Sprintf("%q", strings.Repeat("x", maxEvaluationString+1)) + ")}"
		result := analyzeSources(t, map[string]string{"a.go": source})
		if !hasGap(result, "go_evaluation_limit") || len(nodesOf(result, "go_query")) != 0 {
			t.Fatalf("unbounded literal: %+v", result)
		}
	})
}

func TestEvaluationBudgetsAreCumulative(t *testing.T) {
	for _, exhausted := range []string{"work", "bytes"} {
		t.Run(exhausted, func(t *testing.T) {
			a := &analyzer{fset: token.NewFileSet(), stack: map[string]bool{}, constants: map[string]value{}, evalGaps: map[token.Pos]bool{}}
			expr, err := parser.ParseExprFrom(a.fset, "a.go", `"abc"+"def"`, 0)
			if err != nil {
				t.Fatal(err)
			}
			if exhausted == "work" {
				a.evalWork = maxEvaluationWork - 1
			} else {
				a.evalBytes = maxEvaluationBytes - 1
			}
			v := a.eval(expr, nil, nil)
			if !v.limited || v.text != nil || !hasGap(a.result, "go_evaluation_limit") || a.evalWork > maxEvaluationWork || a.evalBytes > maxEvaluationBytes {
				t.Fatalf("budget was not enforced: value=%+v work=%d bytes=%d gaps=%+v", v, a.evalWork, a.evalBytes, a.result.Gaps)
			}
		})
	}
}
