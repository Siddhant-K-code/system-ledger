package goanalysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
)

type source struct {
	path    string
	ast     *ast.File
	imports map[string]string
	pkg     *sourcePackage
}

type sourcePackage struct {
	key       string
	name      string
	files     []*source
	functions map[string][]*function
	types     map[string][]typeDecl
	globals   map[string][]globalDecl
}

type function struct {
	key       string
	source    *source
	decl      *ast.FuncDecl
	receiver  string
	typeScope *scope
}

type typeDecl struct {
	source *source
	spec   *ast.TypeSpec
}

type globalDecl struct {
	source  *source
	typ     ast.Expr
	expr    ast.Expr
	isConst bool
}

type value struct {
	typ string
	// A binding's static type cannot be replaced by an assigned concrete value.
	staticType string
	declared   bool
	callable   bool
	text       *string
	origins    []Locator
	fn         *function
	pkg        string
	unknown    bool
	escaped    bool
	limited    bool
}

type scope struct {
	parent *scope
	names  map[string]value
}

func newScope(parent *scope) *scope {
	return &scope{parent: parent, names: map[string]value{}}
}

func (s *scope) lookup(name string) (value, bool) {
	for current := s; current != nil; current = current.parent {
		if v, ok := current.names[name]; ok {
			return v, true
		}
	}
	return value{}, false
}

func (s *scope) assign(name string, v value) {
	if name == "_" {
		return
	}
	for current := s; current != nil; current = current.parent {
		if previous, ok := current.names[name]; ok {
			current.names[name] = assignedValue(previous, v)
			return
		}
	}
	// Do not infer a new package-global value from a mutation in one function.
}

func (s *scope) define(name string, v value) {
	if name == "_" {
		return
	}
	if previous, exists := s.names[name]; exists {
		v = assignedValue(previous, v)
	} else {
		v.staticType, v.declared = v.typ, true
		v.callable = v.callable || v.fn != nil
	}
	s.names[name] = v
}

func (s *scope) escape(name string) {
	for current := s; current != nil; current = current.parent {
		if v, ok := current.names[name]; ok {
			v.text, v.fn, v.escaped = nil, nil, true
			v.origins = nil
			if !v.declared {
				v.typ = ""
			}
			current.names[name] = v
			return
		}
	}
}

func assignedValue(previous, next value) value {
	if previous.declared {
		if next.typ != previous.staticType {
			next.typ = ""
		}
		if previous.callable && (next.callable || next.fn != nil) {
			next.typ = previous.staticType
		}
		next.staticType, next.declared, next.callable = previous.staticType, true, previous.callable
		if !previous.callable {
			next.fn = nil
		}
	}
	if previous.escaped || next.escaped {
		next.text, next.fn, next.escaped = nil, nil, true
		next.origins = nil
		if !next.declared {
			next.typ = ""
		}
	}
	return next
}

type analyzer struct {
	fset        *token.FileSet
	result      Result
	fn          *function
	src         *source
	stack       map[string]bool
	controlFlow bool
	constants   map[string]value
	evalWork    int
	evalDepth   int
	evalBytes   int
	evalGaps    map[token.Pos]bool
}

// Analyze performs a static source inventory, including all supplied build-tag
// variants. It neither executes Go tools nor resolves external dependencies.
// Malformed source returns an error and no partial inventory.
func Analyze(files []File) (Result, error) {
	a := &analyzer{fset: token.NewFileSet(), stack: map[string]bool{}, constants: map[string]value{}, evalGaps: map[token.Pos]bool{}}
	inputs := append([]File(nil), files...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	packages := map[string]*sourcePackage{}
	var sources []*source
	seen := map[string]bool{}
	for _, input := range inputs {
		filePath := strings.ReplaceAll(input.Path, "\\", "/")
		if path.IsAbs(filePath) || filePath == ".." || strings.HasPrefix(filePath, "../") || path.Clean(filePath) != filePath {
			return Result{}, fmt.Errorf("Go source path must be clean and project-relative: %q", input.Path)
		}
		if !strings.HasSuffix(filePath, ".go") || strings.HasSuffix(filePath, "_test.go") || generated(input.Content) {
			continue
		}
		if seen[filePath] {
			return Result{}, fmt.Errorf("duplicate Go source path %q", filePath)
		}
		seen[filePath] = true
		tree, err := parser.ParseFile(a.fset, filePath, input.Content, parser.ParseComments|parser.AllErrors|parser.SkipObjectResolution)
		if err != nil {
			return Result{}, fmt.Errorf("parse Go source %s: %w", filePath, err)
		}
		pkgKey := "go_package:" + url.PathEscape(path.Dir(filePath)) + ":" + tree.Name.Name
		pkg := packages[pkgKey]
		if pkg == nil {
			pkg = &sourcePackage{key: pkgKey, name: tree.Name.Name, functions: map[string][]*function{}, types: map[string][]typeDecl{}, globals: map[string][]globalDecl{}}
			packages[pkgKey] = pkg
			a.result.Nodes = append(a.result.Nodes, Node{Key: pkgKey, Kind: "go_package", Name: tree.Name.Name, Locator: a.loc(tree.Name), Attributes: map[string]string{"directory": path.Dir(filePath), "package": tree.Name.Name, "analysis": "static_source_inventory"}})
		}
		src := &source{path: filePath, ast: tree, imports: map[string]string{}, pkg: pkg}
		pkg.files = append(pkg.files, src)
		sources = append(sources, src)
		a.src = src
		for _, imp := range tree.Imports {
			importPath, _ := strconv.Unquote(imp.Path.Value)
			name := path.Base(importPath)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name == "." {
				a.gap("go_dot_import", "Dot imports cannot be resolved conservatively.", imp)
			} else if name != "_" {
				if _, exists := src.imports[name]; exists {
					src.imports[name] = ""
				} else {
					src.imports[name] = importPath
				}
			}
		}
		for _, group := range tree.Comments {
			for _, comment := range group.List {
				if strings.HasPrefix(comment.Text, "//go:build ") || strings.HasPrefix(comment.Text, "// +build ") {
					a.gap("go_build_constraints", "Build constraints are not evaluated; this is a static source inventory, not a deployed build.", comment)
				}
			}
		}
		a.index(src)
	}
	for _, src := range sources {
		a.src = src
		for _, decl := range src.ast.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				for _, fn := range src.pkg.functions[functionSymbol(fd)] {
					if fn.decl == fd {
						a.fn = fn
						a.analyzeFunction(fn)
						a.fn = nil
						break
					}
				}
			}
		}
	}
	a.sort()
	return a.result, nil
}

func generated(content []byte) bool {
	var scan scanner.Scanner
	fset := token.NewFileSet()
	scan.Init(fset.AddFile("", -1, len(content)), content, func(token.Position, string) {}, scanner.ScanComments)
	for {
		_, tok, text := scan.Scan()
		if tok == token.PACKAGE || tok == token.EOF {
			return false
		}
		if tok == token.COMMENT && strings.HasPrefix(text, "// Code generated ") && strings.HasSuffix(text, " DO NOT EDIT.") {
			return true
		}
	}
}

func receiverName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	e := unparen(fd.Recv.List[0].Type)
	prefix := ""
	if star, ok := e.(*ast.StarExpr); ok {
		prefix, e = "*", unparen(star.X)
	}
	switch t := e.(type) {
	case *ast.IndexExpr:
		e = t.X
	case *ast.IndexListExpr:
		e = t.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return prefix + id.Name
	}
	return "?"
}

func functionSymbol(fd *ast.FuncDecl) string {
	if recv := receiverName(fd); recv != "" {
		return strings.TrimPrefix(recv, "*") + "." + fd.Name.Name
	}
	return fd.Name.Name
}

func (a *analyzer) index(src *source) {
	for _, declaration := range src.ast.Decls {
		switch decl := declaration.(type) {
		case *ast.FuncDecl:
			loc := a.loc(decl)
			recv := receiverName(decl)
			qualified := src.pkg.name + "."
			if recv != "" {
				qualified += recv + "."
			}
			qualified += decl.Name.Name
			key := "go_function:" + url.PathEscape(src.path) + ":" + qualified
			symbol := functionSymbol(decl)
			duplicate := decl.Name.Name == "init"
			for _, existing := range src.pkg.functions[symbol] {
				if existing.source.path == src.path {
					duplicate = true
				}
			}
			if duplicate {
				key += ":" + strconv.Itoa(loc.Line)
			}
			fn := &function{key: key, source: src, decl: decl, receiver: recv, typeScope: functionTypeScope(decl)}
			src.pkg.functions[symbol] = append(src.pkg.functions[symbol], fn)
			a.result.Nodes = append(a.result.Nodes, Node{Key: key, Kind: "go_function", Name: qualified, Locator: loc, Attributes: map[string]string{
				"package": src.pkg.name, "receiver": recv, "name": decl.Name.Name, "start_line": strconv.Itoa(loc.Line), "end_line": strconv.Itoa(loc.EndLine),
			}})
			a.result.Relations = append(a.result.Relations, Relation{From: src.pkg.key, To: key, Kind: "contains", Locator: loc})
		case *ast.GenDecl:
			var previous []ast.Expr
			var previousType ast.Expr
			for _, specification := range decl.Specs {
				switch spec := specification.(type) {
				case *ast.TypeSpec:
					src.pkg.types[spec.Name.Name] = append(src.pkg.types[spec.Name.Name], typeDecl{src, spec})
				case *ast.ValueSpec:
					values, typ := spec.Values, spec.Type
					if decl.Tok == token.CONST {
						if len(values) == 0 {
							values, typ = previous, previousType
						} else {
							previous, previousType = values, typ
						}
					}
					for i, name := range spec.Names {
						var expr ast.Expr
						if len(values) == len(spec.Names) {
							expr = values[i]
						}
						src.pkg.globals[name.Name] = append(src.pkg.globals[name.Name], globalDecl{src, typ, expr, decl.Tok == token.CONST})
					}
				}
			}
		}
	}
}

func (a *analyzer) loc(node ast.Node) Locator {
	start, end := a.fset.PositionFor(node.Pos(), false), a.fset.PositionFor(node.End()-1, false)
	return Locator{Path: start.Filename, Line: start.Line, EndLine: end.Line}
}

func (a *analyzer) gap(kind, message string, node ast.Node) {
	key := ""
	if a.fn != nil {
		key = a.fn.key
	}
	a.result.Gaps = append(a.result.Gaps, Gap{Kind: kind, Message: message, NodeKey: key, Locator: a.loc(node)})
}

func (a *analyzer) factKey(kind string, node ast.Node) string {
	pos := a.fset.PositionFor(node.Pos(), false)
	return kind + ":" + url.PathEscape(pos.Filename) + ":" + strconv.Itoa(pos.Line) + ":" + strconv.Itoa(pos.Column)
}

func (a *analyzer) relation(from, to, kind string, node ast.Node) {
	a.result.Relations = append(a.result.Relations, Relation{From: from, To: to, Kind: kind, Locator: a.loc(node)})
}

func (a *analyzer) sort() {
	sort.Slice(a.result.Nodes, func(i, j int) bool { return a.result.Nodes[i].Key < a.result.Nodes[j].Key })
	sort.Slice(a.result.Relations, func(i, j int) bool {
		x, y := a.result.Relations[i], a.result.Relations[j]
		return fmt.Sprintf("%s:%s:%s:%s:%09d", x.From, x.To, x.Kind, x.Path, x.Line) < fmt.Sprintf("%s:%s:%s:%s:%09d", y.From, y.To, y.Kind, y.Path, y.Line)
	})
	sort.Slice(a.result.TableRefs, func(i, j int) bool {
		x, y := a.result.TableRefs[i], a.result.TableRefs[j]
		return x.QueryKey+x.Schema+x.Table+x.Access < y.QueryKey+y.Schema+y.Table+y.Access
	})
	sort.Slice(a.result.Gaps, func(i, j int) bool {
		x, y := a.result.Gaps[i], a.result.Gaps[j]
		return fmt.Sprintf("%s:%09d:%s:%s", x.Path, x.Line, x.Kind, x.Message) < fmt.Sprintf("%s:%09d:%s:%s", y.Path, y.Line, y.Kind, y.Message)
	})
}
