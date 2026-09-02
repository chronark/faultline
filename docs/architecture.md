# Architecture

Loki runs a baseline, enumerates mutants, builds each overlay, tests each overlay, classifies the result, and writes one report.

Statuses are `KILLED`, `SURVIVED`, `TIMED OUT`, `OOM`, and `NOT VIABLE`. Exit codes are 0 for no findings, 1 for findings, and 2 for usage or tool failures.

Keys use `file|function|mutation|original syntax`. They omit line numbers so unrelated edits do not invalidate selectors and equivalence markers.

Loki uses `go build -overlay` and `go test -overlay`. An overlay redirects one source path to a temporary mutant, so Loki never touches the working tree. It starts commands in process groups and kills the group at the deadline. The deadline is the greater of 30 seconds and five times the baseline duration.

Tests may set `LOKI_TEST_TIMEOUT_SECONDS` to shorten the deadline. This hook exists only to keep process-level tests fast.
