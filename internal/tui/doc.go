// Package tui holds the bubbletea client. It imports neither os/exec nor
// internal/executor/*, and talks to the daemon only through the API client
// — never the engine in-process. In the design this reduced from, that
// boundary was also a network boundary; here TestArchitecture_tuiHasNoExecution
// is the only thing holding it.
//
// Empty until L15 (TUI). It exists now so the test above checks a real
// import graph rather than an absent one.
package tui
