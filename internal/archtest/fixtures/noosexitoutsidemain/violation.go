//go:build violation

// Package noosexitoutsidemain is a deliberate architecture-test violation
// fixture: it calls os.Exit outside cmd/kairos, which
// TestArchitecture_noOsExitOutsideMain must catch.
package noosexitoutsidemain

import "os"

func bad() {
	os.Exit(1)
}
