package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/chronark/loki/internal/mutate"
	"github.com/chronark/loki/internal/report"
	"github.com/chronark/loki/internal/run"
)

func main() { os.Exit(runCLI(os.Args[1:])) }

func runCLI(arguments []string) int {
	flags := flag.NewFlagSet("loki", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: loki [--json] [--only <regexp>] [--concurrency N] [package...]")
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
	for _, pkg := range packages {
		if strings.Contains(pkg, "...") {
			fmt.Fprintln(os.Stderr, "package patterns containing ... are not supported; list packages explicitly")
			return 2
		}
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
	writer.Phase("running baseline tests (go test %s)...", strings.Join(packages, " "))
	baseline, err := run.Baseline(root, packages)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	timeout := run.Timeout(baseline)
	writer.Phase("baseline passed in %s; mutant timeout %s", baseline, timeout)
	mutants, notes, err := mutate.Enumerate(root, packages, *only)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for _, note := range notes {
		writer.Phase("not viable: %s: %s", note.Key, note.Message)
	}
	workers := *concurrency
	if workers == 0 {
		workers = run.AutoConcurrency(runtime.NumCPU(), run.TotalMemory())
	}
	writer.Phase("testing %d mutants with concurrency %d...", len(mutants), workers)
	results, err := run.Execute(root, mutants, workers, timeout, func(done int, result run.Result) { writer.Progress(done, len(mutants), result) })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	findings, err := writer.Final(packages, workers, timeout, results)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if findings {
		return 1
	}
	return 0
}
