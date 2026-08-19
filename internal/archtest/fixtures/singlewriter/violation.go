//go:build violation

// Package singlewriter is a deliberate architecture-test violation
// fixture: its AppendIf method touches a writerDB field directly instead
// of only sending to a request channel, which
// TestArchitecture_singleWriter must catch.
package singlewriter

type store struct {
	writerDB *int
}

func (s *store) AppendIf() error {
	_ = s.writerDB
	return nil
}
