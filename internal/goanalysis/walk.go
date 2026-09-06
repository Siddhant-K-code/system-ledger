package goanalysis

import (
	"go/ast"
	"go/token"
	"strings"
)

func (a *analyzer) analyzeFunction(fn *function) {
	a.controlFlow = false
	if fn.decl.Body != nil {
		ast.Inspect(fn.decl.Body, func(node ast.Node) bool {
			if _, closure := node.(*ast.FuncLit); closure {
				return false
			}
			if branch, ok := node.(*ast.BranchStmt); ok && (branch.Tok == token.GOTO || branch.Tok == token.FALLTHROUGH) {
				a.controlFlow = true
			}
			return true
		})
	}
	s := newScope(fn.typeScope)
	a.bindFields(fn.decl.Recv, s, fn.typeScope)
	a.bindFields(fn.decl.Type.Params, s, fn.typeScope)
	a.bindFields(fn.decl.Type.Results, s, fn.typeScope)
	if fn.typeScope != nil {
		a.gap("go_generic_type", "Generic type parameters are unknown; specialized receiver and result types are not inferred.", fn.decl)
	}
	if len(fn.source.pkg.functions[functionSymbol(fn.decl)]) > 1 {
		a.gap("go_ambiguous_declaration", "Multiple source declarations share this package/receiver/name; calls to it are not linked.", fn.decl)
	}
	if fn.decl.Body != nil {
		a.block(fn.decl.Body, s, false)
	}
}

func (a *analyzer) block(block *ast.BlockStmt, s *scope, child bool) {
	if child {
		s = newScope(s)
	}
	for _, statement := range block.List {
		a.statement(statement, s)
	}
}

func cloneScope(s *scope) *scope {
	if s == nil {
		return nil
	}
	copy := newScope(cloneScope(s.parent))
	for k, v := range s.names {
		copy.names[k] = v
	}
	return copy
}

func sameValue(x, y value) bool {
	return x.typ == y.typ && x.staticType == y.staticType && x.declared == y.declared && x.callable == y.callable &&
		x.fn == y.fn && x.pkg == y.pkg && x.unknown == y.unknown && x.escaped == y.escaped && x.limited == y.limited &&
		((x.text == nil && y.text == nil) || (x.text != nil && y.text != nil && *x.text == *y.text))
}

func (a *analyzer) mergeScopes(target *scope, node ast.Node, branches ...*scope) {
	for current := target; current != nil; current = current.parent {
		for name, original := range current.names {
			var merged value
			for i, branch := range branches {
				v := branch.names[name]
				if i == 0 {
					merged = v
				} else if !sameValue(merged, v) {
					merged = value{escaped: merged.escaped || v.escaped, limited: merged.limited || v.limited}
				} else if merged.text != nil {
					origins, ok := a.mergeOrigins(merged.origins, v.origins, node)
					if !ok {
						merged = value{limited: true}
					} else {
						merged.origins = origins
					}
				}
			}
			if len(branches) == 0 {
				merged = original
			}
			merged.staticType, merged.declared, merged.callable = original.staticType, original.declared, original.callable
			current.names[name] = merged
		}
		for i, branch := range branches {
			branches[i] = branch.parent
		}
	}
}

func (a *analyzer) statement(statement ast.Stmt, s *scope) {
	switch st := statement.(type) {
	case *ast.BlockStmt:
		a.block(st, s, true)
	case *ast.ExprStmt:
		a.expression(st.X, s)
	case *ast.AssignStmt:
		for _, expr := range st.Rhs {
			a.expression(expr, s)
		}
		values := make([]value, len(st.Lhs))
		if len(st.Rhs) == len(st.Lhs) {
			for i, rhs := range st.Rhs {
				values[i] = a.eval(rhs, s, a.src)
			}
		} else if len(st.Rhs) == 1 {
			if call, ok := st.Rhs[0].(*ast.CallExpr); ok {
				results := a.callResults(call, s, a.src)
				if len(results) == len(values) {
					copy(values, results)
				}
			}
		}
		for i, lhs := range st.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
				v := values[i]
				if a.controlFlow {
					v = value{typ: v.typ}
				}
				if st.Tok != token.ASSIGN && st.Tok != token.DEFINE {
					v = value{}
				}
				if st.Tok == token.DEFINE {
					s.define(id.Name, v)
				} else {
					s.assign(id.Name, v)
				}
			} else {
				a.expression(lhs, s)
			}
		}
	case *ast.DeclStmt:
		if declaration, ok := st.Decl.(*ast.GenDecl); ok {
			var previous []ast.Expr
			for _, spec := range declaration.Specs {
				if t, ok := spec.(*ast.TypeSpec); ok {
					s.names[t.Name.Name] = value{}
					continue
				}
				spec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				expressions := spec.Values
				if declaration.Tok == token.CONST && len(expressions) == 0 {
					expressions = previous
				} else {
					previous = expressions
				}
				for _, expr := range expressions {
					a.expression(expr, s)
				}
				values := make([]value, len(spec.Names))
				if len(expressions) == len(values) {
					for i, expr := range expressions {
						values[i] = a.eval(expr, s, a.src)
					}
				} else if len(expressions) == 1 {
					if call, ok := expressions[0].(*ast.CallExpr); ok {
						result := a.callResults(call, s, a.src)
						if len(result) == len(values) {
							copy(values, result)
						}
					}
				}
				for i, name := range spec.Names {
					if spec.Type != nil {
						values[i] = a.declaredValue(values[i], spec.Type, s, a.src)
					}
					if a.controlFlow && declaration.Tok != token.CONST {
						values[i] = value{typ: values[i].typ}
					}
					s.define(name.Name, values[i])
				}
			}
		}
	case *ast.ReturnStmt:
		for _, expr := range st.Results {
			a.expression(expr, s)
		}
	case *ast.IfStmt:
		outer := newScope(s)
		if st.Init != nil {
			a.statement(st.Init, outer)
		}
		a.expression(st.Cond, outer)
		yes, no := cloneScope(outer), cloneScope(outer)
		a.block(st.Body, yes, true)
		if st.Else != nil {
			a.statement(st.Else, no)
		}
		a.mergeScopes(outer, st, yes, no)
	case *ast.ForStmt:
		loop := newScope(s)
		if st.Init != nil {
			a.statement(st.Init, loop)
		}
		invalidateWrites(st.Body, loop)
		if st.Post != nil {
			invalidateWrites(st.Post, loop)
		}
		a.expression(st.Cond, loop)
		a.block(st.Body, loop, true)
		if st.Post != nil {
			a.statement(st.Post, loop)
		}
		invalidateWrites(st.Body, loop)
		if st.Post != nil {
			invalidateWrites(st.Post, loop)
		}
	case *ast.RangeStmt:
		a.expression(st.X, s)
		loop := newScope(s)
		for _, expr := range []ast.Expr{st.Key, st.Value} {
			if id, ok := expr.(*ast.Ident); ok {
				if st.Tok == token.DEFINE {
					loop.define(id.Name, value{})
				} else {
					loop.assign(id.Name, value{})
				}
			}
		}
		invalidateWrites(st.Body, loop)
		a.block(st.Body, loop, true)
		invalidateWrites(st.Body, loop)
	case *ast.SwitchStmt:
		outer := newScope(s)
		if st.Init != nil {
			a.statement(st.Init, outer)
		}
		a.expression(st.Tag, outer)
		a.cases(st.Body, outer)
	case *ast.TypeSwitchStmt:
		outer := newScope(s)
		if st.Init != nil {
			a.statement(st.Init, outer)
		}
		a.statement(st.Assign, outer)
		a.cases(st.Body, outer)
	case *ast.SelectStmt:
		a.cases(st.Body, s)
	case *ast.GoStmt:
		a.expression(st.Call, s)
	case *ast.DeferStmt:
		a.expression(st.Call, s)
	case *ast.SendStmt:
		a.expression(st.Chan, s)
		a.expression(st.Value, s)
	case *ast.IncDecStmt:
		a.expression(st.X, s)
		if id, ok := st.X.(*ast.Ident); ok {
			s.assign(id.Name, value{})
		}
	case *ast.LabeledStmt:
		a.statement(st.Stmt, s)
	case *ast.BranchStmt:
		if st.Tok == token.GOTO || st.Tok == token.FALLTHROUGH {
			for current := s; current != nil; current = current.parent {
				for name, previous := range current.names {
					current.names[name] = assignedValue(previous, value{})
				}
			}
			a.gap("go_control_flow", "goto/fallthrough control flow is not resolved; local value evidence is discarded.", st)
		}
	}
}

func (a *analyzer) cases(body *ast.BlockStmt, s *scope) {
	var branches []*scope
	hasDefault := false
	for _, statement := range body.List {
		branch := cloneScope(s)
		local := newScope(branch)
		var statements []ast.Stmt
		switch clause := statement.(type) {
		case *ast.CaseClause:
			if len(clause.List) == 0 {
				hasDefault = true
			}
			for _, expr := range clause.List {
				a.expression(expr, local)
			}
			statements = clause.Body
		case *ast.CommClause:
			if clause.Comm == nil {
				hasDefault = true
			} else {
				a.statement(clause.Comm, local)
			}
			statements = clause.Body
		}
		for _, st := range statements {
			a.statement(st, local)
		}
		branches = append(branches, branch)
	}
	if !hasDefault {
		branches = append(branches, cloneScope(s))
	}
	a.mergeScopes(s, body, branches...)
}

// In loops and escaped closures a later iteration/invocation can change any
// captured value. Over-invalidation is preferable to claiming a false edge.
func invalidateWrites(node ast.Node, s *scope) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range st.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					s.assign(id.Name, value{})
				}
			}
		case *ast.IncDecStmt:
			if id, ok := st.X.(*ast.Ident); ok {
				s.assign(id.Name, value{})
			}
		case *ast.RangeStmt:
			if st.Tok == token.ASSIGN {
				for _, lhs := range []ast.Expr{st.Key, st.Value} {
					if id, ok := lhs.(*ast.Ident); ok {
						s.assign(id.Name, value{})
					}
				}
			}
		case *ast.UnaryExpr:
			if st.Op == token.AND {
				if id, ok := st.X.(*ast.Ident); ok {
					s.assign(id.Name, value{})
				}
			}
		}
		return true
	})
}

func escapeWrites(node ast.Node, s *scope) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range st.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && st.Tok != token.DEFINE {
					s.escape(id.Name)
				}
			}
		case *ast.IncDecStmt:
			if id, ok := st.X.(*ast.Ident); ok {
				s.escape(id.Name)
			}
		case *ast.RangeStmt:
			if st.Tok == token.ASSIGN {
				for _, lhs := range []ast.Expr{st.Key, st.Value} {
					if id, ok := lhs.(*ast.Ident); ok {
						s.escape(id.Name)
					}
				}
			}
		case *ast.UnaryExpr:
			if st.Op == token.AND {
				if id, ok := st.X.(*ast.Ident); ok {
					s.escape(id.Name)
				}
			}
		}
		return true
	})
}

func (a *analyzer) expression(expr ast.Expr, s *scope) {
	if expr == nil {
		return
	}
	ast.Inspect(expr, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				if id, ok := n.X.(*ast.Ident); ok {
					s.escape(id.Name)
				}
			}
		case *ast.FuncLit:
			escapeWrites(n.Body, s)
			a.gap("go_closure", "Anonymous function bodies are not linked to their enclosing named function.", n)
			return false
		case *ast.CallExpr:
			a.expression(n.Fun, s)
			for _, arg := range n.Args {
				a.expression(arg, s)
			}
			a.call(n, s)
			for _, arg := range n.Args {
				if address, ok := arg.(*ast.UnaryExpr); ok && address.Op == token.AND {
					if id, ok := address.X.(*ast.Ident); ok {
						s.escape(id.Name)
					}
				}
			}
			return false
		}
		return true
	})
}

func (a *analyzer) call(call *ast.CallExpr, s *scope) {
	v := a.eval(call.Fun, s, a.src)
	if v.fn != nil {
		a.relation(a.fn.key, v.fn.key, "calls", call)
	} else if v.unknown {
		a.gap("go_ambiguous_call", "Call target has multiple source declarations and cannot be resolved.", call)
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		if id, ok := call.Fun.(*ast.Ident); ok && v.fn == nil {
			if _, local := s.lookup(id.Name); local || len(a.src.pkg.globals[id.Name]) != 0 {
				a.gap("go_unresolved_call", "Local callable cannot be resolved to an exact named function.", call)
			}
		} else if _, simple := call.Fun.(*ast.Ident); !simple {
			a.gap("go_unresolved_call", "Callable expression is outside supported direct named calls.", call)
		}
		return
	}
	receiver := a.eval(selector.X, s, a.src)
	method := selector.Sel.Name
	if method == "HandleFunc" || method == "Handle" {
		if receiver.pkg == "net/http" || receiver.typ == "*http.ServeMux" || receiver.typ == "http.ServeMux" {
			a.route(call, method, s)
		} else if receiver.pkg == "" {
			a.gap("go_unresolved_route_receiver", "Handle/HandleFunc receiver is not proven to be net/http or ServeMux; no route inferred.", call)
		}
		return
	}
	if _, recognized := sqlMethods[method]; recognized {
		if receiver.typ == "*sql.DB" || receiver.typ == "*sql.Tx" {
			a.query(call, method, s)
		} else if receiver.pkg == "" {
			a.gap("go_unresolved_sql_receiver", "SQL-like receiver is not proven to be *database/sql.DB or *database/sql.Tx; no query inferred.", call)
		}
		return
	}
	if v.fn == nil && receiver.pkg == "" && receiver.typ != "*sql.DB" && receiver.typ != "*sql.Tx" && receiver.typ != "*http.ServeMux" {
		a.gap("go_unresolved_method", "Receiver method cannot be resolved to an exact same-package function.", call)
	}
}

func (a *analyzer) route(call *ast.CallExpr, method string, s *scope) {
	if len(call.Args) != 2 {
		a.gap("go_route_arguments", "net/http registration requires exactly two arguments.", call)
		return
	}
	resolved := a.eval(call.Args[0], s, a.src)
	pattern := resolved.text
	if pattern == nil {
		a.gap("go_dynamic_route", "Route pattern is not a statically known string.", call.Args[0])
		return
	}
	httpMethod, routePath := "", *pattern
	if index := strings.IndexAny(*pattern, " \t"); index >= 0 {
		httpMethod, routePath = (*pattern)[:index], strings.TrimLeft((*pattern)[index+1:], " \t")
	}
	if !strings.Contains(routePath, "/") || !validHTTPMethod(httpMethod) || strings.ContainsAny(routePath, " \t\r\n") {
		a.gap("go_route_pattern", "Route pattern cannot be conservatively interpreted.", call.Args[0])
		return
	}
	key := a.factKey("go_route", call)
	a.result.Nodes = append(a.result.Nodes, Node{Key: key, Kind: "go_route", Name: *pattern, Locator: a.loc(call), Evidence: append([]Locator(nil), resolved.origins...), Attributes: map[string]string{
		"pattern": *pattern, "method": httpMethod, "path": routePath, "registration": method,
	}})
	a.relation(a.fn.key, key, "registers", call)
	handler := a.eval(call.Args[1], s, a.src)
	if method == "Handle" {
		handler.fn = a.httpHandler(handler, a.src)
	}
	if handler.fn != nil {
		a.relation(key, handler.fn.key, "handles", call.Args[1])
	} else {
		a.gap("go_unresolved_handler", "Route handler is not an exact named function or method; no handler link inferred.", call.Args[1])
	}
}

func (a *analyzer) httpHandler(handler value, src *source) *function {
	if handler.typ == "http.HandlerFunc" {
		return handler.fn
	}
	base := strings.TrimPrefix(handler.typ, "*")
	if !strings.HasPrefix(base, "$") {
		return nil
	}
	name := strings.TrimPrefix(base, "$")
	if _, field := a.field(name, "ServeHTTP", src, map[string]bool{}); field {
		return nil
	}
	methods := src.pkg.functions[name+".ServeHTTP"]
	if len(methods) != 1 {
		return nil
	}
	method := methods[0]
	if strings.HasPrefix(method.receiver, "*") && !strings.HasPrefix(handler.typ, "*") {
		return nil
	}
	if method.decl.Type.Results != nil && len(method.decl.Type.Results.List) != 0 {
		return nil
	}
	var types []string
	if method.decl.Type.Params != nil {
		for _, field := range method.decl.Type.Params.List {
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for i := 0; i < count; i++ {
				types = append(types, a.typeOf(field.Type, method.typeScope, method.source))
			}
		}
	}
	if len(types) != 2 || types[0] != "http.ResponseWriter" || types[1] != "*http.Request" {
		return nil
	}
	return method
}

func validHTTPMethod(method string) bool {
	for _, r := range method {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	return true
}

var sqlMethods = map[string]int{
	"Query": 0, "QueryContext": 1, "QueryRow": 0, "QueryRowContext": 1,
	"Exec": 0, "ExecContext": 1,
}

func (a *analyzer) query(call *ast.CallExpr, method string, s *scope) {
	index := sqlMethods[method]
	if len(call.Args) <= index {
		a.gap("go_sql_arguments", "database/sql call is missing its query argument.", call)
		return
	}
	resolved := a.eval(call.Args[index], s, a.src)
	text := resolved.text
	if text == nil {
		a.gap("go_dynamic_sql", "SQL text is not a literal or a conservatively resolved constant string.", call.Args[index])
		return
	}
	key := a.factKey("go_query", call)
	ref, err := parseSQL(*text)
	attributes := map[string]string{"method": method}
	if err == nil {
		attributes["operation"] = ref.operation
		attributes["schema"] = ref.schema
		attributes["table"] = ref.table
		attributes["access"] = ref.access
	}
	a.result.Nodes = append(a.result.Nodes, Node{Key: key, Kind: "go_query", Name: method, Locator: a.loc(call), Evidence: append([]Locator(nil), resolved.origins...), Attributes: attributes})
	a.relation(a.fn.key, key, "queries", call)
	if err != nil {
		a.gap("go_unsupported_sql", "SQL table references omitted: "+err.Error()+".", call.Args[index])
		return
	}
	a.result.TableRefs = append(a.result.TableRefs, TableRef{QueryKey: key, Schema: ref.schema, Table: ref.table, Access: ref.access, Locator: a.loc(call.Args[index])})
}
