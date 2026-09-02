package report

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chronark/placebo/internal/mutate"
	"github.com/chronark/placebo/internal/run"
)

// Writer keeps presentation policy out of mutation discovery and execution.
type Writer struct {
	stdout, stderr         io.Writer
	jsonMode, showProgress bool
}

// Summary makes aggregate outcomes stable for CI and coding-agent consumers.
type Summary struct {
	Total              int     `json:"total"`
	Killed             int     `json:"killed"`
	TimedOut           int     `json:"timedOut"`
	OOM                int     `json:"oom"`
	Survived           int     `json:"survived"`
	NotViable          int     `json:"notViable"`
	Allowlisted        int     `json:"allowlisted"`
	UntestedPackages   int     `json:"untestedPackages,omitempty"`
	BaselineFailed     int     `json:"baselineFailed,omitempty"`
	InterruptedMutants int     `json:"interrupted,omitempty"`
	Score              float64 `json:"score"`
}
type mutantRecord struct {
	Status          string `json:"status"`
	File            string `json:"file"`
	Line            int    `json:"line"`
	Column          int    `json:"column"`
	Function        string `json:"function"`
	Mutation        string `json:"mutation"`
	Original        string `json:"original"`
	Mutated         string `json:"mutated,omitempty"`
	Key             string `json:"key"`
	Allowlisted     bool   `json:"allowlisted"`
	AllowlistReason string `json:"allowlistReason,omitempty"`
	Diff            string `json:"diff,omitempty"`
	Context         string `json:"context,omitempty"`
	Reproduce       string `json:"reproduce,omitempty"`
	DurationMs      int64  `json:"durationMs"`
	Reason          string `json:"reason,omitempty"`
}
type jsonReport struct {
	Packages         []packageRecord        `json:"packages"`
	UntestedPackages []string               `json:"untestedPackages"`
	BaselineFailed   []baselineFailedRecord `json:"baselineFailed"`
	Interrupted      bool                   `json:"interrupted"`
	Concurrency      int                    `json:"concurrency"`
	Summary          Summary                `json:"summary"`
	Mutants          []mutantRecord         `json:"mutants"`
}
type packageRecord struct {
	Package    string `json:"package"`
	BaselineMs int64  `json:"baselineMs"`
	TimeoutMs  int64  `json:"timeoutMs"`
	Mutants    int    `json:"mutants"`
}
type baselineFailedRecord struct {
	Package string `json:"package"`
	Output  string `json:"output"`
}

// New prevents terminal-only diagnostics from leaking into redirected output.
func New(stdout, stderr io.Writer, jsonMode, showProgress bool) *Writer {
	return &Writer{stdout: stdout, stderr: stderr, jsonMode: jsonMode, showProgress: showProgress}
}

// Phase gives interactive runs useful milestones without changing the final report stream.
func (w *Writer) Phase(format string, args ...any) { fmt.Fprintf(w.stderr, format+"\n", args...) }

// Progress avoids noisy logs when stderr is redirected.
func (w *Writer) Progress(done, total int, result run.Result) {
	if w.showProgress {
		fmt.Fprintf(w.stderr, "[%3d/%d] %-10s %s  %s\n", done, total, result.Status, result.Mutant.Position(), result.Mutant.Description())
	}
}

// Final emits one complete report and returns whether unallowlisted findings remain.
func (w *Writer) Final(packages []run.Package, workers int, results []run.Result, interrupted int) (bool, error) {
	data := build(packages, workers, results, interrupted)
	if w.jsonMode {
		encoder := json.NewEncoder(w.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(data); err != nil {
			return false, err
		}
	} else {
		human(w.stdout, data)
	}
	for _, record := range data.Mutants {
		if !record.Allowlisted && (record.Status == string(run.Survived) || record.Status == string(run.TimedOut) || record.Status == string(run.OOM)) {
			return true, nil
		}
	}
	return false, nil
}

func build(packages []run.Package, workers int, results []run.Result, interrupted int) jsonReport {
	data := jsonReport{Packages: make([]packageRecord, 0, len(packages)), UntestedPackages: []string{}, BaselineFailed: []baselineFailedRecord{}, Interrupted: interrupted > 0, Concurrency: workers, Mutants: make([]mutantRecord, 0, len(results))}
	mutantsByPackage := make(map[string]int)
	for _, result := range results {
		mutantsByPackage[result.Mutant.PackagePath()]++
	}
	for _, pkg := range packages {
		switch pkg.Status {
		case run.BaselineOK:
			data.Packages = append(data.Packages, packageRecord{Package: pkg.Dir, BaselineMs: pkg.Baseline.Milliseconds(), TimeoutMs: run.Timeout(pkg.Baseline).Milliseconds(), Mutants: mutantsByPackage[pkg.Dir]})
		case run.BaselineUntested:
			data.UntestedPackages = append(data.UntestedPackages, pkg.Dir)
		case run.BaselineFailed:
			data.BaselineFailed = append(data.BaselineFailed, baselineFailedRecord{Package: pkg.Dir, Output: pkg.Output})
		}
	}
	for _, result := range results {
		m := result.Mutant
		allowed := m.AllowlistReason() != ""
		r := mutantRecord{Status: string(result.Status), File: m.RelativePath(), Line: m.Line(), Column: m.Column(), Function: m.Function(), Mutation: m.Description(), Original: m.Original(), Key: m.Key(), Allowlisted: allowed, AllowlistReason: m.AllowlistReason(), DurationMs: result.Duration.Milliseconds(), Reason: result.Note}
		switch result.Status {
		case run.Killed:
			data.Summary.Killed++
		case run.TimedOut:
			data.Summary.TimedOut++
		case run.OOM:
			data.Summary.OOM++
		case run.Survived:
			data.Summary.Survived++
		case run.NotViable:
			data.Summary.NotViable++
		}
		if allowed {
			data.Summary.Allowlisted++
		}
		if result.Status != run.Killed && result.Status != run.NotViable || allowed {
			r.Mutated = m.Mutated()
			r.Diff = mutationDiff(m.OriginalSource(), m.Source(), m.Original(), m.Mutated())
			r.Context = sourceContext(m)
			r.Reproduce = reproduce(packages, m.Key())
		}
		data.Mutants = append(data.Mutants, r)
	}
	sort.SliceStable(data.Mutants, func(i, j int) bool {
		if data.Mutants[i].File != data.Mutants[j].File {
			return data.Mutants[i].File < data.Mutants[j].File
		}
		return data.Mutants[i].Line < data.Mutants[j].Line
	})
	data.Summary.Total = len(results)
	data.Summary.UntestedPackages = len(data.UntestedPackages)
	data.Summary.BaselineFailed = len(data.BaselineFailed)
	data.Summary.InterruptedMutants = interrupted
	viable := data.Summary.Total - data.Summary.NotViable
	if viable > 0 {
		data.Summary.Score = float64(data.Summary.Killed+data.Summary.TimedOut+data.Summary.OOM) / float64(viable)
	}
	return data
}

func human(w io.Writer, data jsonReport) {
	for _, r := range data.Mutants {
		if !r.Allowlisted && (r.Status == string(run.Survived) || r.Status == string(run.TimedOut) || r.Status == string(run.OOM)) {
			fmt.Fprintln(w, strings.Repeat("-", 80))
			start, end := contextBounds(r.Context)
			fmt.Fprintf(w, "%s: %s:%d:%d  %s\nfunction: %s\nkey: %s\n\n%s\n\ncontext (%s:%d-%d):\n%s\nreproduce: %s\n", r.Status, r.File, r.Line, r.Column, r.Mutation, r.Function, r.Key, r.Diff, r.File, start, end, r.Context, r.Reproduce)
			if r.Status == string(run.TimedOut) || r.Status == string(run.OOM) {
				fmt.Fprintf(w, "duration: %s\n", (time.Duration(r.DurationMs) * time.Millisecond).Round(time.Millisecond))
			}
			switch r.Status {
			case string(run.OOM):
				fmt.Fprintln(w, "meaning: this mutation made the tests allocate until the kernel killed the process. The code path has no bound on memory growth. Add an explicit size limit so a broken guard fails fast.")
			case string(run.TimedOut):
				fmt.Fprintln(w, "meaning: this mutation made the tests hang. Add an explicit iteration limit or progress check so a broken guard fails fast.")
			default:
				fmt.Fprintln(w, "meaning: this test is a placebo: it passes whether the code is right or wrong. Add a test that fails under the mutated line, or add a justified //placebo:equivalent marker.")
			}
		}
	}
	if len(data.UntestedPackages) > 0 {
		fmt.Fprintf(w, "untested packages: %d\n", len(data.UntestedPackages))
		for _, pkg := range data.UntestedPackages {
			fmt.Fprintf(w, "  %s\n", pkg)
		}
	}
	if len(data.BaselineFailed) > 0 {
		fmt.Fprintf(w, "baseline failed: %d\n", len(data.BaselineFailed))
		for _, pkg := range data.BaselineFailed {
			fmt.Fprintf(w, "  %s\n", pkg.Package)
			for _, line := range strings.Split(pkg.Output, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}
	fmt.Fprintf(w, "not viable: %d\n", data.Summary.NotViable)
	fmt.Fprintf(w, "\nSummary:\n  total: %d\n  killed: %d\n  timed out: %d\n  oom: %d\n  survived: %d\n  allowlisted: %d\n  score: %.2f%% (killed + timed out + oom) / (total - not viable)\n", data.Summary.Total, data.Summary.Killed, data.Summary.TimedOut, data.Summary.OOM, data.Summary.Survived, data.Summary.Allowlisted, 100*data.Summary.Score)
	if data.Summary.UntestedPackages > 0 {
		fmt.Fprintf(w, "  untested packages: %d\n", data.Summary.UntestedPackages)
	}
	if data.Summary.BaselineFailed > 0 {
		fmt.Fprintf(w, "  baseline failed: %d\n", data.Summary.BaselineFailed)
	}
	if data.Interrupted {
		fmt.Fprintf(w, "  interrupted: %d mutants not tested\n", data.Summary.InterruptedMutants)
	}
	if data.Summary.Allowlisted > 0 {
		fmt.Fprintln(w, "\nallowlisted (still run, not counted as findings):")
		for _, r := range data.Mutants {
			if r.Allowlisted {
				status := r.Status
				if status == string(run.Killed) {
					status = "KILLED (marker is stale, remove it)"
				}
				fmt.Fprintf(w, "%s:%d:%d  %s  %s: %s\n", r.File, r.Line, r.Column, r.Key, status, r.AllowlistReason)
			}
		}
	}
	if len(data.Mutants) >= 2 {
		slowest := append([]mutantRecord(nil), data.Mutants...)
		sort.SliceStable(slowest, func(i, j int) bool { return slowest[i].DurationMs > slowest[j].DurationMs })
		fmt.Fprintln(w, "\nslowest mutants:")
		for _, r := range slowest[:min(5, len(slowest))] {
			fmt.Fprintf(w, "%s  %s  %s:%d:%d  %s\n", (time.Duration(r.DurationMs) * time.Millisecond).Round(time.Millisecond), r.Status, r.File, r.Line, r.Column, r.Mutation)
		}
	}
	if data.Summary.TimedOut > 0 {
		fmt.Fprintln(w, "timed out: reproduce each timeout and add an explicit iteration limit or progress check.")
	}
	fmt.Fprintln(w, "\nnext actions:")
	fmt.Fprintln(w, "- Add a test for each SURVIVED mutant.")
	fmt.Fprintln(w, "- Add an explicit bound for each TIMED OUT or OOM mutant.")
	fmt.Fprintln(w, "- Add //placebo:equivalent <reason> only when a mutant is observably equivalent.")
	if len(data.BaselineFailed) > 0 {
		fmt.Fprintln(w, "- Fix or exclude packages whose baseline failed; this run is incomplete.")
	}
}

func contextBounds(context string) (int, int) {
	lines := strings.Split(context, "\n")
	var start, end int
	if len(lines) > 0 {
		fmt.Sscanf(lines[0], "%d", &start)
		fmt.Sscanf(lines[len(lines)-1], "%d", &end)
	}
	return start, end
}

func reproduce(packages []run.Package, key string) string {
	pattern := strings.ReplaceAll(regexp.QuoteMeta(key), "'", "'\\''")
	dirs := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if pkg.Status == run.BaselineOK {
			dirs = append(dirs, pkg.Dir)
		}
	}
	return fmt.Sprintf("placebo --only '%s' %s", pattern, strings.Join(dirs, " "))
}
func splitLines(source []byte) []string {
	return strings.Split(strings.TrimSuffix(string(source), "\n"), "\n")
}
func bounds(m mutate.Mutant) (int, int) {
	lines := splitLines(m.OriginalSource())
	start := max(1, m.Line()-8)
	if m.FunctionLine() > 0 && m.FunctionLine() < start {
		start = m.FunctionLine()
	}
	return start, min(len(lines), m.Line()+8)
}
func sourceContext(m mutate.Mutant) string {
	lines := splitLines(m.OriginalSource())
	start, end := bounds(m)
	width := len(fmt.Sprint(end))
	var b strings.Builder
	for line := start; line <= end; line++ {
		fmt.Fprintf(&b, "%*d\t%s\n", width, line, lines[line-1])
	}
	return strings.TrimSuffix(b.String(), "\n")
}
func mutationDiff(original, changed []byte, originalExpr, changedExpr string) string {
	before, after := splitLines(original), splitLines(changed)
	start := 0
	for start < len(before) && start < len(after) && before[start] == after[start] {
		start++
	}
	be, ae := len(before), len(after)
	for be > start && ae > start && before[be-1] == after[ae-1] {
		be--
		ae--
	}
	if be-start+ae-start > 10 {
		return "original: " + originalExpr + "\nmutated: " + changedExpr
	}
	var b strings.Builder
	for _, line := range before[start:be] {
		fmt.Fprintf(&b, "-%s\n", line)
	}
	for _, line := range after[start:ae] {
		fmt.Fprintf(&b, "+%s\n", line)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
