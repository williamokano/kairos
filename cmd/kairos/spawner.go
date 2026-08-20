package main

import (
	"context"

	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/tasksource"
)

// engineSpawner adapts internal/tasksource.TriggerRun to
// internal/engine.RunSpawner (L17) — the composition-root indirection
// that lets a spawn: node's dispatch create a real child Run without
// internal/engine ever importing internal/tasksource itself. See
// engine.Config.Spawner's doc comment for why that import is forbidden.
type engineSpawner struct {
	store  eventstore.Store
	limits tasksource.QueueLimits
}

func (s *engineSpawner) SpawnChild(ctx context.Context, req engine.SpawnChildRequest) (string, error) {
	parentRunID := req.ParentRunID
	runID, _, err := tasksource.TriggerRun(ctx, s.store, req.TriggerRef, "spawn", req.TriggerRef, tasksource.CreateRunRequest{
		DefinitionRef: req.DefinitionRef,
		Params:        req.Params,
		TriggerRef:    req.TriggerRef,
		Actor:         "engine:spawn",
		ParentRunID:   &parentRunID,
	}, s.limits)
	return runID, err
}
