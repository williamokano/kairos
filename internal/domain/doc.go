// Package domain holds Kairos's pure domain model: types and state machines
// with zero I/O. It is the dependency sink — it imports nothing from
// internal/, and never os, os/exec, net, database/sql, syscall, math/rand,
// path/filepath, or time.Now. State transitions are pure functions of
// (state, event, now) so replay reproduces them exactly (L1, L12).
//
// Empty until L01 (domain model). It exists now so
// TestArchitecture_domainPurity checks a real import graph from day one.
package domain
