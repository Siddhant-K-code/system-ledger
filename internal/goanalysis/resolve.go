package goanalysis

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

func (a *analyzer) lookup(name string, s *scope, src *source) value {
	if v, ok := s.lookup(name); ok {
		return v
	}
	if imported, ok := src.imports[name]; ok {
		return value{pkg: imported}
	}
	if globals := src.pkg.globals[name]; len(globals) > 0 {
		if len(globals) != 1 {
			return value{unknown: true}
		}
		g := globals[0]
		key := src.pkg.key + ":global:" + name
		if g.isConst {
			if cached, exists := a.constants[key]; exists {
				return cached
			}
		}
		if a.stack[key] {
			return value{unknown: true}
		}
		a.stack[key] = true
		defer delete(a.stack, key)
		if g.isConst {
			v := a.eval(g.expr, nil, g.source)
			a.constants[key] = v
			return v
		}
		if g.typ != nil {
			return a.declaredValue(value{}, g.typ, nil, g.source)
		}
		// Global variables may be reassigned by any function; only type evidence
		// from an initializer survives, never its string or function value.
		v := a.eval(g.expr, nil, g.source)
		return value{typ: v.typ}
	}
	if functions := src.pkg.functions[name]; len(functions) > 0 {
		if len(functions) == 1 && name != "init" {
			return value{typ: "func", fn: functions[0], callable: true}
		}
		return value{unknown: true}
	}
	return value{}
}

func (a *analyzer) typeOf(expr ast.Expr, s *scope, src *source) string {
	if expr == nil {
		return ""
	}
	if !a.enterEvaluation(expr) {
		return ""
	}
	defer a.leaveEvaluation()
	switch t := expr.(type) {
	case *ast.ParenExpr:
		return a.typeOf(t.X, s, src)
	case *ast.StarExpr:
		if typ := a.typeOf(t.X, s, src); typ != "" {
			return "*" + typ
		}
	case *ast.SelectorExpr:
		if v := a.eval(t.X, s, src); v.pkg == "net/http" {
			switch t.Sel.Name {
			case "ServeMux", "HandlerFunc", "ResponseWriter", "Request":
				return "http." + t.Sel.Name
			}
		} else if v.pkg == "database/sql" && (t.Sel.Name == "DB" || t.Sel.Name == "Tx") {
			return "sql." + t.Sel.Name
		}
	case *ast.FuncType:
		return "func"
	case *ast.Ident:
		if _, shadowed := s.lookup(t.Name); shadowed {
			return ""
		}
		declarations := src.pkg.types[t.Name]
		if len(declarations) != 1 {
			return ""
		}
		declaration := declarations[0]
		if declaration.spec.TypeParams != nil && len(declaration.spec.TypeParams.List) != 0 {
			return ""
		}
		if !declaration.spec.Assign.IsValid() {
			return "$" + t.Name
		}
		key := src.pkg.key + ":type:" + t.Name
		if a.stack[key] {
			return ""
		}
		a.stack[key] = true
		defer delete(a.stack, key)
		return a.typeOf(declaration.spec.Type, nil, declaration.source)
	}
	return ""
}

func (a *analyzer) eval(expr ast.Expr, s *scope, src *source) value {
	if expr == nil {
		return value{}
	}
	if !a.enterEvaluation(expr) {
		return value{limited: true}
	}
	defer a.leaveEvaluation()
	switch e := expr.(type) {
	case *ast.Ident:
		return a.lookup(e.Name, s, src)
	case *ast.ParenExpr:
		return a.eval(e.X, s, src)
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			if !a.reserveString(len(e.Value)-2, e) {
				return value{limited: true}
			}
			text, err := strconv.Unquote(e.Value)
			if err == nil {
				return value{text: &text, origins: []Locator{a.loc(e)}}
			}
		}
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			left := a.eval(e.X, s, src)
			if left.limited {
				return value{limited: true}
			}
			right := a.eval(e.Y, s, src)
			if right.limited {
				return value{limited: true}
			}
			if left.text != nil && right.text != nil {
				origins, ok := a.mergeOrigins(left.origins, right.origins, e)
				if !ok {
					return value{limited: true}
				}
				if !a.reserveString(len(*left.text)+len(*right.text), e) {
					return value{limited: true}
				}
				text := *left.text + *right.text
				return value{text: &text, origins: origins}
			}
		}
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			if v := a.eval(e.X, s, src); v.typ != "" {
				return value{typ: "*" + v.typ}
			}
		}
	case *ast.StarExpr:
		v := a.eval(e.X, s, src)
		if strings.HasPrefix(v.typ, "*") {
			return value{typ: strings.TrimPrefix(v.typ, "*")}
		}
	case *ast.CompositeLit:
		return value{typ: a.typeOf(e.Type, s, src)}
	case *ast.TypeAssertExpr:
		return value{typ: a.typeOf(e.Type, s, src)}
	case *ast.SelectorExpr:
		receiver := a.eval(e.X, s, src)
		if receiver.pkg == "net/http" && e.Sel.Name == "DefaultServeMux" {
			return value{typ: "*http.ServeMux"}
		}
		base := strings.TrimPrefix(receiver.typ, "*")
		if strings.HasPrefix(base, "$") {
			name := strings.TrimPrefix(base, "$")
			if field, ok := a.field(name, e.Sel.Name, src, map[string]bool{}); ok {
				return value{typ: field}
			}
			methods := src.pkg.functions[name+"."+e.Sel.Name]
			if len(methods) == 1 {
				return value{fn: methods[0]}
			}
			if len(methods) > 1 {
				return value{unknown: true}
			}
		}
	case *ast.CallExpr:
		if values := a.callResults(e, s, src); len(values) == 1 {
			return values[0]
		}
	}
	return value{}
}

func (a *analyzer) field(typeName, name string, src *source, seen map[string]bool) (string, bool) {
	if seen[typeName] {
		return "", false
	}
	seen[typeName] = true
	declarations := src.pkg.types[typeName]
	if len(declarations) != 1 {
		return "", false
	}
	declaration := declarations[0]
	if !a.enterEvaluation(declaration.spec) {
		return "", false
	}
	defer a.leaveEvaluation()
	if underlying, ok := declaration.spec.Type.(*ast.Ident); ok {
		return a.field(underlying.Name, name, declaration.source, seen)
	}
	structure, ok := declaration.spec.Type.(*ast.StructType)
	if !ok {
		return "", false
	}
	var types []string
	for _, field := range structure.Fields.List {
		for _, id := range field.Names {
			if id.Name == name {
				types = append(types, a.typeOf(field.Type, nil, declaration.source))
			}
		}
	}
	if len(types) == 1 {
		return types[0], true
	}
	// Embedded/promoted fields and methods are intentionally not inferred.
	return "", false
}

func (a *analyzer) callResults(call *ast.CallExpr, s *scope, src *source) []value {
	if !a.enterEvaluation(call) {
		return nil
	}
	defer a.leaveEvaluation()
	if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "new" {
		if _, shadowed := s.lookup("new"); !shadowed && len(src.pkg.globals["new"]) == 0 && len(src.pkg.functions["new"]) == 0 && len(call.Args) == 1 {
			if typ := a.typeOf(call.Args[0], s, src); typ != "" {
				return []value{{typ: "*" + typ}}
			}
		}
	}
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		receiver := a.eval(selector.X, s, src)
		if receiver.pkg == "net/http" {
			if selector.Sel.Name == "NewServeMux" {
				return []value{{typ: "*http.ServeMux"}}
			}
			if selector.Sel.Name == "HandlerFunc" && len(call.Args) == 1 {
				v := a.eval(call.Args[0], s, src)
				v.typ, v.staticType, v.declared, v.callable = "http.HandlerFunc", "", false, true
				return []value{v}
			}
		}
		if receiver.pkg == "database/sql" && (selector.Sel.Name == "Open" || selector.Sel.Name == "OpenDB") {
			if selector.Sel.Name == "OpenDB" {
				return []value{{typ: "*sql.DB"}}
			}
			return []value{{typ: "*sql.DB"}, {}}
		}
		if receiver.typ == "*sql.DB" && (selector.Sel.Name == "Begin" || selector.Sel.Name == "BeginTx") {
			return []value{{typ: "*sql.Tx"}, {}}
		}
	}
	if fn := a.eval(call.Fun, s, src).fn; fn != nil {
		var values []value
		if fn.decl.Type.Results != nil {
			for _, field := range fn.decl.Type.Results.List {
				count := len(field.Names)
				if count == 0 {
					count = 1
				}
				for i := 0; i < count; i++ {
					values = append(values, a.declaredValue(value{}, field.Type, fn.typeScope, fn.source))
				}
			}
		}
		return values
	}
	return nil
}

func (a *analyzer) bindFields(fields *ast.FieldList, s, typeScope *scope) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		v := a.declaredValue(value{}, field.Type, typeScope, a.src)
		for _, name := range field.Names {
			s.names[name.Name] = v
		}
	}
}

func (a *analyzer) declaredValue(v value, expr ast.Expr, s *scope, src *source) value {
	typ := a.typeOf(expr, s, src)
	v.typ, v.staticType, v.declared = typ, typ, true
	v.callable = a.callableType(expr, s, src)
	if !v.callable {
		v.fn = nil
	}
	return v
}

func (a *analyzer) callableType(expr ast.Expr, s *scope, src *source) bool {
	if expr == nil || !a.enterEvaluation(expr) {
		return false
	}
	defer a.leaveEvaluation()
	switch t := expr.(type) {
	case *ast.FuncType:
		return true
	case *ast.ParenExpr:
		return a.callableType(t.X, s, src)
	case *ast.SelectorExpr:
		return a.typeOf(t, s, src) == "http.HandlerFunc"
	case *ast.Ident:
		if _, shadowed := s.lookup(t.Name); shadowed {
			return false
		}
		if declarations := src.pkg.types[t.Name]; len(declarations) == 1 {
			return a.callableType(declarations[0].spec.Type, nil, declarations[0].source)
		}
	}
	return false
}

func functionTypeScope(decl *ast.FuncDecl) *scope {
	s := newScope(nil)
	if decl.Type.TypeParams != nil {
		for _, field := range decl.Type.TypeParams.List {
			for _, name := range field.Names {
				s.names[name.Name] = value{}
			}
		}
	}
	if decl.Recv != nil {
		for _, field := range decl.Recv.List {
			expr := unparen(field.Type)
			if star, ok := expr.(*ast.StarExpr); ok {
				expr = unparen(star.X)
			}

			var indices []ast.Expr
			switch t := expr.(type) {
			case *ast.IndexExpr:
				indices = []ast.Expr{t.Index}
			case *ast.IndexListExpr:
				indices = t.Indices
			}
			for _, index := range indices {
				if name, ok := index.(*ast.Ident); ok {
					s.names[name.Name] = value{}
				}
			}
		}
	}
	if len(s.names) == 0 {
		return nil
	}
	return s
}

func unparen(expr ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = parenthesized.X
	}
}
