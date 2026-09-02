package run

import "testing"

func TestAutoConcurrency(t *testing.T) {
	cases := []struct {
		cpus   int
		memory uint64
		want   int
	}{{16, 31 << 30, 8}, {16, 8 << 30, 4}, {2, 64 << 30, 1}}
	for _, test := range cases {
		if got := AutoConcurrency(test.cpus, test.memory); got != test.want {
			t.Errorf("AutoConcurrency(%d, %d) = %d, want %d", test.cpus, test.memory, got, test.want)
		}
	}
}
