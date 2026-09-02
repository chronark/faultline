# loki

Loki is a mutation tester for Go. It makes one small change to your code, runs your tests, and checks that they fail. If the tests still pass, you have found a gap.

Loki has three flags and no configuration. It never modifies your working tree. Its output is written for humans and for coding agents: every finding carries the diff, the surrounding code, a command to reproduce it, and what to do about it.

```text
$ loki ./pkg/urn
running baseline tests (go test ./pkg/urn)...
baseline passed in 400ms; mutant timeout 30s (5x baseline, min 30s)
enumerated 327 mutants across 1 packages
testing 327 mutants with concurrency 8 (auto: 16 cpus, 31 GiB memory)...
finished 327 mutants in 41.2s
--------------------------------------------------------------------------------
SURVIVED: pkg/urn/urn.go:56:26  > -> >=
function: Covers
key: pkg/urn/urn.go|Covers|> -> >=|len(patternSegments) > 0

-	if len(patternSegments) > 0 && patternSegments[len(patternSegments)-1] == "**" {
+	if len(patternSegments) >= 0 && patternSegments[len(patternSegments)-1] == "**" {

context (pkg/urn/urn.go:48-64):
49	func (v V1) Covers(target V1) bool {
50		if v.WorkspaceID != target.WorkspaceID {
51			return false
52		}
53		patternSegments := strings.Split(v.Resource, "/")
54		targetSegments := strings.Split(target.Resource, "/")
55
56		if len(patternSegments) > 0 && patternSegments[len(patternSegments)-1] == "**" {
...
reproduce: loki --only 'pkg/urn/urn\.go\|Covers\|> -> >=\|len\(patternSegments\) > 0' ./pkg/urn
meaning: this bug was injected and the test suite still passed. Add a test that fails under the mutated line, or add a justified //loki:equivalent marker.
not viable: 12

Summary:
  total: 327
  killed: 301
  timed out: 2
  oom: 0
  survived: 12
  not viable: 12
  allowlisted: 0
  score: 96.19% (killed + timed out + oom) / (total - not viable)
```

## Why mutation testing

Coverage tells you which lines your tests execute. It does not tell you which lines your tests check. A test that calls a function and asserts nothing gives 100% coverage and catches nothing.

Mutation testing measures the second thing. Loki injects a bug, for example `>` becomes `>=`, or a `return err` becomes `return nil`, and runs the tests. A test suite that notices every injected bug will also notice most real ones. A mutant that survives points at a specific line where a specific bug would ship unnoticed.

## Install

With Go:

```bash
go install github.com/chronark/loki@latest
```

With [mise](https://mise.jdx.dev), pinned to a release:

```toml
[tools]
"github:chronark/loki" = "0.1.0"
```

Or download a binary from [GitHub Releases](https://github.com/chronark/loki/releases). Loki has no dependencies beyond the Go toolchain it finds on your `PATH`.

## Use

```text
loki [--json] [--only <regexp>] [--concurrency N] [package...]
```

Run Loki from the module root and pass package directories:

```bash
loki ./pkg/urn ./pkg/rbac
```

With no package argument, Loki tests the current directory, like `go test`. Package patterns with `...` are rejected. List the packages you want to gate; mutation testing is expensive and should run on the code where bugs cost the most.

| Flag | Meaning |
| --- | --- |
| `--json` | Write the report as JSON to stdout. Phase lines still go to stderr. |
| `--only <regexp>` | Run only mutants whose key matches. Every finding prints a ready `--only` command. |
| `--concurrency N` | Number of mutants tested in parallel. Default: `min(cpus / 2, memory / 2 GiB)`. |
| `--version` | Print the version. |

There are no other options. The operator set, the timeout, and the memory budget are Loki's opinion, so every repository gets the same signal.

### How a run works

1. Loki runs `go test` on each package. If the baseline fails, Loki stops with exit code 2. Mutation results on a failing suite are meaningless.
2. Loki parses every non-test, non-generated Go file and enumerates mutants from function bodies. It needs no type information, so enumeration takes milliseconds.
3. For each mutant, Loki writes the mutated file to a temporary directory and runs `go build -overlay` and `go test -overlay -count=1`. The overlay redirects one source path to the mutated copy. Your working tree is never touched.
4. Loki classifies the outcome and prints one report.

### Statuses

| Status | What happened | What to do |
| --- | --- | --- |
| `KILLED` | The tests failed. The bug was caught. | Nothing. |
| `SURVIVED` | The tests passed with the bug in place. | Add a test that fails under the mutated line. |
| `TIMED OUT` | The tests hung. The mutant created an infinite loop. | The loop has a single termination guard. Add an explicit iteration limit or a progress check, so a broken guard fails fast instead of spinning. |
| `OOM` | The kernel killed the test process. The mutant allocated without limit. | Add an explicit size limit on the growing structure. |
| `NOT VIABLE` | The mutant did not compile. | Nothing. It is excluded from the score. |

`TIMED OUT` and `OOM` count as detected in the score, because the tests did notice. Loki still reports them as findings, because a loop that can spin forever after a one-token change has no independent bound.

The score is `(killed + timed out + oom) / (total - not viable)`.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | No findings. Every viable mutant was killed, or is marked equivalent. |
| 1 | At least one `SURVIVED`, `TIMED OUT`, or `OOM` mutant is not marked equivalent. |
| 2 | Usage error, failing baseline, or tool failure. |

Use Loki as a gate in CI by running it and checking the exit code. Nothing else is required.

### Equivalent mutants

Some mutants cannot change behavior. Removing `continue` at the end of a loop body, or replacing `> 0` with `>= 0` on a value that is never zero, produces a program that behaves the same. No test can kill it.

Mark the line in the source:

```go
if len(parts) > 0 { //loki:equivalent parts is never empty here, the caller splits a non-empty string
```

The reason is mandatory. Loki exits with code 2 if the reason is missing. Loki still runs marked mutants. If a marked mutant gets killed, the report says `KILLED (marker is stale, remove it)`, so wrong justifications stay visible.

There is no allowlist file. The marker lives next to the code, moves with it, and shows up in code review.

### Mutant keys

Every mutant has a stable key:

```text
pkg/urn/urn.go|Covers|> -> >=|len(patternSegments) > 0
```

The key is `file|function|mutation|original expression`. It has no line number, so a `--only` selector stays valid when unrelated code moves.

## Operators

Loki always applies all 15 groups.

| Group | Mutation |
| --- | --- |
| conditionals | Swap comparison operators (`<` to `<=`, `==` to `!=`), swap `&&` and `\|\|`, force `if` conditions to `true` and `false` |
| arithmetic | Swap `+ - * / %` |
| incdec | Swap `++` and `--`, swap compound assignments (`+=` to `-=`) |
| negation | Remove `!` |
| boolean | Flip `true` and `false` |
| literal | Shift integer literals by one in each direction |
| errnil | Replace a returned error with `nil` |
| loop | Force a `for` condition to `false` |
| remove | Remove `break`, `continue`, an assignment, or an increment |
| string | Empty a string literal; swap two literals in the same function. Arguments of `errors.New`, `panic`, `fmt.*`, and `*.Errorf`/`*.New`/`*.Wrap*` calls are skipped |
| field | Remove one field from a composite literal |
| slice | Shift slice bounds by one (`a[:n]` to `a[:n-1]`), shift non-literal indexes |
| switch | Remove a `case` or `default` clause; empty a `case` body |
| range | Force a `range` loop to zero iterations or to one iteration |
| return | Replace a non-error return value with its zero value; negate a returned Boolean expression |

Mutants that compile to identical source are deduplicated.

## Timeouts and memory

The per-mutant timeout is `max(30s, 5 × baseline duration)`. Loki runs each `go build` and `go test` in its own process group and kills the whole group at the deadline, so a hanging test binary cannot outlive its mutant.

Each test process runs with `GOMEMLIMIT=1536MiB`. The default concurrency budgets two GiB per worker, so a mutant that allocates without bound slows down and hits the timeout before the machine runs out of memory. Lower `--concurrency` on shared machines.

## Using Loki with coding agents

Loki's report is designed to be pasted into an agent's context. Each finding is self-contained: the diff shows the injected bug, the context shows the function, `reproduce:` gives the exact command, and `meaning:` says whether the fix is a test, a bound, or a marker.

A working loop:

1. Run `loki --json ./pkg/x > findings.json`.
2. Give the agent the findings and this instruction: for each `SURVIVED` mutant, add a test that fails under the mutated line; for each `TIMED OUT` or `OOM` mutant, add an explicit bound in the code; add `//loki:equivalent <reason>` only when no test can distinguish the mutant.
3. The agent runs the `reproduce:` command after each fix. A single mutant takes a few seconds.
4. Run `loki ./pkg/x` again. Exit code 0 means done.

The `--json` document contains the same fields as the human report, plus `durationMs` per mutant and `reason` for `NOT VIABLE` mutants.

## Why no config

Every knob is a way to weaken the gate. A team that can disable operators will disable the ones that find bugs. A team that can raise the timeout will hide the loop that has no bound. Loki's defaults are the same for every repository, so a score means the same thing everywhere.

If a default is wrong for everyone, open an issue.

## Limits

- Loki mutates function bodies only. Package-level declarations, constants, and type definitions are not mutated.
- Enumeration is syntactic. Some mutants do not compile and are reported as `NOT VIABLE`. This is expected and excluded from the score.
- Loki tests each mutant against the tests of its own package. It does not run tests in other packages that import the mutated one.
- Linux and macOS. Process groups are not implemented for Windows.

## Development

```bash
go test -race ./...
```

`e2e/` builds the binary and runs it against a fixture module with known-answer mutants: one `KILLED`, one `SURVIVED`, one `TIMED OUT`, one equivalent-marked, one `NOT VIABLE`. This is how the tester itself is tested. See [docs/architecture.md](docs/architecture.md).

## License

MIT
