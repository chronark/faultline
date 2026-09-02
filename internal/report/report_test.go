package report

import "testing"

func TestMutationDiffExtractsChangedRegion(t *testing.T) {
	original := []byte("package fixture\n\nfunc covers(n int) bool {\n\treturn n > 0\n}\n")
	mutated := []byte("package fixture\n\nfunc covers(n int) bool {\n\treturn n >= 0\n}\n")
	want := "-\treturn n > 0\n+\treturn n >= 0"
	if got := mutationDiff(original, mutated, "n > 0", "n >= 0"); got != want {
		t.Fatalf("mutationDiff() = %q, want %q", got, want)
	}
}
