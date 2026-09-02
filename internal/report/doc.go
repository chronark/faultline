// Package report renders mutation results for humans and automation.
//
// The JSON document contains packages with package, baselineMs, timeoutMs, and
// mutants fields; untestedPackages; baselineFailed entries with package and
// output fields; interrupted; concurrency; summary; and mutants. Mutant records
// include source evidence, reproduction details, and durationMs.
package report
