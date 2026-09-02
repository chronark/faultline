// Package run isolates each mutant in a Go overlay and classifies its observable result.
package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chronark/placebo/internal/mutate"
)

// Status distinguishes useful test detection from infrastructure and compile outcomes.
type Status string

const (
	Killed    Status = "KILLED"
	Survived  Status = "SURVIVED"
	TimedOut  Status = "TIMED OUT"
	OOM       Status = "OOM"
	NotViable Status = "NOT VIABLE"
)

// Result keeps execution evidence attached to the mutant for deterministic reporting.
type Result struct {
	Mutant   mutate.Mutant
	Status   Status
	Duration time.Duration
	Note     string
}

// BaselineStatus determines whether mutants from a package can produce meaningful results.
type BaselineStatus string

const (
	BaselineOK       BaselineStatus = "ok"
	BaselineUntested BaselineStatus = "untested"
	BaselineFailed   BaselineStatus = "failed"
)

// Package keeps go-list identity and baseline evidence together.
type Package struct {
	Dir        string
	ImportPath string
	Baseline   time.Duration
	Status     BaselineStatus
	Output     string
}

// ListPackages expands Go package patterns and returns module-relative directories.
func ListPackages(ctx context.Context, root string, arguments []string) ([]Package, error) {
	arguments = rewriteLocalPackageArguments(arguments, os.Stat)
	args := append([]string{"list", "-f", "{{.ImportPath}}\t{{.Dir}}"}, arguments...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("go list failed: %w\n%s", err, stderr.String())
	}
	seen := make(map[string]bool)
	var packages []Package
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		importPath, directory, found := strings.Cut(scanner.Text(), "\t")
		if !found {
			return nil, fmt.Errorf("parse go list output %q", scanner.Text())
		}
		relative, relErr := filepath.Rel(root, directory)
		if relErr != nil {
			return nil, fmt.Errorf("make package directory relative: %w", relErr)
		}
		dir := "."
		if relative != "." {
			dir = "./" + filepath.ToSlash(relative)
		}
		if !seen[dir] {
			seen[dir] = true
			packages = append(packages, Package{Dir: dir, ImportPath: importPath})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read go list output: %w", err)
	}
	return packages, nil
}

func rewriteLocalPackageArguments(arguments []string, stat func(string) (os.FileInfo, error)) []string {
	rewritten := append([]string(nil), arguments...)
	for i, argument := range rewritten {
		if argument == "" || strings.HasPrefix(argument, ".") || strings.HasPrefix(argument, "/") || hasWindowsDrive(argument) {
			continue
		}
		directory := strings.TrimSuffix(argument, "/...")
		info, err := stat(directory)
		if err != nil || !info.IsDir() {
			continue
		}
		// go list treats bare paths as import paths. Only existing directories are rewritten so real import paths keep working.
		rewritten[i] = "./" + argument
	}
	return rewritten
}

func hasWindowsDrive(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	letter := path[0]
	return letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z'
}

// Baseline runs all packages once while retaining package-specific timing and failures.
func Baseline(ctx context.Context, root string, packages []Package) ([]Package, error) {
	args := []string{"test", "-json", "-count=1"}
	for _, pkg := range packages {
		args = append(args, pkg.Dir)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=1536MiB")
	output, commandErr := cmd.CombinedOutput()
	parsed, err := ParseBaseline(bytes.NewReader(output), packages)
	if err != nil {
		return nil, err
	}
	if commandErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(commandErr, &exitErr) {
			return nil, fmt.Errorf("run baseline: %w", commandErr)
		}
	}
	return parsed, nil
}

// ParseBaseline decodes test2json output without treating toolchain diagnostics as JSON errors.
func ParseBaseline(reader io.Reader, packages []Package) ([]Package, error) {
	result := append([]Package(nil), packages...)
	byImportPath := make(map[string]int, len(result))
	outputs := make([][]string, len(result))
	for i := range result {
		byImportPath[result[i].ImportPath] = i
	}
	type event struct {
		Action, Package, Test, Output string
		Elapsed                       float64
	}
	var synthetic []string
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		var current event
		if err := json.Unmarshal(scanner.Bytes(), &current); err != nil {
			synthetic = append(synthetic, scanner.Text())
			continue
		}
		index, known := byImportPath[current.Package]
		if !known || current.Test != "" {
			continue
		}
		if current.Action == "output" {
			outputs[index] = append(outputs[index], strings.TrimSuffix(current.Output, "\n"))
		}
		switch current.Action {
		case "pass":
			result[index].Status = BaselineOK
			result[index].Baseline = time.Duration(current.Elapsed * float64(time.Second))
		case "skip":
			result[index].Status = BaselineUntested
		case "fail":
			result[index].Status = BaselineFailed
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read baseline output: %w", err)
	}
	attachedSynthetic := false
	for i := range result {
		if result[i].Status == BaselineFailed {
			lines := append(outputs[i], synthetic...)
			result[i].Output = outputTail(lines, 40)
			attachedSynthetic = attachedSynthetic || len(synthetic) > 0
		}
	}
	if len(synthetic) > 0 && !attachedSynthetic {
		result = append(result, Package{Dir: "<go test>", ImportPath: "<go test>", Status: BaselineFailed, Output: outputTail(synthetic, 40)})
	}
	return result, nil
}

// Timeout gives slow suites proportional headroom while bounding hangs in fast suites.
func Timeout(baseline time.Duration) time.Duration {
	if override := os.Getenv("PLACEBO_TEST_TIMEOUT_SECONDS"); override != "" {
		seconds, err := strconv.Atoi(override)
		if err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return max(30*time.Second, 5*baseline)
}

// Mutant uses a private directory so parallel workers cannot collide.
func Mutant(ctx context.Context, root, directory string, current mutate.Mutant, timeout time.Duration) Result {
	started := time.Now()
	finish := func(status Status, note string) Result {
		return Result{Mutant: current, Status: status, Duration: time.Since(started), Note: note}
	}
	if current.Reason() != "" {
		return finish(NotViable, current.Reason())
	}
	mutatedPath := filepath.Join(directory, "mutated.go")
	if err := os.WriteFile(mutatedPath, current.Source(), 0o600); err != nil {
		return finish(NotViable, "write mutated source: "+err.Error())
	}
	overlayPath := filepath.Join(directory, "overlay.json")
	overlay, err := json.Marshal(struct {
		Replace map[string]string `json:"Replace"`
	}{map[string]string{current.Path(): mutatedPath}})
	if err != nil {
		return finish(NotViable, "encode overlay: "+err.Error())
	}
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		return finish(NotViable, "write overlay: "+err.Error())
	}
	_, buildErr, buildContextErr := runGo(ctx, root, []string{"build", "-overlay", overlayPath, current.PackagePath()}, timeout)
	if buildErr != nil {
		note := ""
		if errors.Is(buildContextErr, context.DeadlineExceeded) {
			note = "build timed out"
		}
		return finish(NotViable, note)
	}
	output, testErr, contextErr := runGo(ctx, root, []string{"test", "-overlay", overlayPath, "-count=1", "-timeout", timeout.String(), current.PackagePath()}, timeout+15*time.Second)
	return finish(classify(testErr, output, contextErr), "")
}

// Execute preserves enumeration order and returns the number left unscheduled after cancellation.
func Execute(ctx context.Context, root string, mutants []mutate.Mutant, workers int, timeouts map[string]time.Duration, progress func(int, Result)) ([]Result, int, error) {
	results := make([]Result, len(mutants))
	completedResults := make([]bool, len(mutants))
	jobs := make(chan int)
	done := make(chan int, len(mutants))
	errs := make(chan error, workers)
	go func() {
		defer close(jobs)
		for i := range mutants {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			dir, err := os.MkdirTemp("", "placebo-*")
			if err != nil {
				errs <- err
				return
			}
			defer os.RemoveAll(dir)
			for i := range jobs {
				results[i] = Mutant(ctx, root, dir, mutants[i], timeouts[mutants[i].PackagePath()])
				if ctx.Err() != nil {
					return
				}
				done <- i
			}
		}()
	}
	go func() { group.Wait(); close(done); close(errs) }()
	completed := 0
	for i := range done {
		completed++
		completedResults[i] = true
		if progress != nil {
			progress(completed, results[i])
		}
	}
	for err := range errs {
		if err != nil {
			return nil, len(mutants) - completed, fmt.Errorf("execute mutants: %w", err)
		}
	}
	collected := make([]Result, 0, completed)
	for i, ok := range completedResults {
		if ok {
			collected = append(collected, results[i])
		}
	}
	return collected, len(mutants) - completed, nil
}

// AutoConcurrency prevents CPU saturation and budgets two GiB for each Go process.
func AutoConcurrency(cpus int, memoryBytes uint64) int {
	workers := cpus / 2
	if memoryBytes > 0 {
		workers = min(workers, int(memoryBytes/(2<<30)))
	}
	return max(1, workers)
}

// TotalMemory uses the tighter host or cgroup limit so automatic concurrency works in containers.
func TotalMemory() uint64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	var total uint64
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			var kib uint64
			if _, err := fmt.Sscanf(line, "MemTotal: %d kB", &kib); err == nil {
				total = kib << 10
				break
			}
		}
	}
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		if limit, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil && (total == 0 || limit < total) {
			total = limit
		}
	}
	return total
}

func classify(commandErr error, output []byte, contextErr error) Status {
	if commandErr == nil {
		return Survived
	}
	if errors.Is(contextErr, context.DeadlineExceeded) || strings.Contains(string(output), "panic: test timed out") {
		return TimedOut
	}
	var exitErr *exec.ExitError
	if errors.As(commandErr, &exitErr) {
		if wait, ok := exitErr.Sys().(syscall.WaitStatus); ok && wait.Signaled() && wait.Signal() == syscall.SIGKILL {
			return OOM
		}
	}
	return Killed
}

func runGo(parent context.Context, root string, arguments []string, timeout time.Duration) ([]byte, error, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", arguments...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=1536MiB")
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return err
		}
	}
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.CombinedOutput()
	return output, err, ctx.Err()
}

func outputTail(lines []string, count int) string {
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}
