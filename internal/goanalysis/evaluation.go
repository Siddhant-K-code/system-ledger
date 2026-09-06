package goanalysis

import "go/ast"

const (
	maxEvaluationWork    = 200_000
	maxEvaluationDepth   = 128
	maxEvaluationString  = 64 << 10
	maxEvaluationBytes   = 8 << 20
	maxEvaluationOrigins = 128
)

// Budgets cover the complete inventory, not just one expansion. Cached
// constants share their immutable string values rather than copying them.
func (a *analyzer) enterEvaluation(node ast.Node) bool {
	if a.evalWork >= maxEvaluationWork || a.evalDepth >= maxEvaluationDepth {
		a.evaluationLimit(node)
		return false
	}
	a.evalWork++
	a.evalDepth++
	return true
}

func (a *analyzer) leaveEvaluation() {
	a.evalDepth--
}

func (a *analyzer) reserveString(size int, node ast.Node) bool {
	if size > maxEvaluationString || size > maxEvaluationBytes-a.evalBytes {
		a.evaluationLimit(node)
		return false
	}
	a.evalBytes += size
	return true
}

func (a *analyzer) evaluationLimit(node ast.Node) {
	if node != nil && !a.evalGaps[node.Pos()] {
		a.evalGaps[node.Pos()] = true
		a.gap("go_evaluation_limit", "Static evaluation exceeded its work, depth, string-size, or provenance budget; dependent facts are omitted.", node)
	}
}

// Origins are immutable sorted sets. Reusing identical sets keeps memoized
// empty-string doubling from duplicating provenance exponentially.
func (a *analyzer) mergeOrigins(left, right []Locator, node ast.Node) ([]Locator, bool) {
	if sameOrigins(left, right) || len(right) == 0 {
		return left, true
	}
	if len(left) == 0 {
		return right, true
	}
	capacity := len(left) + len(right)
	if capacity > maxEvaluationOrigins {
		capacity = maxEvaluationOrigins
	}
	merged := make([]Locator, 0, capacity)
	for i, j := 0, 0; i < len(left) || j < len(right); {
		var next Locator
		switch {
		case j == len(right) || (i < len(left) && beforeOrigin(left[i], right[j])):
			next = left[i]
			i++
		case i == len(left) || beforeOrigin(right[j], left[i]):
			next = right[j]
			j++
		default:
			next = left[i]
			i++
			j++
		}
		if len(merged) == maxEvaluationOrigins {
			a.evaluationLimit(node)
			return nil, false
		}
		merged = append(merged, next)
	}
	if sameOrigins(merged, left) {
		return left, true
	}
	if sameOrigins(merged, right) {
		return right, true
	}
	return merged, true
}

func sameOrigins(left, right []Locator) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func beforeOrigin(left, right Locator) bool {
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	if left.Line != right.Line {
		return left.Line < right.Line
	}
	return left.EndLine < right.EndLine
}
