// Package mutate enumerates deterministic source edits without type checking so discovery stays fast.
//
// The operator groups are:
//
//   - conditionals: comparison, logical, and if-condition mutations
//   - arithmetic: arithmetic binary operator swaps
//   - incdec: increment, decrement, and compound assignment swaps
//   - negation: logical negation removal
//   - boolean: Boolean literal flips
//   - literal: integer literal boundary changes
//   - errnil: error return replacement with nil
//   - loop: for-condition forcing to false
//   - remove: break, continue, assignment, and increment removal
//   - string: string literal emptying and same-function swaps
//   - field: composite literal field removal
//   - slice: slice and index bound shifts
//   - switch: case clause removal and emptying
//   - range: range loop iteration forcing
//   - return: non-error return value zeroing
//
// Mutant keys have the stable format file|function|mutation|original. A source
// line may mark observably equivalent mutants with //placebo:equivalent <reason>.
// A reason is mandatory, and marked mutants are still run.
package mutate
