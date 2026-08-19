//go:build violation

// Package dependencydirection is a deliberate architecture-test violation
// fixture: it imports internal/api, a leaf package nothing should import,
// which TestArchitecture_dependencyDirection must catch.
package dependencydirection

import _ "github.com/williamokano/kairos/internal/api"
