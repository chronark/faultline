package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/chronark/placebo/internal/mutate"
	"github.com/chronark/placebo/internal/report"
	"github.com/chronark/placebo/internal/run"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runCLI(ctx, os.Args[1:]))
}

// runCLI returns 0 for success, 1 for findings, and 2 for incomplete runs or errors.
func runCLI(ctx context.Context, arguments []string) int {
	flags := flag.NewFlagSet("placebo", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: placebo [--json] [--only <regexp>] [--concurrency N] [package...]")
		flags.PrintDefaults()
	}
	jsonMode := flags.Bool("json", false, "write the report as JSON")
	only := flags.String("only", "", "regular expression selecting mutant keys")
	concurrency := flags.Int("concurrency", 0, "mutants evaluated concurrently (0 = auto)")
	showVersion := flags.Bool("version", false, "print the version")
	if err := flags.Parse(arguments); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(os.Stdout, version)
		return 0
	}
	packages := flags.Args()
	if len(packages) == 0 {
		packages = []string{"."}
	}
	if *concurrency < 0 {
		fmt.Fprintln(os.Stderr, "--concurrency must be non-negative")
		return 2
	}
	if _, err := regexp.Compile(*only); err != nil {
		fmt.Fprintf(os.Stderr, "compile --only expression: %v\n", err)
		return 2
	}
	info, err := os.Stderr.Stat()
	writer := report.New(os.Stdout, os.Stderr, *jsonMode, err == nil && info.Mode()&os.ModeCharDevice != 0)
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	listed, err := run.ListPackages(ctx, root, packages)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	writer.Phase("running baseline tests (go test %s)...", strings.Join(packages, " "))
	baselineStarted := time.Now()
	packageResults, err := run.Baseline(ctx, root, listed)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var okPackages []string
	timeouts := make(map[string]time.Duration)
	var timeoutMin, timeoutMax time.Duration
	untested, failed := 0, 0
	for _, pkg := range packageResults {
		switch pkg.Status {
		case run.BaselineOK:
			okPackages = append(okPackages, pkg.Dir)
			timeout := run.Timeout(pkg.Baseline)
			timeouts[pkg.Dir] = timeout
			if timeoutMin == 0 || timeout < timeoutMin {
				timeoutMin = timeout
			}
			if timeout > timeoutMax {
				timeoutMax = timeout
			}
		case run.BaselineUntested:
			untested++
		case run.BaselineFailed:
			failed++
		}
	}
	if len(okPackages) == 0 {
		fmt.Fprintf(os.Stderr, "no packages have a passing baseline (%d without tests, %d failed)\n", untested, failed)
		return 2
	}
	timeoutRange := timeoutMin.String()
	if timeoutMin != timeoutMax {
		timeoutRange += ".." + timeoutMax.String()
	}
	writer.Phase("baseline passed for %d packages in %s; mutant timeout per package %s (5x baseline, min 30s)", len(okPackages), time.Since(baselineStarted).Round(100*time.Millisecond), timeoutRange)
	if untested > 0 {
		writer.Phase("skipping %d packages without tests", untested)
	}
	if failed > 0 {
		writer.Phase("skipping %d packages whose baseline failed", failed)
	}
	mutants, notes, err := mutate.Enumerate(root, okPackages, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	writer.Phase("enumerated %d mutants across %d packages", len(mutants), len(okPackages))
	for _, note := range notes {
		writer.Phase("not viable: %s: %s", note.Key, note.Message)
	}
	if *only != "" {
		pattern := regexp.MustCompile(*only)
		filtered := mutants[:0]
		for _, mutant := range mutants {
			if pattern.MatchString(mutant.Key()) {
				filtered = append(filtered, mutant)
			}
		}
		mutants = filtered
		writer.Phase("filtered to %d mutants with --only %q", len(mutants), *only)
	}
	workers := *concurrency
	if workers == 0 {
		workers = run.AutoConcurrency(runtime.NumCPU(), run.TotalMemory())
	}
	estimate := roughEstimate(mutants, packageResults, workers)
	writer.Phase("rough estimate: ~%s at concurrency %d", estimate, workers)
	if *concurrency == 0 {
		writer.Phase("testing %d mutants with concurrency %d (auto: %d cpus, %d GiB memory)...", len(mutants), workers, runtime.NumCPU(), run.TotalMemory()>>30)
	} else {
		writer.Phase("testing %d mutants with concurrency %d (flag)...", len(mutants), workers)
	}
	executionStarted := time.Now()
	results, interrupted, err := run.Execute(ctx, root, mutants, workers, timeouts, func(done int, result run.Result) { writer.Progress(done, len(mutants), result) })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	writer.Phase("finished %d mutants in %s", len(results), time.Since(executionStarted).Round(100*time.Millisecond))
	findings, err := writer.Final(packageResults, workers, results, interrupted)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if failed > 0 || interrupted > 0 {
		return 2
	}
	if findings {
		return 1
	}
	return 0
}

func roughEstimate(mutants []mutate.Mutant, packages []run.Package, workers int) time.Duration {
	baselineByDir := make(map[string]time.Duration, len(packages))
	for _, pkg := range packages {
		baselineByDir[pkg.Dir] = pkg.Baseline
	}
	var estimate time.Duration
	for _, mutant := range mutants {
		estimate += baselineByDir[mutant.PackagePath()] + 2*time.Second
	}
	estimate /= time.Duration(workers)
	if estimate < 2*time.Minute {
		return estimate.Round(time.Second)
	}
	return estimate.Round(time.Minute)
}
