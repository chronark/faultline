package fixture

import "testing"

func TestFullyTested(t *testing.T) {
	if FullyTested(1) != true || FullyTested(0) != false {
		t.Fatal("boundary is wrong")
	}
}
func TestWeaklyTested(t *testing.T) {
	if !WeaklyTested(2) {
		t.Fatal("positive value rejected")
	}
}
func TestCount(t *testing.T) {
	if Count(2) != 2 {
		t.Fatal("wrong count")
	}
}
func TestEquivalent(t *testing.T) {
	if !Equivalent(1) {
		t.Fatal("positive value rejected")
	}
}
func TestCompileSensitive(t *testing.T) {
	if CompileSensitive() != 1 {
		t.Fatal("wrong value")
	}
}
