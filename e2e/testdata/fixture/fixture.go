package fixture

func FullyTested(n int) bool { return n > 0 }

func WeaklyTested(n int) bool { return n > 0 }

func Count(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total++
	}
	return total
}

func Equivalent(n int) bool { return n > 0 } //loki:equivalent callers require positive input

func CompileSensitive() int {
	value := 1
	return value
}
