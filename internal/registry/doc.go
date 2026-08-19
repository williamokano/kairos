// Package registry parses and validates workflow definitions — the YAML a
// user writes, like the README's fix-issue.yaml — into two tiers: a rich
// Definition/NodeDef carrying every authored field (actor, prompt, inputs,
// output schema, gates, effects, wait, spawn/join), and a minimal
// domain.Graph (internal/domain, L01) carrying only what domain.Advance
// needs to route a run (ID, Wait, Retry, LoopGuard). ProjectGraph is the
// seam between them.
//
// This is NOT where Domain profiles live (13-domains.md's registry data
// adapting the engine to a class of work — code, inbox, messaging). That
// mechanism is phase-1 scope and lives in this same package eventually,
// in domains.go, which this document does not create. Conflating
// "workflow definition" with "Domain profile" is the AGENTS.md-warned
// mistake that costs a day — they are different things that happen to
// share a package.
package registry
