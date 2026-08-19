//go:build violation

// Package domainpurity is a deliberate architecture-test violation fixture:
// it imports os, which TestArchitecture_domainPurity must catch.
package domainpurity

import "os"

var _ = os.Getenv
