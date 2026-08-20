//go:build violation

// Package runcreationreachable is a deliberate architecture-test
// violation fixture: it imports internal/tasksource — masquerading as
// internal/engine's actor dispatch code reaching the one code path that
// creates a Run — which TestArchitecture_runCreationNotReachableFromActors
// must catch.
package runcreationreachable

import _ "github.com/williamokano/kairos/internal/tasksource"
