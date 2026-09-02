package mutate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var generatedLine = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// operatorSet records enabled groups so enumeration can avoid generating unwanted files.
type operatorSet map[string]bool

var operatorDescriptions = []struct{ name, description string }{
	{"conditionals", "comparison, logical, and if-condition mutations"},
	{"arithmetic", "arithmetic binary operator swaps"},
	{"incdec", "increment, decrement, and compound assignment swaps"},
	{"negation", "logical negation removal"},
	{"boolean", "Boolean literal flips"},
	{"literal", "integer literal boundary changes"},
	{"errnil", "error return replacement with nil"},
	{"loop", "for-condition forcing to false"},
	{"remove", "break, continue, assignment, and increment removal"},
	{"string", "string literal emptying and same-function swaps"},
	{"field", "composite literal field removal"},
	{"slice", "slice and index bound shifts"},
	{"switch", "case clause removal and emptying"},
	{"range", "range loop iteration forcing"},
	{"return", "non-error return value zeroing"},
}

func splitList(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

// parseOperators validates names early so misspelled groups cannot silently weaken a run.
func parseOperators(value string) (operatorSet, error) {
	valid := make(operatorSet, len(operatorDescriptions))
	for _, group := range operatorDescriptions {
		valid[group.name] = true
	}
	if strings.TrimSpace(value) == "all" {
		return valid, nil
	}
	selected := make(operatorSet)
	for _, name := range splitList(value) {
		if !valid[name] {
			names := make([]string, 0, len(operatorDescriptions))
			for _, group := range operatorDescriptions {
				names = append(names, group.name)
			}
			return nil, fmt.Errorf("unknown operator %q; valid names: %s", name, strings.Join(names, ", "))
		}
		selected[name] = true
	}
	return selected, nil
}

// Mutant keeps an edit and its evidence together so execution and reporting cannot disagree.
type Mutant struct {
	path, relativePath, packagePath, function, description, original, mutated string
	reason                                                                    string
	allowlistReason                                                           string
	line, column                                                              int
	source, originalSource                                                    []byte
	functionLine                                                              int
}

// Note preserves non-fatal enumeration diagnostics for phase reporting.
type Note struct {
	Key     string
	Message string
}

// Enumerate centralizes selection so every runner uses the same stable keys.
func Enumerate(root string, packageDirs []string, only string) ([]Mutant, []Note, error) {
	operators, err := parseOperators("all")
	if err != nil {
		return nil, nil, err
	}
	mutants, err := enumeratePackages(root, packageDirs, operators)
	if err != nil {
		return nil, nil, err
	}
	pattern, err := regexp.Compile(only)
	if err != nil {
		return nil, nil, fmt.Errorf("compile --only expression: %w", err)
	}
	selected := make([]Mutant, 0, len(mutants))
	notes := make([]Note, 0)
	for _, current := range mutants {
		if current.reason != "" {
			notes = append(notes, Note{Key: current.key(), Message: current.reason})
		}
		if pattern.MatchString(current.key()) {
			selected = append(selected, current)
		}
	}
	return selected, notes, nil
}

// Accessors keep mutation construction private while allowing execution and reports to share exact evidence.
func (m Mutant) Path() string            { return m.path }
func (m Mutant) RelativePath() string    { return m.relativePath }
func (m Mutant) PackagePath() string     { return m.packagePath }
func (m Mutant) Function() string        { return m.function }
func (m Mutant) Description() string     { return m.description }
func (m Mutant) Original() string        { return m.original }
func (m Mutant) Mutated() string         { return m.mutated }
func (m Mutant) Reason() string          { return m.reason }
func (m Mutant) AllowlistReason() string { return m.allowlistReason }
func (m Mutant) Line() int               { return m.line }
func (m Mutant) Column() int             { return m.column }
func (m Mutant) Source() []byte          { return m.source }
func (m Mutant) OriginalSource() []byte  { return m.originalSource }
func (m Mutant) FunctionLine() int       { return m.functionLine }
func (m Mutant) Key() string             { return m.key() }
func (m Mutant) Position() string        { return m.position() }

// key remains stable when unrelated lines move because it describes the mutated syntax.
func (m Mutant) key() string {
	return strings.Join([]string{m.relativePath, m.function, m.description, normalizeSpace(m.original)}, "|")
}

// position gives diagnostics the familiar source location format.
func (m Mutant) position() string {
	return fmt.Sprintf("%s:%d:%d", m.relativePath, m.line, m.column)
}

// replacement describes one textual edit derived from an AST node.
type replacement struct {
	start, end                           int
	description, original, expr, mutated string
	function                             string
	line, column, functionLine           int
}

// enumeratePackages discovers production Go files without relying on git state.
func enumeratePackages(root string, packages []string, operators operatorSet) ([]Mutant, error) {
	var mutants []Mutant
	for _, packagePath := range packages {
		directory := packagePath
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(root, directory)
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("read package %s: %w", packagePath, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			found, enumerateErr := enumerateFileWithOperators(root, path, operators)
			if enumerateErr != nil {
				return nil, enumerateErr
			}
			for index := range found {
				found[index].packagePath = packagePath
			}
			mutants = append(mutants, found...)
		}
	}
	sort.Slice(mutants, func(i, j int) bool { return mutants[i].key() < mutants[j].key() })
	return mutants, nil
}

// enumerateFile creates each distinct formatted replacement while preserving its original AST position.
func enumerateFile(root, path string) ([]Mutant, error) {
	operators, err := parseOperators("all")
	if err != nil {
		return nil, err
	}
	return enumerateFileWithOperators(root, path, operators)
}

// enumerateFileWithOperators creates only replacements from the requested groups.
func enumerateFileWithOperators(root, path string, operators operatorSet) ([]Mutant, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	firstLine, _, _ := bytes.Cut(source, []byte("\n"))
	if generatedLine.Match(bytes.TrimSuffix(firstLine, []byte("\r"))) {
		return nil, nil
	}
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	replacements := collectReplacements(fileSet, file, source, operators)
	seen := make(map[[sha256.Size]byte]struct{}, len(replacements))
	result := make([]Mutant, 0, len(replacements))
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return nil, fmt.Errorf("make %s relative: %w", path, err)
	}
	for _, edit := range replacements {
		mutated := append([]byte(nil), source[:edit.start]...)
		mutated = append(mutated, edit.expr...)
		mutated = append(mutated, source[edit.end:]...)
		formatted, formatErr := format.Source(mutated)
		reason := ""
		if formatErr != nil {
			reason = formatErr.Error()
		} else {
			mutated = formatted
			_, formatErr = parser.ParseFile(token.NewFileSet(), path, mutated, parser.SkipObjectResolution)
			if formatErr != nil {
				reason = formatErr.Error()
			}
		}
		hash := sha256.Sum256(mutated)
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		current := Mutant{path: path, relativePath: filepath.ToSlash(relative), packagePath: "", function: edit.function,
			description: edit.description, original: edit.original, mutated: edit.mutated,
			line: edit.line, column: edit.column, functionLine: edit.functionLine, source: mutated, originalSource: source, reason: reason, allowlistReason: ""}
		allowlistReason, markerErr := equivalentMarker(source, edit.line, relative)
		if markerErr != nil {
			return nil, markerErr
		}
		current.allowlistReason = allowlistReason
		result = append(result, current)
	}
	return result, nil
}

// equivalentMarker reads a justified equivalence marker from the mutated source line.
func equivalentMarker(source []byte, line int, path string) (string, error) {
	lines := bytes.Split(source, []byte("\n"))
	if line < 1 || line > len(lines) {
		return "", nil
	}
	const marker = "//placebo:equivalent"
	text := string(lines[line-1])
	index := strings.Index(text, marker)
	if index < 0 {
		return "", nil
	}
	reason := strings.TrimSpace(text[index+len(marker):])
	if len(strings.Fields(reason)) == 0 {
		return "", fmt.Errorf("%s:%d: //placebo:equivalent requires a reason", filepath.ToSlash(path), line)
	}
	return reason, nil
}

// collectReplacements walks with explicit enter and leave events so each key names its enclosing function.
func collectReplacements(fileSet *token.FileSet, file *ast.File, source []byte, operators operatorSet) []replacement {
	collector := mutationCollector{
		fileSet: fileSet, source: source, function: "", functionLine: 0, replacements: nil, operators: operators,
		errorResults: nil, resultTypes: nil, arrayLiterals: nil, stringTargets: nil, skippedStrings: nil,
		forHeaders: forHeaderStatements(file),
	}
	ast.Walk(&collector, file)
	return collector.replacements
}

// mutationCollector records edits rather than modifying shared syntax trees.
type mutationCollector struct {
	fileSet        *token.FileSet
	source         []byte
	function       string
	functionLine   int
	replacements   []replacement
	operators      operatorSet
	errorResults   []bool
	resultTypes    []ast.Expr
	arrayLiterals  map[token.Pos]bool
	stringTargets  map[token.Pos][]string
	skippedStrings map[token.Pos]bool
	forHeaders     map[token.Pos]*ast.ForStmt
}

// Visit maintains function scope and emits all operators attached to a node.
func (c *mutationCollector) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	if declaration, ok := node.(*ast.FuncDecl); ok {
		child := *c
		child.function = declaration.Name.Name
		child.functionLine = c.fileSet.Position(declaration.Pos()).Line
		child.errorResults = resultErrors(declaration.Type.Results)
		child.resultTypes = resultTypes(declaration.Type.Results)
		child.arrayLiterals = arrayLiteralPositions(declaration.Body)
		child.stringTargets, child.skippedStrings = stringLiteralTargets(declaration.Body)
		ast.Walk(&child, declaration.Body)
		c.replacements = append(c.replacements, child.replacements[len(c.replacements):]...)
		return nil
	}
	if literal, ok := node.(*ast.FuncLit); ok {
		child := *c
		child.errorResults = resultErrors(literal.Type.Results)
		child.resultTypes = resultTypes(literal.Type.Results)
		child.arrayLiterals = arrayLiteralPositions(literal.Body)
		child.stringTargets, child.skippedStrings = stringLiteralTargets(literal.Body)
		ast.Walk(&child, literal.Body)
		c.replacements = append(c.replacements, child.replacements[len(c.replacements):]...)
		return nil
	}
	switch current := node.(type) {
	case *ast.BinaryExpr:
		for _, mutation := range binaryMutations(current.Op, c.operators) {
			original := printNode(c.fileSet, current)
			mutated := printNode(c.fileSet, current.X) + " " + mutation[1] + " " + printNode(c.fileSet, current.Y)
			c.add(current.OpPos, current.OpPos+token.Pos(len(current.Op.String())), mutation, original, mutated)
		}
	case *ast.IfStmt:
		if !c.operators["conditionals"] {
			break
		}
		original := printNode(c.fileSet, current.Cond)
		c.add(current.Cond.Pos(), current.Cond.End(), [2]string{"condition -> true", "true"}, original, "true")
		c.add(current.Cond.Pos(), current.Cond.End(), [2]string{"condition -> false", "false"}, original, "false")
	case *ast.AssignStmt:
		if mutation, ok := assignmentMutation(current.Tok, c.operators); ok {
			c.add(current.TokPos, current.TokPos+token.Pos(len(current.Tok.String())), mutation, printNode(c.fileSet, current), mutation[1])
		}
		if current.Tok == token.ASSIGN && c.operators["remove"] {
			c.remove(current, "remove assignment")
		}
	case *ast.IncDecStmt:
		if c.operators["incdec"] {
			target := token.INC
			if current.Tok == token.INC {
				target = token.DEC
			}
			c.add(current.TokPos, current.TokPos+2, [2]string{current.Tok.String() + " -> " + target.String(), target.String()}, printNode(c.fileSet, current), printNode(c.fileSet, current.X)+target.String())
		}
		if c.operators["remove"] {
			c.remove(current, "remove "+printNode(c.fileSet, current))
		}
	case *ast.UnaryExpr:
		if current.Op == token.NOT && c.operators["negation"] {
			c.add(current.Pos(), current.End(), [2]string{"remove !", printNode(c.fileSet, current.X)}, printNode(c.fileSet, current), printNode(c.fileSet, current.X))
		}
	case *ast.Ident:
		if c.operators["boolean"] && (current.Name == "true" || current.Name == "false") {
			target := "true"
			if current.Name == "true" {
				target = "false"
			}
			c.add(current.Pos(), current.End(), [2]string{current.Name + " -> " + target, target}, current.Name, target)
		}
	case *ast.BasicLit:
		if current.Kind == token.INT && c.operators["literal"] && !c.arrayLiterals[current.Pos()] {
			c.addInteger(current)
		}
		if current.Kind == token.STRING && c.function != "" && c.operators["string"] && !c.skippedStrings[current.Pos()] {
			c.addString(current)
		}
	case *ast.CompositeLit:
		if c.function != "" && c.operators["field"] {
			c.addFieldRemovals(current)
		}
	case *ast.ReturnStmt:
		if c.operators["errnil"] && len(current.Results) == len(c.errorResults) {
			for index, expression := range current.Results {
				identifier, isNil := expression.(*ast.Ident)
				if c.errorResults[index] && (!isNil || identifier.Name != "nil") {
					c.add(expression.Pos(), expression.End(), [2]string{"error -> nil", "nil"}, printNode(c.fileSet, expression), "nil")
				}
			}
		}
		if c.operators["return"] {
			c.addReturnMutations(current)
		}
	case *ast.ForStmt:
		if current.Cond != nil && c.operators["loop"] {
			c.add(current.Cond.Pos(), current.Cond.End(), [2]string{"loop condition -> false", "false"}, printNode(c.fileSet, current.Cond), "false")
		}
	case *ast.SliceExpr:
		if c.operators["slice"] {
			c.addSliceMutations(current)
		}
	case *ast.IndexExpr:
		if c.operators["slice"] {
			c.addIndexMutations(current)
		}
	case *ast.SwitchStmt:
		if c.operators["switch"] {
			c.addSwitchMutations(current)
		}
	case *ast.RangeStmt:
		if c.operators["range"] {
			c.addRangeMutations(current)
		}
	case *ast.BranchStmt:
		if c.operators["remove"] && current.Label == nil && (current.Tok == token.BREAK || current.Tok == token.CONTINUE) {
			c.remove(current, "remove "+current.Tok.String())
		}
	}
	return c
}

// addString empties behavior-bearing strings and substitutes the function's small local vocabulary.
func (c *mutationCollector) addString(literal *ast.BasicLit) {
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return
	}
	if value != "" {
		c.add(literal.Pos(), literal.End(), [2]string{displayLiteral(literal.Value) + ` -> ""`, `""`}, literal.Value, `""`)
	}
	for _, target := range c.stringTargets[literal.Pos()] {
		c.add(literal.Pos(), literal.End(), [2]string{displayLiteral(literal.Value) + " -> " + displayLiteral(target), target}, literal.Value, target)
	}
}

// addFieldRemovals removes source text so positions inside anonymous types retain their line breaks.
func (c *mutationCollector) addFieldRemovals(literal *ast.CompositeLit) {
	for index, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key := printNode(c.fileSet, keyValue.Key)
		start := c.fileSet.Position(element.Pos()).Offset
		end := c.fileSet.Position(element.End()).Offset
		if comma := bytes.IndexByte(c.source[end:c.fileSet.Position(literal.Rbrace).Offset], ','); comma >= 0 {
			end += comma + 1
		} else if index > 0 {
			previousEnd := c.fileSet.Position(literal.Elts[index-1].End()).Offset
			if comma := bytes.LastIndexByte(c.source[previousEnd:start], ','); comma >= 0 {
				start = previousEnd + comma
			}
		}
		literalStart := c.fileSet.Position(literal.Pos()).Offset
		literalEnd := c.fileSet.Position(literal.End()).Offset
		mutatedLiteral := append([]byte(nil), c.source[literalStart:start]...)
		mutatedLiteral = append(mutatedLiteral, c.source[end:literalEnd]...)
		position := c.fileSet.Position(element.Pos())
		c.replacements = append(c.replacements, replacement{start: start, end: end,
			description: "remove field " + key, expr: "", original: printNode(c.fileSet, keyValue), mutated: normalizeSpace(string(mutatedLiteral)), function: c.function,
			line: position.Line, column: position.Column, functionLine: c.functionLine})
	}
}

// displayLiteral bounds source-form descriptions so allowlist keys remain readable.
func displayLiteral(literal string) string {
	runes := []rune(literal)
	if len(runes) <= 30 {
		return literal
	}
	return string(runes[:27]) + "..."
}

// stringLiteralTargets collects one representative spelling per value and excludes nested functions.
func stringLiteralTargets(body *ast.BlockStmt) (map[token.Pos][]string, map[token.Pos]bool) {
	skipped := make(map[token.Pos]bool)
	ast.Inspect(body, func(node ast.Node) bool {
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || !isMessageCall(call.Fun) {
			return true
		}
		for _, argument := range call.Args {
			if literal, direct := argument.(*ast.BasicLit); direct && literal.Kind == token.STRING {
				skipped[literal.Pos()] = true
			}
		}
		return true
	})
	type foundLiteral struct {
		position token.Pos
		value    string
	}
	var literals []foundLiteral
	distinct := make(map[string]string)
	ast.Inspect(body, func(node ast.Node) bool {
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING || skipped[literal.Pos()] {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil {
			literals = append(literals, foundLiteral{position: literal.Pos(), value: value})
			if _, exists := distinct[value]; !exists {
				distinct[value] = literal.Value
			}
		}
		return true
	})
	targets := make(map[token.Pos][]string)
	if len(distinct) > 6 {
		return targets, skipped
	}
	for _, literal := range literals {
		for value, text := range distinct {
			if value != literal.value {
				targets[literal.position] = append(targets[literal.position], text)
			}
		}
		sort.Strings(targets[literal.position])
	}
	return targets, skipped
}

// isMessageCall skips conventional error and formatting calls because mutating diagnostic prose creates noise rather than behavioral coverage.
func isMessageCall(callee ast.Expr) bool {
	if identifier, ok := callee.(*ast.Ident); ok {
		return identifier.Name == "panic"
	}
	selector, ok := callee.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	if packageName.Name == "errors" {
		return selector.Sel.Name == "New"
	}
	if packageName.Name == "fmt" {
		return true
	}
	name := selector.Sel.Name
	return name == "Errorf" || name == "New" || strings.HasPrefix(name, "Wrap")
}

// remove reprints for clauses because their separators belong to the enclosing statement.
func (c *mutationCollector) remove(statement ast.Stmt, description string) {
	if loop := c.forHeaders[statement.Pos()]; loop != nil {
		clone := *loop
		if loop.Init == statement {
			clone.Init = nil
		}
		if loop.Post == statement {
			clone.Post = nil
		}
		c.add(loop.Pos(), loop.End(), [2]string{description, printNodeRaw(c.fileSet, &clone)}, printNode(c.fileSet, statement), printNode(c.fileSet, &clone))
		c.locateLastReplacement(statement.Pos())
		return
	}
	c.add(statement.Pos(), statement.End(), [2]string{description, ""}, printNode(c.fileSet, statement), "")
}

// forHeaderStatements identifies statements whose removal must update the enclosing AST field.
func forHeaderStatements(file *ast.File) map[token.Pos]*ast.ForStmt {
	headers := make(map[token.Pos]*ast.ForStmt)
	ast.Inspect(file, func(node ast.Node) bool {
		loop, ok := node.(*ast.ForStmt)
		if !ok {
			return true
		}
		if loop.Init != nil {
			headers[loop.Init.Pos()] = loop
		}
		if loop.Post != nil {
			headers[loop.Post.Pos()] = loop
		}
		return true
	})
	return headers
}

// addInteger derives decimal values while preserving the original literal as the key text.
func (c *mutationCollector) addInteger(literal *ast.BasicLit) {
	value, err := strconv.ParseInt(literal.Value, 0, 64)
	if err != nil {
		return
	}
	c.add(literal.Pos(), literal.End(), [2]string{fmt.Sprintf("%s -> %d", literal.Value, value+1), strconv.FormatInt(value+1, 10)}, literal.Value, strconv.FormatInt(value+1, 10))
	if value > 0 {
		c.add(literal.Pos(), literal.End(), [2]string{fmt.Sprintf("%s -> %d", literal.Value, value-1), strconv.FormatInt(value-1, 10)}, literal.Value, strconv.FormatInt(value-1, 10))
	}
}

// resultErrors expands named result fields because each name occupies a return position.
func resultErrors(fields *ast.FieldList) []bool {
	if fields == nil {
		return nil
	}
	var results []bool
	for _, field := range fields.List {
		identifier, ok := field.Type.(*ast.Ident)
		count := max(1, len(field.Names))
		for range count {
			results = append(results, ok && identifier.Name == "error")
		}
	}
	return results
}

// resultTypes expands named result fields so return expressions align with their declared types.
func resultTypes(fields *ast.FieldList) []ast.Expr {
	if fields == nil {
		return nil
	}
	var results []ast.Expr
	for _, field := range fields.List {
		for range max(1, len(field.Names)) {
			results = append(results, field.Type)
		}
	}
	return results
}

// arrayLiteralPositions excludes integer literals under array lengths without suppressing body expressions.
func arrayLiteralPositions(body *ast.BlockStmt) map[token.Pos]bool {
	positions := make(map[token.Pos]bool)
	ast.Inspect(body, func(node ast.Node) bool {
		array, ok := node.(*ast.ArrayType)
		if !ok || array.Len == nil {
			return true
		}
		ast.Inspect(array.Len, func(child ast.Node) bool {
			if literal, ok := child.(*ast.BasicLit); ok {
				positions[literal.Pos()] = true
			}
			return true
		})
		return true
	})
	return positions
}

// add translates token positions into source offsets once, before concurrent execution.
func (c *mutationCollector) add(start, end token.Pos, mutation [2]string, original, mutated string) {
	position := c.fileSet.Position(start)
	c.replacements = append(c.replacements, replacement{start: position.Offset, end: c.fileSet.Position(end).Offset,
		description: mutation[0], expr: mutation[1], original: original, mutated: normalizeSpace(mutated), function: c.function,
		line: position.Line, column: position.Column, functionLine: c.functionLine})
}

// binaryMutations defines the deliberately small mutation vocabulary for authorization predicates.
func binaryMutations(operator token.Token, operators operatorSet) [][2]string {
	var targets []token.Token
	if operators["conditionals"] {
		conditionals := map[token.Token][]token.Token{
			token.EQL: {token.NEQ}, token.NEQ: {token.EQL}, token.LSS: {token.GEQ, token.LEQ}, token.LEQ: {token.GTR, token.LSS},
			token.GTR: {token.LEQ, token.GEQ}, token.GEQ: {token.LSS, token.GTR}, token.LAND: {token.LOR}, token.LOR: {token.LAND},
		}
		targets = conditionals[operator]
	}
	if operators["arithmetic"] {
		arithmetic := map[token.Token]token.Token{token.ADD: token.SUB, token.SUB: token.ADD, token.MUL: token.QUO, token.QUO: token.MUL, token.REM: token.MUL}
		if target, exists := arithmetic[operator]; exists {
			targets = append(targets, target)
		}
	}
	result := make([][2]string, 0, len(targets))
	for _, target := range targets {
		result = append(result, [2]string{operator.String() + " -> " + target.String(), target.String()})
	}
	return result
}

// assignmentMutation maps only compound operators whose inverse remains syntactically valid.
func assignmentMutation(operator token.Token, operators operatorSet) ([2]string, bool) {
	if !operators["incdec"] {
		return [2]string{}, false
	}
	targets := map[token.Token]token.Token{token.ADD_ASSIGN: token.SUB_ASSIGN, token.SUB_ASSIGN: token.ADD_ASSIGN, token.MUL_ASSIGN: token.QUO_ASSIGN, token.QUO_ASSIGN: token.MUL_ASSIGN}
	target, ok := targets[operator]
	return [2]string{operator.String() + " -> " + target.String(), target.String()}, ok
}

// printNode produces canonical expression text for stable allowlist keys.
func printNode(fileSet *token.FileSet, node ast.Node) string {
	return normalizeSpace(printNodeRaw(fileSet, node))
}

// printNodeRaw preserves statement boundaries when a complete control-flow node becomes replacement source.
func printNodeRaw(fileSet *token.FileSet, node ast.Node) string {
	var output bytes.Buffer
	if err := printer.Fprint(&output, fileSet, node); err != nil {
		return "<unprintable expression>"
	}
	return output.String()
}

// normalizeSpace prevents formatting-only source changes from invalidating allowlist entries.
func normalizeSpace(value string) string { return strings.Join(strings.Fields(value), " ") }
