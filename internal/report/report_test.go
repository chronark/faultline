package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chronark/faultline/internal/run"
)

func TestMutationDiffExtractsChangedRegion(t *testing.T) {
	original := []byte("package fixture\n\nfunc covers(n int) bool {\n\treturn n > 0\n}\n")
	mutated := []byte("package fixture\n\nfunc covers(n int) bool {\n\treturn n >= 0\n}\n")
	want := "-\treturn n > 0\n+\treturn n >= 0"
	if got := mutationDiff(original, mutated, "n > 0", "n >= 0"); got != want {
		t.Fatalf("mutationDiff() = %q, want %q", got, want)
	}
}

func TestHumanReportIncludesActionableFindingsAndFooter(t *testing.T) {
	records := []mutantRecord{
		{Status: string(run.Survived), File: "fixture.go", Line: 4, Column: 11, Function: "covers", Mutation: "> -> >=", Key: "fixture.go|covers|> -> >=|n > 0", Diff: "-\treturn n > 0\n+\treturn n >= 0", Context: "3\tfunc covers(n int) bool {\n4\t\treturn n > 0\n5\t}", Reproduce: "faultline --only 'key' .", DurationMs: 42},
		{Status: string(run.TimedOut), File: "fixture.go", Line: 8, Column: 2, Mutation: "++ -> --", DurationMs: 5000, Diff: "-i++\n+i--", Context: "8\ti++", Reproduce: "faultline --only 'timeout' ."},
		{Status: string(run.OOM), File: "fixture.go", Line: 9, Column: 2, Mutation: "x", DurationMs: 6000, Diff: "-x\n+y", Context: "9\tx", Reproduce: "faultline --only 'oom' ."},
		{Status: string(run.Killed), File: "fixture.go", Line: 10, Column: 2, Key: "stale", Allowlisted: true, AllowlistReason: "same behavior", DurationMs: 10},
	}
	data := jsonReport{
		UntestedPackages: []string{"./notests"},
		BaselineFailed:   []baselineFailedRecord{{Package: "./broken", Output: "--- FAIL: TestBroken"}},
		Interrupted:      true,
		Summary:          Summary{Total: 5, Killed: 1, TimedOut: 1, OOM: 1, Survived: 1, NotViable: 1, Allowlisted: 1, UntestedPackages: 1, BaselineFailed: 1, InterruptedMutants: 3, Score: .75},
		Mutants:          records,
	}
	var output bytes.Buffer
	human(&output, data)
	for _, want := range []string{strings.Repeat("-", 80), "context (fixture.go:3-5):", "reproduce: faultline --only", "meaning: the tests did not detect this mutation", "duration: 5s", "meaning: this mutation made the tests hang", "meaning: this mutation made the tests allocate", "untested packages: 1\n  ./notests", "baseline failed: 1\n  ./broken", "interrupted: 3 mutants not tested", "run is incomplete", "not viable: 1", "allowlisted (still run, not counted as findings):", "marker is stale", "score: 75.00% (killed + timed out + oom) / (total - not viable)", "slowest mutants:", "next actions:"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("report does not contain %q\n%s", want, output.String())
		}
	}
}

func TestHumanReportOmitsSlowestForOneMutant(t *testing.T) {
	var output bytes.Buffer
	human(&output, jsonReport{Mutants: []mutantRecord{{Status: string(run.Killed)}}})
	if strings.Contains(output.String(), "slowest mutants:") {
		t.Fatalf("one-mutant report contains slowest section: %s", output.String())
	}
}

func TestJSONReportContainsStableFields(t *testing.T) {
	data := jsonReport{Packages: []packageRecord{{Package: ".", BaselineMs: 100, TimeoutMs: 30000, Mutants: 1}}, UntestedPackages: []string{}, BaselineFailed: []baselineFailedRecord{}, Concurrency: 2, Summary: Summary{}, Mutants: []mutantRecord{{Status: "SURVIVED", File: "x.go", Line: 1, Column: 2, Function: "f", Mutation: "x", Original: "x", Mutated: "y", Key: "key", Allowlisted: true, AllowlistReason: "reason", Diff: "diff", Context: "context", Reproduce: "command", DurationMs: 1, Reason: "note"}}}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"packages", "untestedPackages", "baselineFailed", "interrupted", "concurrency", "summary", "mutants"} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("JSON does not contain %q", field)
		}
	}
	mutant := decoded["mutants"].([]any)[0].(map[string]any)
	for _, field := range []string{"status", "file", "line", "column", "function", "mutation", "original", "mutated", "key", "allowlisted", "allowlistReason", "diff", "context", "reproduce", "durationMs", "reason"} {
		if _, ok := mutant[field]; !ok {
			t.Errorf("mutant JSON does not contain %q", field)
		}
	}
}
