package broken

import "testing"

func TestBroken(t *testing.T) {
	t.Fatal("intentional baseline failure")
}
