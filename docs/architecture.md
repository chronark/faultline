# Architecture

Placebo expands arguments with `go list`, runs one JSON baseline, enumerates mutants, builds each overlay, tests each overlay, classifies the result, and writes one report.

The baseline maps import paths back to module-relative directories. Passing packages receive individual durations. Placebo skips packages without tests and packages whose baseline fails, then mutates the remaining packages. It preserves the output tail for each failed package.

Statuses are `KILLED`, `SURVIVED`, `TIMED OUT`, `OOM`, and `NOT VIABLE`. Exit codes are 0 for no findings, 1 for findings, and 2 for incomplete runs or errors. A failed baseline or interrupt makes a run incomplete.

Keys use `file|function|mutation|original syntax`. They omit line numbers so unrelated edits do not invalidate selectors and equivalence markers.

Placebo uses `go build -overlay` and `go test -overlay`. An overlay redirects one source path to a temporary mutant, so Placebo never touches the working tree. It starts commands in process groups and kills the group at the deadline. Each package deadline is the greater of 30 seconds and five times that package's baseline duration.

Cancellation stops scheduling mutants and kills in-flight process groups. Placebo reports completed results and the number not tested.

The JSON report records package baselines, timeouts, and mutant counts. It also records untested packages, failed baseline output, and interruption state.

Tests may set `PLACEBO_TEST_TIMEOUT_SECONDS` to shorten the deadline. This hook exists only to keep process-level tests fast.
