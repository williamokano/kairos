package engine_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/policy"
)

// TestEngine_parkedEffectConfirmationHoldsNoAdmissionSlot verifies
// 05-gates.md's "RELEASE ALL PERMITS" is trivially true by construction
// here: checkEffects runs BEFORE admission ever grants a claim
// (dispatchStartNode's ordering), so a parked node was never granted a
// slot to release in the first place. With NodeSlots capped at 1, a
// second, independent run's effect node must still be admittable and
// complete while the first stays parked — proof the park holds nothing.
func TestEngine_parkedEffectConfirmationHoldsNoAdmissionSlot(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)

	confirmFake := effect.NewFake("gh.pr.create")
	confirmFake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "acme/backend#1"}
	allowFake := effect.NewFake("git.push")
	allowFake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "kairos/other"}

	eng := engine.New(engine.Config{
		Store:    st,
		Executor: local.New(local.DefaultBootIDProvider()),
		BootID:   local.DefaultBootIDProvider(),
		WorkRoot: testWorkRoot(t), WorkspaceRepo: repo,
		KillGrace: 200 * time.Millisecond,
		Policy: policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{
			"gh.pr.create": {Confirm: "each"}, "git.push": {Allow: "*"},
		}},
		EffectProviders: map[string]effect.Provider{"gh.pr.create": confirmFake, "git.push": allowFake},
		Admission:       admission.Config{NodeSlots: 1, MaxQueued: 10},
		Logger:          slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	// Run 1: parks on a confirm-tier effect, holding (by this document's
	// design) zero admission slots.
	parkedRunID := "run_admission_parked"
	startEffectRun(t, ctx, st, parkedRunID, writeEffectDef(t, "gh.pr.create", "    with: { branch: kairos/fix, base: main, title: Fix }"))

	deadline := time.Now().Add(8 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("run 1 never parked within the deadline")
		}
		state, ok, err := st.GetRunState(ctx, parkedRunID)
		if err == nil && ok {
			execs := state.Executions["n1"]
			if len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecWaiting {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Run 2: a plain allow-tier effect node — with NodeSlots: 1, this
	// only succeeds if run 1's park truly consumed no slot.
	allowRunID := "run_admission_allow"
	startEffectRun(t, ctx, st, allowRunID, writeEffectDef(t, "git.push", "    with: { branch: kairos/other }"))

	status := waitForTerminal(t, ctx, st, allowRunID, 8*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("run 2 status = %q, want %q — a parked confirmation must not hold an admission slot", status, domain.RunSucceeded)
	}
}
