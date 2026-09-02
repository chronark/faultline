# loki

Loki is a mutation tester for Go. It injects small bugs and checks whether your tests detect them. It never changes your working tree.

## Install

Run `go install github.com/chronark/loki@latest`. You can also download a GoReleaser binary from GitHub Releases.

## Use

```bash
loki [--json] [--only <regexp>] [--concurrency N] [package...]
```

Pass `--json` for machine-readable output. Pass `--only` to select stable mutant keys. Pass `--concurrency` to set workers, or leave it at zero for automatic sizing. Packages default to `.`.

`KILLED` means the test suite caught the bug. `SURVIVED` means you should add a test. `TIMED OUT` or `OOM` means you should add an explicit bound. `NOT VIABLE` means the mutant did not compile. Mark an observably equivalent source line with `//loki:equivalent <reason>`; Loki still executes it.

Exit code 0 means no findings. Exit code 1 means findings remain. Exit code 2 means usage, baseline, or tool failure.

```text
SURVIVED: compare.go:12:10  > -> >=
function: Covers
reproduce: loki --only 'compare\.go\|Covers\|> -> >=\|n > 0' .
```

## Using with coding agents

Run `loki --json` and feed the findings to the agent. Each finding includes the diff, context, reproduce command, and meaning. Loop with `--only` until the mutant is killed.

## Why no config

Loki uses opinionated operators, timeouts, and resource defaults so every repository gets the same signal without maintaining configuration.
