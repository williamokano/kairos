package archtest

import "testing"

// TestArchitecture_runCreationNotReachableFromActors is AGENTS.md §9's
// L15 test: "no Run exists that nobody asked for." internal/tasksource is
// the ONE code path that turns a decision to run something into a Run
// (CreateRun/TriggerRun) — internal/api and internal/tasksource's own
// sources (inbox, poller, cron, webhook, plugin) call it; internal/engine,
// which dispatches actor processes, never may. An actor that could create
// its own Run would let a workflow synthesise work no trigger ever
// authorised — exactly what 01-architecture.md's L15 forbids.
func TestArchitecture_runCreationNotReachableFromActors(t *testing.T) {
	const tasksourcePkg = modulePath + "/internal/tasksource"
	const enginePkg = modulePath + "/internal/engine"

	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./...") {
			if pkg.PkgPath != enginePkg {
				continue
			}
			if hit := importsAny(pkg, tasksourcePkg); len(hit) > 0 {
				t.Errorf("%s imports %s: actor dispatch code must never be able to create a Run directly", pkg.PkgPath, tasksourcePkg)
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/runcreationreachable") {
			if hit := importsAny(pkg, tasksourcePkg); len(hit) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the runcreationreachable fixture to be flagged, but the checker passed it")
		}
	})
}
