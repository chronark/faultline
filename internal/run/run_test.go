package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRewriteLocalPackageArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := func(path string) (os.FileInfo, error) { return os.Stat(filepath.Join(root, path)) }

	arguments := []string{"pkg/x", "pkg/...", "./pkg/x", "github.com/foo/bar", ".", "./...", `C:\pkg\x`}
	want := []string{"./pkg/x", "./pkg/...", "./pkg/x", "github.com/foo/bar", ".", "./...", `C:\pkg\x`}
	got := rewriteLocalPackageArguments(arguments, stat)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argument %q rewritten to %q, want %q", arguments[i], got[i], want[i])
		}
	}
}

func TestParseBaselineRecordsPackageOutcomesAndMalformedOutput(t *testing.T) {
	packages := []Package{
		{Dir: ".", ImportPath: "example.com/root"},
		{Dir: "./notests", ImportPath: "example.com/root/notests"},
		{Dir: "./broken", ImportPath: "example.com/root/broken"},
	}
	stream := strings.NewReader(strings.Join([]string{
		`{"Action":"output","Package":"example.com/root/broken","Output":"--- FAIL: TestBroken (0.00s)\\n"}`,
		`{"Action":"pass","Package":"example.com/root","Elapsed":1.25}`,
		`{"Action":"output","Package":"example.com/root/notests","Output":"?   example.com/root/notests [no test files]\\n"}`,
		`{"Action":"skip","Package":"example.com/root/notests","Elapsed":0.01}`,
		`compiler crashed outside test2json`,
		`{"Action":"output","Package":"example.com/root/broken","Output":"FAIL\\n"}`,
		`{"Action":"fail","Package":"example.com/root/broken","Elapsed":0.02}`,
	}, "\n"))

	got, err := ParseBaseline(stream, packages)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != BaselineOK || got[0].Baseline != 1250*time.Millisecond {
		t.Errorf("passing package = %+v", got[0])
	}
	if got[1].Status != BaselineUntested {
		t.Errorf("untested package = %+v", got[1])
	}
	if got[2].Status != BaselineFailed || !strings.Contains(got[2].Output, "--- FAIL") || !strings.Contains(got[2].Output, "FAIL") {
		t.Errorf("failed package = %+v", got[2])
	}
	if !strings.Contains(got[2].Output, "compiler crashed outside test2json") {
		t.Errorf("synthetic output was not retained with the failed package: %q", got[2].Output)
	}
}

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
