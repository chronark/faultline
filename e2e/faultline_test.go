package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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

func TestFaultlineKnownAnswers(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "faultline")
	command := exec.Command("go", "build", "-o", binary, "..")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build faultline: %v\n%s", err, output)
	}
	help, err := exec.Command(binary, "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(help), "usage: faultline ") {
		t.Fatalf("help: %v\n%s", err, help)
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
			if m.Status != "KILLED" && m.Status != "NOT VIABLE" && !strings.HasPrefix(m.Reproduce, "faultline --only") {
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
	for _, want := range []string{"meaning: the tests did not detect this mutation", "//faultline:equivalent", "meaning: this mutation made the tests hang", "duration:", "next actions:", "not viable: 1", "allowlisted (still run", "slowest mutants:", strings.Repeat("-", 80)} {
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
	t.Run("recursive package pattern", func(t *testing.T) {
		data, code := execute(t, binary, fixture, "--json", "--concurrency", "2", "./...")
		if code != 2 {
			t.Fatalf("recursive pattern exit = %d, want 2", code)
		}
		assertKnownAnswers(t, data)
		human, stderr, code := executeCombined(t, binary, fixture, "--concurrency", "2", "./...")
		if code != 2 {
			t.Fatalf("human recursive pattern exit = %d, want 2", code)
		}
		for _, want := range []string{"untested packages: 1", "./notests", "baseline failed: 1", "./broken", "run is incomplete"} {
			if !strings.Contains(human, want) {
				t.Errorf("recursive report does not contain %q\n%s", want, human)
			}
		}
		if !strings.Contains(stderr, "rough estimate:") {
			t.Errorf("stderr does not contain rough estimate:\n%s", stderr)
		}
	})

	t.Run("bare package directory", func(t *testing.T) {
		human, _, code := executeCombined(t, binary, fixture, "--only", regexp.QuoteMeta(killedKey), ".", "broken")
		if code != 2 {
			t.Fatalf("bare package directory exit = %d, want 2", code)
		}
		if !strings.Contains(human, "baseline failed: 1") || !strings.Contains(human, "./broken") {
			t.Fatalf("bare package directory report missing baseline failure:\n%s", human)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(binary, "--concurrency", "1")
		cmd.Dir = fixture
		cmd.Env = append(os.Environ(), "FAULTLINE_TEST_TIMEOUT_SECONDS=5")
		pipe, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout = &stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		phase := make(chan struct{})
		scannerDone := make(chan struct{})
		go func() {
			defer close(scannerDone)
			scanner := bufio.NewScanner(pipe)
			for scanner.Scan() {
				stderr.WriteString(scanner.Text() + "\n")
				if strings.Contains(scanner.Text(), "testing 28 mutants") {
					select {
					case <-phase:
					default:
						close(phase)
					}
				}
			}
		}()
		select {
		case <-phase:
		case <-time.After(10 * time.Second):
			t.Fatal("testing phase did not appear")
		}
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		err = cmd.Wait()
		<-scannerDone
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("interrupt exit = %v, want 2; stderr:\n%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "interrupted:") {
			t.Fatalf("interrupted report missing summary:\n%s", stdout.String())
		}
	})
}

func assertKnownAnswers(t *testing.T, data report) {
	t.Helper()
	want := map[string]string{killedKey: "KILLED", survivedKey: "SURVIVED", timedOutKey: "TIMED OUT", allowlistedKey: "SURVIVED", notViableKey: "NOT VIABLE"}
	for _, mutant := range data.Mutants {
		if status, ok := want[mutant.Key]; ok && mutant.Status == status {
			delete(want, mutant.Key)
		}
	}
	if len(want) > 0 {
		t.Errorf("recursive report missing known answers: %v", want)
	}
}

func executeCombined(t *testing.T, binary, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FAULTLINE_TEST_TIMEOUT_SECONDS=5")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

func executeOutput(t *testing.T, binary, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FAULTLINE_TEST_TIMEOUT_SECONDS=5")
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
	cmd.Env = append(os.Environ(), "FAULTLINE_TEST_TIMEOUT_SECONDS=5")
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
