package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type report struct {
	Summary struct{ Total, Killed, TimedOut, OOM, Survived, NotViable, Allowlisted int } `json:"summary"`
	Mutants []struct {
		Key, Status, Reproduce string
		Allowlisted            bool
	} `json:"mutants"`
}

const (
	killedKey      = "fixture.go|FullyTested|> -> >=|n > 0"
	survivedKey    = "fixture.go|WeaklyTested|> -> >=|n > 0"
	timedOutKey    = "fixture.go|Count|++ -> --|i++"
	allowlistedKey = "fixture.go|Equivalent|> -> >=|n > 0"
	notViableKey   = "fixture.go|CompileSensitive|return value -> 0|value"
)

func TestPlaceboKnownAnswers(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "placebo")
	command := exec.Command("go", "build", "-o", binary, "..")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build placebo: %v\n%s", err, output)
	}
	fixture := filepath.Join("testdata", "fixture")
	data, code := execute(t, binary, fixture, "--json", "--concurrency", "2")
	if code != 1 {
		t.Fatalf("full exit = %d, want 1", code)
	}
	want := map[string]string{killedKey: "KILLED", survivedKey: "SURVIVED", timedOutKey: "TIMED OUT", allowlistedKey: "SURVIVED", notViableKey: "NOT VIABLE"}
	for _, m := range data.Mutants {
		if status, ok := want[m.Key]; ok {
			if m.Status != status {
				t.Errorf("%s = %s, want %s", m.Key, m.Status, status)
			}
			if m.Status != "KILLED" && m.Status != "NOT VIABLE" && !strings.HasPrefix(m.Reproduce, "placebo --only") {
				t.Errorf("reproduce for %s = %q", m.Key, m.Reproduce)
			}
			delete(want, m.Key)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing keys: %v", want)
	}
	s := data.Summary
	if s.Total != 28 || s.Killed != 22 || s.TimedOut != 2 || s.OOM != 0 || s.Survived != 3 || s.NotViable != 1 || s.Allowlisted != 5 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	human := executeOutput(t, binary, fixture, "--concurrency", "2")
	for _, want := range []string{"meaning: this test is a placebo", "meaning: this mutation made the tests hang", "duration:", "next actions:", "not viable: 1", "allowlisted (still run", "slowest mutants:", strings.Repeat("-", 80)} {
		if !strings.Contains(human, want) {
			t.Errorf("human report does not contain %q", want)
		}
	}
	selected, code := execute(t, binary, fixture, "--json", "--concurrency", "2", "--only", regexp.QuoteMeta(survivedKey))
	if code != 1 || selected.Summary.Total != 1 {
		t.Fatalf("survivor selection: code=%d summary=%+v", code, selected.Summary)
	}
	killed, code := execute(t, binary, fixture, "--json", "--only", regexp.QuoteMeta(killedKey))
	if code != 0 || killed.Summary.Total != 1 {
		t.Fatalf("killed selection: code=%d summary=%+v", code, killed.Summary)
	}
	_, code = execute(t, binary, fixture, "./...")
	if code != 2 {
		t.Fatalf("recursive pattern exit = %d, want 2", code)
	}
}

func executeOutput(t *testing.T, binary, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PLACEBO_TEST_TIMEOUT_SECONDS=5")
	output, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		t.Fatalf("execute human report: %v", err)
	}
	return string(output)
}

func execute(t *testing.T, binary, dir string, args ...string) (report, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PLACEBO_TEST_TIMEOUT_SECONDS=5")
	output, err := cmd.Output()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("execute: %v", err)
		}
		code = exit.ExitCode()
	}
	var data report
	if len(output) > 0 {
		if err := json.Unmarshal(output, &data); err != nil {
			t.Fatalf("decode %q: %v", output, err)
		}
	}
	return data, code
}
