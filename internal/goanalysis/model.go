// Package goanalysis inventories Go source without building it, importing
// dependencies, or selecting a platform's build constraints.
package goanalysis

// File is a project-relative Go source path and its contents.
type File struct {
	Path    string
	Content []byte
}

// Locator points at the inclusive source-line span supporting a fact.
type Locator struct {
	Path    string
	Line    int
	EndLine int
}

type Node struct {
	Key  string
	Kind string
	Name string
	Locator
	// Evidence contains supporting literal spans for route/query strings.
	Evidence []Locator
	// Query attributes contain operation/table metadata, never raw SQL or its literals.
	Attributes map[string]string
}

type Relation struct {
	From string
	To   string
	Kind string
	Locator
}

// TableRef is unresolved: consumers must resolve it only against an
// unambiguous table declaration in the same service as the query.
type TableRef struct {
	QueryKey string
	Schema   string
	Table    string
	Access   string // "read" or "write"
	Locator
}

type Gap struct {
	Kind    string
	Message string
	NodeKey string
	Locator
}

type Result struct {
	Nodes     []Node
	Relations []Relation
	TableRefs []TableRef
	Gaps      []Gap
}
