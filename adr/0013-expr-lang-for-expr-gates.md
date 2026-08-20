# 0013 — `github.com/expr-lang/expr` for the `expr` gate kind

**Status:** Accepted
**Date:** 2026-08-20
**Related:** [`AGENTS.md`](../AGENTS.md) §1 · [`05-gates.md`](../05-gates.md) ·
[`L10-constraints-gates.md`](../L10-constraints-gates.md)

## Context

`05-gates.md` specifies an `expr` gate kind — "in-process, free, needs no workspace, and
unbluffable" — evaluating a boolean expression over a node's typed JSON output, with examples
like `all($.output.requirements[*].id, . in $.output.tasks[*].satisfies[])`. Neither
`05-gates.md` nor AGENTS.md's approved-dependency table names an expression-evaluation library:
the table predates this document. Implementing `expr` requires choosing one.

## Decision

**`github.com/expr-lang/expr`.** Pure Go (no cgo, satisfying the hard `CGO_ENABLED=0` darwin/
linux × arm64/amd64 cross-compilation constraint), compiles an expression once and evaluates it
against a `map[string]any`/typed Go value without reflection-heavy interpretation per call,
widely used (the successor to `antonmedv/expr`, itself long-used in Kubernetes-adjacent and CI
tooling), and its expression grammar already includes the array-predicate builtins
(`all`, `any`, `filter`, `map`) `05-gates.md`'s own examples use verbatim — no bespoke JSONPath
engine or query-language subset needs to be built to match the doc's examples.

Alternatives considered and rejected:
- **`github.com/PaesslerAG/gval`** — smaller and less actively maintained; lacks the built-in
  array-predicate functions `05-gates.md`'s examples rely on without custom function
  registration for each one.
- **A bespoke JSONPath + boolean-grammar parser** — matches the doc's `$.output.foo[*]` syntax
  more literally, but is a parser-and-evaluator to build and maintain for a narrow slice
  document, which is exactly the kind of speculative infrastructure AGENTS §7 warns against.
  `expr-lang/expr`'s own syntax (`output.requirements`, no `$.`) is close enough that gate
  authors write `all(output.requirements, .id in map(output.tasks, .satisfies))`-shaped
  expressions instead — a documented syntax difference from `05-gates.md`'s literal examples,
  not a functional gap.

## Consequences

**Good.** One dependency, pure Go, no reflection surprises across the typed JSON `map[string]any`
the node's `NodeOutputReceived.Output` decodes into. Compiling the expression once (at gate-
definition-parse time, in `internal/registry`) and reusing the compiled program on every
evaluation keeps the `expr` kind at the "µs, free" cost `05-gates.md`'s placement table
promises.

**Accepted cost.** Gate authors write `expr-lang/expr`'s dot-path syntax, not the literal
JSONPath (`$.output.foo`) `05-gates.md`'s prose examples show. `L10-constraints-gates.md`
registers this explicitly as a documented decision, not a silent divergence.

**Revisit condition.** If a later document needs literal JSONPath (`$.…`) — e.g. to let an
`effect.confirmation.requested` preview template reuse the same query language end users see in
`05-gates.md`'s docs — reconsider a JSONPath-syntax library or a thin translation layer in front
of `expr-lang/expr`, rather than adding a second expression engine.
