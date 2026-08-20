// Package engine is the advance loop: it turns events the store commits
// into the Cmds domain.Advance produces for them, and dispatches those
// Cmds to the executor. It is the only consumer of
// internal/eventstore.Store.Subscribe and the only caller of
// internal/executor/local.Executor.
//
// "Decision before action" (01-architecture.md) is satisfied by
// transaction ordering, not by a separate RunAdvanced event: by the time
// a shard goroutine can observe an event on the bus, the NodeExecution
// status it implies is already committed by RunStateProjection inside the
// same AppendIf transaction that produced the event. Reconciliation
// (reconcile.go) re-derives anything undispatched by scanning for
// NodeExecution rows whose status implies an owed action with no recorded
// outcome — see L05-engine.md's Documented decisions for the full
// reasoning behind not adding a RunAdvanced event type.
//
// runShards[N]: every event is routed by hash(runID) to one goroutine, so
// all events for one run are folded and dispatched in order, on one
// goroutine, matching 01-architecture.md's architecture diagram.
package engine
