package mutate

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reflect"
)

// TestEnumerateFileProducesDistinctOperators guarantees overlapping comparison
// operators and forced conditions become separate, position-aware mutants.
func TestEnumerateFileProducesDistinctOperators(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture

func covers(left, right int, enabled bool) bool {
	if left >= right && enabled {
		return true
	}
	return false
}
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	mutants, err := enumerateFileWithOperators(root, path, operatorSet{"conditionals": true})
	require.NoError(t, err)
	require.Len(t, mutants, 5)

	descriptions := make([]string, 0, len(mutants))
	positions := make(map[string]string, len(mutants))
	for _, current := range mutants {
		descriptions = append(descriptions, current.description)
		positions[current.description] = current.position()
		require.Equal(t, "covers", current.function)
	}
	require.ElementsMatch(t, []string{">= -> <", ">= -> >", "&& -> ||", "condition -> true", "condition -> false"}, descriptions)
	require.Equal(t, "fixture.go:4:10", positions[">= -> <"])
	require.Equal(t, "fixture.go:4:19", positions["&& -> ||"])
	require.Equal(t, "fixture.go:4:5", positions["condition -> true"])
}

// TestRemoveForHeaderStatementsReparse guarantees deleting either edge of a for clause preserves valid syntax.
func TestRemoveForHeaderStatementsReparse(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture
func visit(cond bool) {
	for visited := 0; cond && visited < 10; visited++ { }
	var visited int
	for visited = 0; cond && visited < 10; visited++ { }
}
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	mutants, err := enumerateFileWithOperators(root, path, operatorSet{"remove": true})
	require.NoError(t, err)
	require.NotEmpty(t, mutants)
	for _, current := range mutants {
		_, parseErr := parser.ParseFile(token.NewFileSet(), current.relativePath, current.source, parser.SkipObjectResolution)
		require.NoError(t, parseErr, current.description)
	}
}

// TestEveryMutantReparses guarantees every operator emits syntactically valid Go
// around declarations and control-flow syntax whose separators depend on positions.
func TestEveryMutantReparses(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture
type record struct { value int }
func consume(func(int) int) {}
func mutate(stack []struct {
	query record
	visited bool
}, values []int, enabled bool) record {
	stack = append(stack, struct {
		query record
		visited bool
	}{query: record{value: 1}, visited: true})
	for index := 0; index < len(values); index++ { values[index]++ }
	switch len(values) { case 0: enabled = false; default: enabled = true }
	for index, value := range values { values[index] = value }
	_ = values[1:len(values)]
	consume(
		func(value int) int {
			if enabled { return value + 1 }
			return value
		},
	)
	var callback interface {
		apply(record) record
	}
	_ = callback
	return record{
		value: len(stack),
	}
}
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	mutants, err := enumerateFile(root, path)
	require.NoError(t, err)
	require.NotEmpty(t, mutants)
	for _, current := range mutants {
		_, parseErr := parser.ParseFile(token.NewFileSet(), current.relativePath, current.source, parser.SkipObjectResolution)
		require.NoError(t, parseErr, current.description)
		require.Empty(t, current.reason, current.description)
	}
}

// TestEnumerateFileProducesNewOperators guarantees every syntactic group emits its documented mutation at the source site.
func TestEnumerateFileProducesNewOperators(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture

import "fmt"

func mutate(n int, flag bool) (int, error) {
	n = n + 4
	n += 2
	n++
	flag = !flag
	flag = true
	var bytes [8]byte
	_ = bytes
	for n > 0 {
		if flag { continue }
		break
	}
	n = n - n
	n = n * n
	n = n / n
	n = n % n
	n--
	n -= n
	n *= n
	n /= n
	return n, fmt.Errorf("bad")
}
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	mutants, err := enumerateFile(root, path)
	require.NoError(t, err)

	actual := make([]string, 0, len(mutants))
	for _, current := range mutants {
		if current.description != "condition -> true" && current.description != "condition -> false" && current.description != "> -> <=" && current.description != "> -> >=" {
			actual = append(actual, fmt.Sprintf("%s@%d", current.description, current.line))
		}
	}
	require.ElementsMatch(t, []string{
		"+ -> -@6", "4 -> 5@6", "4 -> 3@6", "remove assignment@6",
		"+= -> -=@7", "2 -> 3@7", "2 -> 1@7", "++ -> --@8", "remove n++@8",
		"remove assignment@9", "remove !@9", "remove assignment@10", "true -> false@10",
		"remove assignment@12",
		"loop condition -> false@13", "0 -> 1@13", "remove continue@14", "remove break@15",
		"- -> +@17", "remove assignment@17", "* -> /@18", "remove assignment@18",
		"/ -> *@19", "remove assignment@19", "% -> *@20", "remove assignment@20",
		"-- -> ++@21", "remove n--@21", "-= -> +=@22", "*= -> /=@23", "/= -> *=@24",
		"error -> nil@25", "return n -> 0@25",
	}, actual)

	selected, err := parseOperators("literal")
	require.NoError(t, err)
	literalOnly, err := enumerateFileWithOperators(root, path, selected)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"4 -> 5", "4 -> 3", "2 -> 3", "2 -> 1", "0 -> 1"}, mutantDescriptions(literalOnly))

	_, err = parseOperators("unknown")
	require.EqualError(t, err, "unknown operator \"unknown\"; valid names: conditionals, arithmetic, incdec, negation, boolean, literal, errnil, loop, remove, string, field, slice, switch, range, return")
}

// TestFlowOperators guarantees the four flow-oriented groups enumerate their boundary variants and exclusions.
func TestFlowOperators(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture
type record struct{ N int }
type label string
func slices(xs []int, i int) { _=xs[:i]; _=xs[i:]; _=xs[:]; _=xs[i]; _=xs[2]; _=xs[1:i:cap(xs)] }
func switches(n int) { switch {
case n == 1: n++
case n == 2:
default: n--
}; switch n { case 3: } }
func ranges(xs []int) { for range xs { _=xs }; for range xs {} }
func values(ok bool, text string, n int, pointer *int, list []int, r record, l label) (bool,string,int,*int,[]int,record,label,error) { return ok,text,n,pointer,list,r,l,nil }
func zero() (bool,string,int,record) { return false,"",0,record{} }
func named() (result int) { return }
func tuple() (bool,string) { return values(false,"",0,nil,nil,record{},label(""),nil) }
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	operators, err := parseOperators("slice,switch,range,return")
	require.NoError(t, err)
	mutants, err := enumerateFileWithOperators(root, path, operators)
	require.NoError(t, err)
	actual := make([]string, 0, len(mutants))
	for _, current := range mutants {
		actual = append(actual, fmt.Sprintf("%s@%d", current.description, current.line))
	}
	require.ElementsMatch(t, []string{
		"[:i] -> [:i-1]@4", "[:i] -> [:i+1]@4", "[i:] -> [i+1:]@4", "[i:] -> [:]@4", "[:] -> [1:]@4", "[i] -> [i+1]@4", "[i] -> [i-1]@4",
		"remove case n == 1@6", "empty case n == 1@6", "remove case n == 2@7", "remove default@8", "remove case 3@9",
		"range xs -> zero iterations@10", "range xs -> one iteration@10", "range xs -> zero iterations@10",
		"return ok -> false@11", "return ok -> !(ok)@11", `return text -> ""@11`, "return n -> 0@11", "return pointer -> nil@11", "return list -> nil@11", "return r -> *new(record)@11", "return l -> *new(label)@11",
	}, actual)
	for _, current := range mutants {
		require.Contains(t, []string{"slice", "switch", "range", "return"}, operatorGroup(current.description))
	}
}

// operatorGroup classifies fixture descriptions using the report's documented prefixes.
func operatorGroup(description string) string {
	switch {
	case len(description) > 0 && description[0] == '[':
		return "slice"
	case len(description) >= 6 && description[:6] == "remove", len(description) >= 5 && description[:5] == "empty":
		return "switch"
	case len(description) >= 6 && description[:6] == "range ":
		return "range"
	case len(description) >= 7 && description[:7] == "return ":
		return "return"
	default:
		return ""
	}
}

// TestStringMutations guarantees local token swaps, cardinality bounds, and message exclusions remain syntactic.
func TestStringMutations(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture
import ("errors"; "fmt"; "strings"; "example/errs")
func pair(x string) { _ = "*"; _ = "**"; _ = strings.Split(x, "/"); _ = errors.New("error"); _ = fmt.Errorf("format"); _ = errs.New("fault") }
func many() { _="1"; _="2"; _="3"; _="4"; _="5"; _="6"; _="7" }
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	mutants, err := enumerateFileWithOperators(root, path, operatorSet{"string": true})
	require.NoError(t, err)
	require.Len(t, mutants, 16)
	descriptions := mutantDescriptions(mutants)
	require.Contains(t, descriptions, `"*" -> ""`)
	require.Contains(t, descriptions, `"*" -> "**"`)
	require.Contains(t, descriptions, `"/" -> ""`)
	require.NotContains(t, descriptions, `"error" -> ""`)
	require.NotContains(t, descriptions, `"format" -> ""`)
	require.NotContains(t, descriptions, `"fault" -> ""`)
}

// TestFieldMutations guarantees keyed struct and map elements are removable while positional elements are not.
func TestFieldMutations(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture
type item struct { A, B, C int }
func fields() { _ = item{A: 1, B: 2, C: 3}; _ = [2]int{1, 2}; _ = map[string]int{"a": 1, "b": 2} }
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	mutants, err := enumerateFileWithOperators(root, path, operatorSet{"field": true})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"remove field A", "remove field B", "remove field C", `remove field "a"`, `remove field "b"`}, mutantDescriptions(mutants))
}

// TestFieldMutationsReparseAnonymousStructLiteral guarantees removing a keyed
// element does not collapse anonymous struct field declarations into invalid syntax.
func TestFieldMutationsReparseAnonymousStructLiteral(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture
type PermissionQuery struct{}
func appendFrame(stack []struct {
	query PermissionQuery
	visited bool
}, query PermissionQuery) []struct {
	query PermissionQuery
	visited bool
} {
	return append(stack, struct {
		query PermissionQuery
		visited bool
	}{query: query, visited: true})
}
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	mutants, err := enumerateFileWithOperators(root, path, operatorSet{"field": true})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"remove field query", "remove field visited"}, mutantDescriptions(mutants))
	for _, current := range mutants {
		_, parseErr := parser.ParseFile(token.NewFileSet(), current.relativePath, current.source, parser.SkipObjectResolution)
		require.NoError(t, parseErr, current.description)
		require.Empty(t, current.reason, current.description)
	}
}

// TestStringAndFieldSelection guarantees focused runs do not enable unrelated groups.
func TestStringAndFieldSelection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	require.NoError(t, os.WriteFile(path, []byte(`package fixture
type item struct { A int }
func selected() bool { _ = "x"; _ = item{A: 1}; return true }
`), 0o600))
	operators, err := parseOperators("string,field")
	require.NoError(t, err)
	mutants, err := enumerateFileWithOperators(root, path, operators)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{`"x" -> ""`, "remove field A"}, mutantDescriptions(mutants))
}

// TestErrorReturnToNilUsesDeclaredPosition guarantees nil and tuple-forwarding returns are excluded.
func TestErrorReturnToNilUsesDeclaredPosition(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.go")
	source := `package fixture
import "fmt"
type V1 struct{}
func ok() (V1, error) { return V1{}, nil }
func bad() (V1, error) { return V1{}, fmt.Errorf("bad") }
func forwarded() (V1, error) { return bad() }
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	mutants, err := enumerateFileWithOperators(root, path, operatorSet{"errnil": true})
	require.NoError(t, err)
	require.Len(t, mutants, 1)
	require.Equal(t, "error -> nil", mutants[0].description)
	require.Equal(t, "fmt.Errorf(\"bad\")", mutants[0].original)
	require.Equal(t, 5, mutants[0].line)
}

// mutantDescriptions keeps operator filtering assertions focused on the public descriptions.
func mutantDescriptions(mutants []Mutant) []string {
	descriptions := make([]string, 0, len(mutants))
	for _, current := range mutants {
		descriptions = append(descriptions, current.description)
	}
	return descriptions
}

func TestEquivalentMarker(t *testing.T) {
	reason, err := equivalentMarker([]byte("package x\nvar x = 1 //loki:equivalent same result\n"), 2, "x.go")
	require.NoError(t, err)
	require.Equal(t, "same result", reason)
	_, err = equivalentMarker([]byte("package x\nvar x = 1 //loki:equivalent\n"), 2, "x.go")
	require.EqualError(t, err, "x.go:2: //loki:equivalent requires a reason")
}

type assertions struct{}

var require assertions

func (assertions) NoError(t *testing.T, err error, msg ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v %v", err, msg)
	}
}
func (assertions) Equal(t *testing.T, want, got any, msg ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("want %#v, got %#v %v", want, got, msg)
	}
}
func (assertions) EqualError(t *testing.T, err error, want string, msg ...any) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("want error %q, got %v %v", want, err, msg)
	}
}
func (assertions) ErrorContains(t *testing.T, err error, want string, msg ...any) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want error containing %q, got %v %v", want, err, msg)
	}
}
func (assertions) Len(t *testing.T, value any, want int, msg ...any) {
	t.Helper()
	got := reflect.ValueOf(value).Len()
	if got != want {
		t.Fatalf("want length %d, got %d %v", want, got, msg)
	}
}
func (assertions) NotEmpty(t *testing.T, value any, msg ...any) {
	t.Helper()
	if reflect.ValueOf(value).Len() == 0 {
		t.Fatalf("want non-empty %v", msg)
	}
}
func (assertions) Empty(t *testing.T, value any, msg ...any) {
	t.Helper()
	v := reflect.ValueOf(value)
	if (v.Kind() == reflect.String || v.Kind() == reflect.Slice || v.Kind() == reflect.Map) && v.Len() != 0 {
		t.Fatalf("want empty, got %v %v", value, msg)
	}
}
func (assertions) Contains(t *testing.T, value any, element any, msg ...any) {
	t.Helper()
	if !contains(value, element) {
		t.Fatalf("%v does not contain %v %v", value, element, msg)
	}
}
func (assertions) NotContains(t *testing.T, value any, element any, msg ...any) {
	t.Helper()
	if contains(value, element) {
		t.Fatalf("%v contains %v %v", value, element, msg)
	}
}
func (assertions) ElementsMatch(t *testing.T, want, got any, msg ...any) {
	t.Helper()
	a, b := reflect.ValueOf(want), reflect.ValueOf(got)
	if a.Len() != b.Len() {
		t.Fatalf("different lengths: %v != %v %v", want, got, msg)
	}
	used := make([]bool, b.Len())
	for i := 0; i < a.Len(); i++ {
		found := false
		for j := 0; j < b.Len(); j++ {
			if !used[j] && reflect.DeepEqual(a.Index(i).Interface(), b.Index(j).Interface()) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("elements differ: %v != %v %v", want, got, msg)
		}
	}
}
func contains(value, element any) bool {
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.String {
		e, ok := element.(string)
		return ok && strings.Contains(v.String(), e)
	}
	for i := 0; i < v.Len(); i++ {
		if reflect.DeepEqual(v.Index(i).Interface(), element) {
			return true
		}
	}
	return false
}
