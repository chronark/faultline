// Package run isolates each mutant in a Go overlay and classifies its observable result.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Baseline prevents meaningless mutation scores when the unmodified package already fails.
func Baseline(root string, packages []string) (time.Duration, error) {
	started := time.Now()
	args := append([]string{"test", "-count=1"}, packages...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=1536MiB")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("baseline tests failed; mutation results would be meaningless:\n%s", output)
	}
	return time.Since(started), nil
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
func Mutant(root, directory string, current mutate.Mutant, timeout time.Duration) Result {
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
	_, buildErr, buildContextErr := runGo(root, []string{"build", "-overlay", overlayPath, current.PackagePath()}, timeout)
	if buildErr != nil {
		note := ""
		if errors.Is(buildContextErr, context.DeadlineExceeded) {
			note = "build timed out"
		}
		return finish(NotViable, note)
	}
	output, testErr, contextErr := runGo(root, []string{"test", "-overlay", overlayPath, "-count=1", "-timeout", timeout.String(), current.PackagePath()}, timeout+15*time.Second)
	return finish(classify(testErr, output, contextErr), "")
}

// Execute preserves enumeration order despite concurrent mutation tests.
func Execute(root string, mutants []mutate.Mutant, workers int, timeout time.Duration, progress func(int, Result)) ([]Result, error) {
	results := make([]Result, len(mutants))
	jobs := make(chan int, len(mutants))
	done := make(chan int, len(mutants))
	errs := make(chan error, workers)
	for i := range mutants {
		jobs <- i
	}
	close(jobs)
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
				results[i] = Mutant(root, dir, mutants[i], timeout)
				done <- i
			}
		}()
	}
	go func() { group.Wait(); close(done); close(errs) }()
	completed := 0
	for i := range done {
		completed++
		if progress != nil {
			progress(completed, results[i])
		}
	}
	for err := range errs {
		if err != nil {
			return nil, fmt.Errorf("execute mutants: %w", err)
		}
	}
	return results, nil
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

func runGo(root string, arguments []string, timeout time.Duration) ([]byte, error, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
