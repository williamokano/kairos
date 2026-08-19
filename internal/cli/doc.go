// Package cli holds the cobra verbs. It talks to the daemon only through
// the API client over the unix socket — it never imports internal/api,
// internal/eventstore, os/exec, or internal/executor/* (the same posture
// AGENTS.md §2 holds internal/tui and internal/cli/chat to, applied here
// even though the enforcing architecture test doesn't yet cover this
// package's os/exec-freedom explicitly — see
// TestArchitecture_noExecOutsideExecutor's exemption table, which stays
// exactly {internal/executor/local, cmd/kairos}, never internal/cli).
//
// Starting the daemon process is cmd/kairos/main.go's job, injected here
// as a DaemonStarter — see daemonstart.go.
package cli
