package mutate

import (
	"go/ast"
	"go/token"
)

// addSliceMutations shifts two-index slice bounds while leaving three-index capacity constraints intact.
func (c *mutationCollector) addSliceMutations(slice *ast.SliceExpr) {
	if slice.Max != nil {
		return
	}
	if slice.High != nil {
		bound := printNode(c.fileSet, slice.High)
		original := sliceBounds(c, slice)
		c.add(slice.High.Pos(), slice.High.End(), [2]string{original + " -> " + sliceWithBound(c, slice, "high", bound+"-1"), bound + " - 1"}, bound, bound+" - 1")
		c.add(slice.High.Pos(), slice.High.End(), [2]string{original + " -> " + sliceWithBound(c, slice, "high", bound+"+1"), bound + " + 1"}, bound, bound+" + 1")
	}
	if slice.Low != nil {
		bound := printNode(c.fileSet, slice.Low)
		original := sliceBounds(c, slice)
		c.add(slice.Low.Pos(), slice.Low.End(), [2]string{original + " -> " + sliceWithBound(c, slice, "low", bound+"+1"), bound + " + 1"}, bound, bound+" + 1")
		c.add(slice.Low.Pos(), slice.Low.End(), [2]string{original + " -> " + sliceWithBound(c, slice, "low", ""), ""}, bound, "")
	} else if slice.High == nil {
		original := printNode(c.fileSet, slice)
		mutated := printNode(c.fileSet, slice.X) + "[1:]"
		c.add(slice.Pos(), slice.End(), [2]string{"[:] -> [1:]", mutated}, original, mutated)
	}
}

// sliceWithBound keeps compact descriptions faithful to the complete slice expression.
func sliceWithBound(c *mutationCollector, slice *ast.SliceExpr, bound, replacement string) string {
	low, high := "", ""
	if slice.Low != nil {
		low = printNode(c.fileSet, slice.Low)
	}
	if slice.High != nil {
		high = printNode(c.fileSet, slice.High)
	}
	if bound == "low" {
		low = replacement
	} else if bound == "high" {
		high = replacement
	}
	return displayLiteral("[" + low + ":" + high + "]")
}

// sliceBounds omits the receiver so slice descriptions have a stable, group-specific prefix.
func sliceBounds(c *mutationCollector, slice *ast.SliceExpr) string {
	return sliceWithBound(c, slice, "", "")
}

// addIndexMutations avoids integer literals because the literal group already shifts them.
func (c *mutationCollector) addIndexMutations(index *ast.IndexExpr) {
	if literal, ok := index.Index.(*ast.BasicLit); ok && literal.Kind == token.INT {
		return
	}
	bound := printNode(c.fileSet, index.Index)
	c.add(index.Index.Pos(), index.Index.End(), [2]string{"[" + displayLiteral(bound) + "] -> [" + displayLiteral(bound) + "+1]", bound + " + 1"}, bound, bound+" + 1")
	c.add(index.Index.Pos(), index.Index.End(), [2]string{"[" + displayLiteral(bound) + "] -> [" + displayLiteral(bound) + "-1]", bound + " - 1"}, bound, bound+" - 1")
}

// addSwitchMutations reprints the switch because clause separators belong to the enclosing block.
func (c *mutationCollector) addSwitchMutations(statement *ast.SwitchStmt) {
	for index, item := range statement.Body.List {
		clause, ok := item.(*ast.CaseClause)
		if !ok {
			continue
		}
		description := "remove default"
		if clause.List != nil {
			description = "remove case " + displayLiteral(expressionList(c, clause.List))
		}
		clone := *statement
		body := *statement.Body
		body.List = append([]ast.Stmt(nil), statement.Body.List[:index]...)
		body.List = append(body.List, statement.Body.List[index+1:]...)
		clone.Body = &body
		c.add(statement.Pos(), statement.End(), [2]string{description, printNodeRaw(c.fileSet, &clone)}, printNode(c.fileSet, clause), printNode(c.fileSet, &clone))
		c.locateLastReplacement(clause.Pos())
		if clause.List != nil && len(clause.Body) > 0 {
			empty := *clause
			empty.Body = []ast.Stmt{&ast.EmptyStmt{Semicolon: clause.Colon + 1, Implicit: false}}
			emptySwitch := *statement
			emptyBody := *statement.Body
			emptyBody.List = append([]ast.Stmt(nil), statement.Body.List...)
			emptyBody.List[index] = &empty
			emptySwitch.Body = &emptyBody
			c.add(statement.Pos(), statement.End(), [2]string{"empty case " + displayLiteral(expressionList(c, clause.List)), printNodeRaw(c.fileSet, &emptySwitch)}, printNode(c.fileSet, clause), printNode(c.fileSet, &emptySwitch))
			c.locateLastReplacement(clause.Pos())
		}
	}
}

// locateLastReplacement reports an enclosing-node edit at the behavior-bearing child syntax.
func (c *mutationCollector) locateLastReplacement(position token.Pos) {
	location := c.fileSet.Position(position)
	last := &c.replacements[len(c.replacements)-1]
	last.line, last.column = location.Line, location.Column
}

// expressionList uses printer output so descriptions match the source-level case expressions.
func expressionList(c *mutationCollector, expressions []ast.Expr) string {
	text := ""
	for index, expression := range expressions {
		if index > 0 {
			text += ", "
		}
		text += printNode(c.fileSet, expression)
	}
	return text
}

// addRangeMutations forces boundary iteration counts without making assumptions about the ranged type.
func (c *mutationCollector) addRangeMutations(statement *ast.RangeStmt) {
	target := printNode(c.fileSet, statement.X)
	c.add(statement.X.Pos(), statement.X.End(), [2]string{"range " + displayLiteral(target) + " -> zero iterations", target + "[:0]"}, target, target+"[:0]")
	if len(statement.Body.List) == 0 {
		return
	}
	clone := *statement
	body := *statement.Body
	body.List = append(append([]ast.Stmt(nil), statement.Body.List...), &ast.BranchStmt{TokPos: token.NoPos, Tok: token.BREAK, Label: nil})
	clone.Body = &body
	c.add(statement.Pos(), statement.End(), [2]string{"range " + displayLiteral(target) + " -> one iteration", printNodeRaw(c.fileSet, &clone)}, printNode(c.fileSet, statement), printNode(c.fileSet, &clone))
}

// addReturnMutations derives zero values from syntax so enumeration remains independent of type checking.
func (c *mutationCollector) addReturnMutations(statement *ast.ReturnStmt) {
	if len(statement.Results) == 0 || len(statement.Results) != len(c.resultTypes) {
		return
	}
	for index, expression := range statement.Results {
		typeExpression := c.resultTypes[index]
		if identifier, ok := typeExpression.(*ast.Ident); ok && identifier.Name == "error" {
			continue
		}
		zero, boolean := zeroValue(c, typeExpression)
		original := printNode(c.fileSet, expression)
		if boolean && booleanLiteral(expression) {
			continue
		}
		if !alreadyZero(expression, zero) {
			c.add(expression.Pos(), expression.End(), [2]string{"return " + displayLiteral(original) + " -> " + zero, zero}, original, zero)
		}
		if boolean && !booleanLiteral(expression) {
			negated := "!(" + original + ")"
			c.add(expression.Pos(), expression.End(), [2]string{"return " + displayLiteral(original) + " -> " + negated, negated}, original, negated)
		}
	}
}

// zeroValue chooses literals where Go has universal spellings and new for all remaining declared types.
func zeroValue(c *mutationCollector, expression ast.Expr) (string, bool) {
	if identifier, ok := expression.(*ast.Ident); ok {
		switch identifier.Name {
		case "bool":
			return "false", true
		case "string":
			return `""`, false
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune", "float32", "float64", "complex64", "complex128":
			return "0", false
		case "any":
			return "nil", false
		}
	}
	switch current := expression.(type) {
	case *ast.StarExpr, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.InterfaceType:
		return "nil", false
	case *ast.ArrayType:
		if current.Len == nil {
			return "nil", false
		}
	}
	return "*new(" + printNode(c.fileSet, expression) + ")", false
}

// alreadyZero suppresses mutations that cannot change the returned value syntactically.
func alreadyZero(expression ast.Expr, zero string) bool {
	if printNode(token.NewFileSet(), expression) == zero {
		return true
	}
	literal, ok := expression.(*ast.CompositeLit)
	return ok && len(literal.Elts) == 0
}

// booleanLiteral excludes both Boolean constants from the additional negation variant.
func booleanLiteral(expression ast.Expr) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && (identifier.Name == "true" || identifier.Name == "false")
}
